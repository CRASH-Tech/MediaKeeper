package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const incomingDir = ".incoming" // hidden: library scans do not look inside

// Download states.
const (
	stateDownloading = "downloading"
	stateOrganizing  = "organizing"
	stateAttention   = "attention" // downloaded, but a person has to say what it is
	stateDone        = "done"
	stateError       = "error"
)

// Download is one link, magnet or torrent given by an administrator: it is
// fetched into its own folder under .incoming, then identified and moved
// into the library like any other file.
type Download struct {
	ID     string    `json:"id"`
	Source string    `json:"source"`
	Name   string    `json:"name"`
	State  string    `json:"state"`
	Total  int64     `json:"total"`
	Done   int64     `json:"done"`
	Speed  int64     `json:"speed"`
	Error  string    `json:"error,omitempty"`
	Log    string    `json:"log,omitempty"` // what the organizer said
	Added  time.Time `json:"added"`
	Filed  []string  `json:"filed,omitempty"`  // where its videos are in the library now
	Preset *preset   `json:"preset,omitempty"` // what it is, said before it finished
	Root   string    `json:"root,omitempty"`   // the library folder chosen for it; "" goes by kind

	dir    string
	cancel context.CancelFunc
	busy   bool // being organized right now
}

// preset is the administrator's answer to "what is it?" given while the
// download is still running; it is used instead of a search when the
// download turns out to be one movie or one series.
type preset struct {
	resolveRequest
	Label string `json:"label"` // "Inception (2010) · TMDB"
}

type Downloads struct {
	s   *Server
	dir string

	mu   sync.Mutex
	list []*Download

	ariaOnce sync.Once
	aria     *aria2
	ariaErr  error
}

func NewDownloads(s *Server) *Downloads {
	d := &Downloads{s: s}
	d.folder()
	return d
}

var errNoLibrary = errors.New("there is no library folder yet: add one under Settings")

// folder is where downloads are kept: .incoming in the first library folder.
// A library set up while the server runs gets it then. "" while there is
// no library folder.
func (d *Downloads) folder() string {
	d.mu.Lock()
	if d.dir != "" {
		defer d.mu.Unlock()
		return d.dir
	}
	root := d.s.firstRoot()
	if root == "" {
		d.mu.Unlock()
		return ""
	}
	d.dir = filepath.Join(root, incomingDir)
	d.mu.Unlock()
	d.load()
	return d.dir
}

func (d *Downloads) statePath() string { return filepath.Join(d.dir, "downloads.json") }

// load restores the list after a restart. Transfers do not survive one;
// what had been downloaded completely and waits for a decision does.
func (d *Downloads) load() {
	data, err := os.ReadFile(d.statePath())
	if err != nil || json.Unmarshal(data, &d.list) != nil {
		return
	}
	for _, dl := range d.list {
		dl.dir = filepath.Join(d.dir, dl.ID)
		if dl.State == stateDownloading || dl.State == stateOrganizing {
			dl.State, dl.Error = stateError, "interrupted by a restart of the server"
			os.RemoveAll(dl.dir)
		}
	}
}

func (d *Downloads) save() {
	d.mu.Lock()
	data, _ := json.MarshalIndent(d.list, "", " ")
	d.mu.Unlock()
	if d.folder() != "" && os.MkdirAll(d.dir, 0o755) == nil {
		os.WriteFile(d.statePath(), data, 0o644)
	}
}

func (d *Downloads) Close() {
	d.mu.Lock()
	for _, dl := range d.list {
		if dl.cancel != nil {
			dl.cancel()
		}
	}
	d.mu.Unlock()
	if d.aria != nil {
		d.aria.stop()
	}
}

func (d *Downloads) find(id string) *Download {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, dl := range d.list {
		if dl.ID == id {
			return dl
		}
	}
	return nil
}

func (d *Downloads) set(dl *Download, change func()) {
	d.mu.Lock()
	change()
	d.mu.Unlock()
}

// Add starts a download from a link or a magnet; torrent holds the content
// of an uploaded .torrent file instead. root is the library folder to file
// it in, "" for the one of its kind.
func (d *Downloads) Add(source string, torrent []byte, root string) (*Download, error) {
	source = strings.TrimSpace(source)
	if err := d.checkRoot(root); err != nil {
		return nil, err
	}
	isTorrent := torrent != nil || strings.HasPrefix(strings.ToLower(source), "magnet:")
	if torrent == nil && !isTorrent {
		u, err := url.Parse(source)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("give a magnet link, an http(s) link or a .torrent file")
		}
		isTorrent = strings.HasSuffix(strings.ToLower(u.Path), ".torrent")
	}
	if d.folder() == "" {
		return nil, errNoLibrary
	}
	if isTorrent {
		if _, err := d.aria2(); err != nil {
			return nil, err
		}
	}
	dl := &Download{ID: randomHex(8), Source: source, Name: source, State: stateDownloading, Added: time.Now(), Root: root}
	if strings.HasPrefix(source, "magnet:") {
		dl.Name = "magnet link"
		if m := regexp.MustCompile(`[?&]dn=([^&]+)`).FindStringSubmatch(source); m != nil {
			dl.Name, _ = url.QueryUnescape(m[1])
		}
	}
	dl.dir = filepath.Join(d.dir, dl.ID)
	if err := os.MkdirAll(dl.dir, 0o755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	dl.cancel = cancel
	d.mu.Lock()
	d.list = append(d.list, dl)
	d.mu.Unlock()
	d.save()

	go func() {
		var err error
		if isTorrent {
			err = d.fetchTorrent(ctx, dl, source, torrent)
		} else {
			err = d.fetchHTTP(ctx, dl, source)
		}
		if ctx.Err() != nil {
			return // removed by the administrator
		}
		if err != nil {
			d.fail(dl, err)
			return
		}
		d.organize(dl)
	}()
	return dl, nil
}

// checkRoot accepts "" or one of the library folders.
func (d *Downloads) checkRoot(root string) error {
	if root == "" {
		return nil
	}
	for _, r := range d.s.libRoots() {
		if r.Path == root {
			return nil
		}
	}
	return fmt.Errorf("%s is not a library folder", root)
}

// targetRoots are the library folders a download is filed into: the one
// chosen for it first — the only one when it holds both movies and series,
// else titles of the other kind go by kind — or all of them, by kind.
func targetRoots(roots []Root, chosen string) []Root {
	for i, r := range roots {
		if r.Path != chosen {
			continue
		}
		if r.Kind == rootMixed {
			return []Root{r}
		}
		return append([]Root{r}, append(append([]Root(nil), roots[:i]...), roots[i+1:]...)...)
	}
	return roots
}

func (d *Downloads) fail(dl *Download, err error) {
	d.s.log("download %q failed: %v", dl.Name, err)
	d.set(dl, func() { dl.State, dl.Error, dl.Speed = stateError, err.Error(), 0 })
	os.RemoveAll(dl.dir)
	d.save()
}

// Remove cancels a download and deletes whatever of it is still in
// .incoming. Files already moved into the library stay there.
func (d *Downloads) Remove(id string) error {
	d.mu.Lock()
	idx := -1
	for i, dl := range d.list {
		if dl.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		d.mu.Unlock()
		return errNotFound
	}
	dl := d.list[idx]
	if dl.busy {
		d.mu.Unlock()
		return errors.New("the files are being organized right now, try again in a moment")
	}
	d.list = append(d.list[:idx], d.list[idx+1:]...)
	d.mu.Unlock()
	if dl.cancel != nil {
		dl.cancel()
	}
	os.RemoveAll(dl.dir)
	d.save()
	return nil
}

// fetchHTTP downloads a plain link.
func (d *Downloads) fetchHTTP(ctx context.Context, dl *Download, link string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		ResponseHeaderTimeout: 60 * time.Second,
	}}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the server answered %s", resp.Status)
	}

	// The name: what the server suggests, else the end of the address.
	name := ""
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		name = params["filename"]
	}
	if name == "" {
		name, _ = url.PathUnescape(filepath.Base(resp.Request.URL.Path))
	}
	name = filepath.Base(filepath.FromSlash(strings.ReplaceAll(name, `\`, "/")))
	if !videoExts[strings.ToLower(filepath.Ext(name))] {
		return fmt.Errorf("%q is not a video file", name)
	}
	d.set(dl, func() { dl.Name, dl.Total = name, max(resp.ContentLength, 0) })

	f, err := os.Create(filepath.Join(dl.dir, name))
	if err != nil {
		return err
	}
	buf := make([]byte, 256<<10)
	last, lastDone := time.Now(), int64(0)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			d.set(dl, func() {
				dl.Done += int64(n)
				if since := time.Since(last); since >= time.Second {
					dl.Speed = int64(float64(dl.Done-lastDone) / since.Seconds())
					last, lastDone = time.Now(), dl.Done
				}
			})
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if dl.Total > 0 && dl.Done != dl.Total {
		return fmt.Errorf("the connection broke at %d of %d bytes", dl.Done, dl.Total)
	}
	return nil
}

// aria2 starts the torrent engine on first use.
func (d *Downloads) aria2() (*aria2, error) {
	d.ariaOnce.Do(func() { d.aria, d.ariaErr = startAria2(d.dir) })
	return d.aria, d.ariaErr
}

// fetchTorrent downloads a magnet link, a link to a .torrent file or an
// uploaded .torrent through aria2.
func (d *Downloads) fetchTorrent(ctx context.Context, dl *Download, source string, torrent []byte) error {
	aria, err := d.aria2()
	if err != nil {
		return err
	}
	options := map[string]string{"dir": dl.dir}
	var gid string
	if torrent != nil {
		err = aria.call(&gid, "aria2.addTorrent", base64.StdEncoding.EncodeToString(torrent), []string{}, options)
	} else {
		err = aria.call(&gid, "aria2.addUri", []string{source}, options)
	}
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			aria.call(nil, "aria2.forceRemove", gid)
			return ctx.Err()
		case <-time.After(time.Second):
		}
		var st struct {
			Status          string   `json:"status"`
			TotalLength     string   `json:"totalLength"`
			CompletedLength string   `json:"completedLength"`
			DownloadSpeed   string   `json:"downloadSpeed"`
			ErrorMessage    string   `json:"errorMessage"`
			FollowedBy      []string `json:"followedBy"`
			Bittorrent      struct {
				Info struct {
					Name string `json:"name"`
				} `json:"info"`
			} `json:"bittorrent"`
		}
		if err := aria.call(&st, "aria2.tellStatus", gid); err != nil {
			return err
		}
		// A magnet or a link to a .torrent first fetches the description of
		// the torrent, then the real download follows under a new id.
		if st.Status == "complete" && len(st.FollowedBy) > 0 {
			gid = st.FollowedBy[0]
			continue
		}
		d.set(dl, func() {
			if len(st.FollowedBy) == 0 && st.Bittorrent.Info.Name != "" && !strings.HasPrefix(st.Bittorrent.Info.Name, "[METADATA]") {
				dl.Name = st.Bittorrent.Info.Name
				dl.Total, dl.Done = int64(atoi(st.TotalLength)), int64(atoi(st.CompletedLength))
			}
			dl.Speed = int64(atoi(st.DownloadSpeed))
		})
		switch st.Status {
		case "complete":
			return nil
		case "error", "removed":
			return errors.New(firstNonEmpty(st.ErrorMessage, "the download was stopped"))
		}
	}
}

// aria2 is the external program that speaks BitTorrent, driven over its
// JSON-RPC interface on a local port.
type aria2 struct {
	cmd    *exec.Cmd
	url    string
	secret string
}

func startAria2(dir string) (*aria2, error) {
	tool, err := exec.LookPath("aria2c")
	if err != nil {
		return nil, errors.New("aria2c is not installed on the server: torrents and magnet links cannot be downloaded")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	a := &aria2{url: fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", port), secret: randomHex(16)}
	a.cmd = exec.Command(tool, "--enable-rpc", "--rpc-listen-all=false", fmt.Sprintf("--rpc-listen-port=%d", port),
		"--rpc-secret="+a.secret, "--dir="+dir, "--seed-time=0", "--follow-torrent=mem", "--bt-save-metadata=false",
		"--file-allocation=none", "--allow-overwrite=true", "--max-concurrent-downloads=4",
		"--summary-interval=0", "--console-log-level=warn", "--quiet=true",
		fmt.Sprintf("--stop-with-process=%d", os.Getpid()))
	if err := a.cmd.Start(); err != nil {
		return nil, err
	}
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if a.call(nil, "aria2.getVersion") == nil {
			return a, nil
		}
	}
	a.stop()
	return nil, errors.New("aria2c did not start")
}

func (a *aria2) stop() {
	if a.cmd.Process != nil {
		a.cmd.Process.Kill()
		a.cmd.Wait()
	}
}

func (a *aria2) call(result any, method string, params ...any) error {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "mk", "method": method, "params": append([]any{"token:" + a.secret}, params...),
	})
	resp, err := http.Post(a.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return errors.New("aria2c does not answer")
	}
	defer resp.Body.Close()
	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return err
	}
	if reply.Error != nil {
		return errors.New("aria2c: " + reply.Error.Message)
	}
	if result != nil {
		return json.Unmarshal(reply.Result, result)
	}
	return nil
}

var reSample = regexp.MustCompile(`(?i)(^|[^a-z])sample([^a-z]|$)`)

// newApp makes an organizer for a download folder: everything it
// identifies goes into the library.
func (d *Downloads) newApp(dl *Download, out *bytes.Buffer) (*App, error) {
	providers, _, err := buildProviders(d.s.config())
	if err != nil {
		return nil, err
	}
	ui := NewUI(strings.NewReader(""), out)
	roots := d.s.libRoots()
	if len(roots) == 0 {
		return nil, errNoLibrary
	}
	d.mu.Lock()
	roots = targetRoots(roots, dl.Root)
	d.mu.Unlock()
	a := &App{ui: ui, root: dl.dir, out: roots[0].Path, outSet: true, outRoots: roots, yes: true, noTags: d.s.tagsOff(), noJournal: true}
	// A new hub every time: a source that was unreachable an hour ago gets
	// another chance.
	a.hub = NewHub(providers, func(name, reason string) { ui.Printf("(source %s is off: %s)\n", name, reason) })
	if a.files, err = Scan(dl.dir); err != nil {
		return nil, err
	}
	return a, nil
}

// organize identifies what was downloaded and moves it into the library.
// What cannot be identified with confidence stays, waiting for the
// administrator.
func (d *Downloads) organize(dl *Download) {
	d.set(dl, func() { dl.State, dl.Speed, dl.busy = stateOrganizing, 0, true })
	defer d.set(dl, func() { dl.busy = false })

	// Release samples only get in the way.
	filepath.WalkDir(dl.dir, func(path string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && videoExts[strings.ToLower(filepath.Ext(path))] && reSample.MatchString(e.Name()) {
			if st, err := e.Info(); err == nil && st.Size() < 300<<20 {
				os.Remove(path)
			}
		}
		return nil
	})

	var out bytes.Buffer
	a, err := d.newApp(dl, &out)
	if err == nil && len(a.files) == 0 {
		err = errors.New("there are no video files in the download")
	}
	if err != nil {
		d.fail(dl, err)
		return
	}
	plan := &Plan{}
	units := Group(a.files)
	d.mu.Lock()
	p := dl.Preset
	d.mu.Unlock()
	if p != nil && p.Kind == kindTV && len(units) > 1 {
		units = oneSeries(units)
	}
	if p != nil && len(units) != 1 {
		fmt.Fprintf(&out, "(%q was said, but the download holds %d titles: each is identified on its own)\n", p.Label, len(units))
		p = nil
	}
	for _, u := range units {
		var m *Match
		var err error
		if p != nil {
			if m, err = matchFor(a, u, p.resolveRequest); err != nil {
				fmt.Fprintf(&out, "(%s does not fit the files: %v)\n", p.Label, err)
				m = nil
			}
		}
		if m == nil {
			if m, err = a.Identify(u); err != nil {
				fmt.Fprintf(&out, "✗ %s: %v\n", u.Files[0].Rel, err)
			}
		}
		if m != nil {
			d.plan(a, plan, u, m, &out)
		}
	}
	d.finish(dl, a, plan, &out)
}

// oneSeries: a download said to be a series is that series, though its
// episodes guess at the series' name differently ("Show", "Show US", none).
// Its episodes become one unit; anything that is not an episode stays apart.
func oneSeries(units []*Unit) []*Unit {
	var series *Unit
	var out []*Unit
	for _, u := range units {
		switch {
		case u.Kind != kindTV:
			out = append(out, u)
		case series == nil:
			series = u
			out = append(out, u)
		default:
			series.Files = append(series.Files, u.Files...)
		}
	}
	return out
}

func (d *Downloads) plan(a *App, plan *Plan, u *Unit, m *Match, out *bytes.Buffer) {
	a.localize(m)
	if m.Show != nil {
		if err := a.AddShow(plan, u, m.Show); err != nil {
			fmt.Fprintf(out, "✗ %s: %v\n", u.Title, err)
			return
		}
		fmt.Fprintf(out, "✓ %s → %s/%s\n", u.Title, showsFolder, withYear(m.Show.Title, m.Show.Year))
		return
	}
	a.AddMovie(plan, u.Files[0], m.Movie)
	fmt.Fprintf(out, "✓ %s → %s/%s\n", u.Files[0].Rel, moviesFolder, withYear(m.Movie.Title, m.Movie.Year))
}

// finish applies the plan and decides what the download is now: done, or
// waiting for the administrator.
func (d *Downloads) finish(dl *Download, a *App, plan *Plan, out *bytes.Buffer) {
	if len(plan.Items) > 0 {
		for _, it := range plan.Items {
			if it.Conflict != "" {
				fmt.Fprintf(out, "✗ %s: %s\n", filepath.Base(it.From), it.Conflict)
			}
		}
		d.s.organizing.Lock() // one change of the library at a time
		a.Apply(plan)
		d.s.organizing.Unlock()
		d.s.refresh()
		var filed []string
		for _, it := range plan.Items {
			// Moved: there now, gone from the download (a file of the same
			// name already in the library does not count).
			if it.Move.Src != "" && !it.IsDir && it.Conflict == "" && exists(it.Move.Dst) && !exists(it.Move.Src) {
				filed = append(filed, it.Move.Dst)
			}
		}
		d.set(dl, func() { dl.Filed = append(dl.Filed, filed...) })
	}
	left, _ := Scan(dl.dir)
	log := strings.TrimSpace(out.String())
	if len(left) == 0 {
		os.RemoveAll(dl.dir)
		d.set(dl, func() { dl.State, dl.Log = stateDone, log })
		d.s.log("download %q is in the library", dl.Name)
	} else {
		d.set(dl, func() { dl.State, dl.Log = stateAttention, log })
		d.s.log("download %q needs attention: %d file(s) are not identified", dl.Name, len(left))
	}
	d.save()
}

// pendingUnit is a movie or a series of a download that waits for a decision.
type pendingUnit struct {
	Key   string   `json:"key"` // the first file: stable while the unit waits
	Kind  string   `json:"kind"`
	Title string   `json:"title"`
	Year  int      `json:"year"`
	Files []string `json:"files"`
}

func (d *Downloads) pending(dl *Download) []pendingUnit {
	files, _ := Scan(dl.dir)
	var out []pendingUnit
	for _, u := range Group(files) {
		p := pendingUnit{Key: u.Files[0].Rel, Kind: u.Kind, Title: u.Title, Year: u.Year}
		for _, f := range u.Files {
			p.Files = append(p.Files, f.Rel)
		}
		out = append(out, p)
	}
	return out
}

// unit finds a waiting unit again by its key.
func (d *Downloads) unit(dl *Download, key string, out *bytes.Buffer) (*App, *Unit, error) {
	a, err := d.newApp(dl, out)
	if err != nil {
		return nil, nil, err
	}
	for _, u := range Group(a.files) {
		if u.Files[0].Rel == key {
			return a, u, nil
		}
	}
	return nil, nil, errors.New("these files are not waiting any more")
}

// search looks a waiting unit up in all sources; without a key, the
// download itself, by its name, while it is still running.
func (d *Downloads) search(dl *Download, key, query string) ([]candidate, error) {
	var out bytes.Buffer
	if key == "" {
		a, err := d.searcher(&out)
		if err != nil {
			return nil, err
		}
		return candidatesFor(a, guessUnit(dl.Name), query), nil
	}
	a, u, err := d.unit(dl, key, &out)
	if err != nil {
		return nil, err
	}
	return candidatesFor(a, u, query), nil
}

// guessUnit is what a download's name suggests before there are files.
func guessUnit(name string) *Unit {
	g := ParsePath(name)
	u := &Unit{Kind: kindMovie, Title: g.Title, Year: g.Year}
	if g.IsSeries {
		u.Kind = kindTV
	}
	return u
}

// searcher is an organizer that only searches the sources.
func (d *Downloads) searcher(out *bytes.Buffer) (*App, error) {
	providers, _, err := buildProviders(d.s.config())
	if err != nil {
		return nil, err
	}
	ui := NewUI(strings.NewReader(""), out)
	a := &App{ui: ui}
	a.hub = NewHub(providers, func(name, reason string) { ui.Printf("(source %s is off: %s)\n", name, reason) })
	return a, nil
}

// setPreset records what a running download is. The entry is loaded now,
// so that a wrong ID is reported at once and the page can name the choice.
func (d *Downloads) setPreset(dl *Download, req resolveRequest) error {
	if dl.State != stateDownloading {
		return errors.New("the download has finished: choose for its files below")
	}
	var out bytes.Buffer
	a, err := d.searcher(&out)
	if err != nil {
		return err
	}
	var m *Match
	if req.Ref != "" {
		ref, ok := parseRef(strings.TrimSpace(req.Ref))
		if !ok {
			return errors.New("not an IMDb number, tmdb:ID, kp:ID, tvmaze:ID or a link to one of the catalogues")
		}
		m, err = a.lookupRef(ref, guessUnit(dl.Name).Kind)
	} else {
		m, err = a.hub.Load(SearchResult{Source: req.Source, Kind: req.Kind, ID: req.ID})
	}
	if err != nil {
		return err
	}
	a.localize(m)
	p := &preset{}
	// Kept as the entry itself: the files decide later whether a series is
	// one file or many.
	if m.Show != nil {
		p.Source, p.ID, p.Kind = m.Show.Source, m.Show.ID, kindTV
		p.Label = withYear(m.Show.Title, m.Show.Year) + " · " + a.sourceName(m.Show.Source)
	} else {
		p.Source, p.ID, p.Kind = m.Movie.Source, m.Movie.ID, m.Movie.SourceKind()
		p.Label = withYear(m.Movie.Title, m.Movie.Year) + " · " + a.sourceName(m.Movie.Source)
	}
	p.AsMovie = true // a single file of a series is the whole series, as the console asks
	d.set(dl, func() { dl.Preset = p })
	d.save()
	return nil
}

// moved follows a file of a finished download that was renamed in the
// library, so that the page still leads to it.
func (d *Downloads) moved(from, to string) {
	changed := false
	d.mu.Lock()
	for _, dl := range d.list {
		for i, path := range dl.Filed {
			if path == from {
				dl.Filed[i], changed = to, true
			}
		}
	}
	d.mu.Unlock()
	if changed {
		d.save()
	}
}

func (d *Downloads) resolve(dl *Download, req resolveRequest) error {
	d.mu.Lock()
	if dl.busy {
		d.mu.Unlock()
		return errors.New("the files are being organized right now, try again in a moment")
	}
	dl.busy = true
	d.mu.Unlock()
	defer d.set(dl, func() { dl.busy = false })

	var out bytes.Buffer
	a, u, err := d.unit(dl, req.Key, &out)
	if err != nil {
		return err
	}
	m, err := matchFor(a, u, req)
	if err != nil {
		return err
	}
	plan := &Plan{}
	d.plan(a, plan, u, m, &out)
	d.set(dl, func() { dl.State = stateOrganizing })
	d.finish(dl, a, plan, &out)
	return firstFailure(out.String())
}

// discard deletes the files of a waiting unit.
func (d *Downloads) discard(dl *Download, key string) error {
	var out bytes.Buffer
	a, u, err := d.unit(dl, key, &out)
	if err != nil {
		return err
	}
	for _, f := range u.Files {
		os.Remove(f.Path)
		for _, sc := range f.Sidecars {
			os.Remove(sc)
		}
	}
	d.finish(dl, a, &Plan{}, &out)
	return nil
}

// api serves /api/downloads for administrators.
func (d *Downloads) api(w http.ResponseWriter, r *http.Request, parts []string) {
	fail := func(err error) {
		status := http.StatusBadRequest
		if err == errNotFound {
			status = http.StatusNotFound
		}
		apiError(w, status, err)
	}
	if len(parts) == 1 && parts[0] == "libraries" { // where a download can be filed
		list := []map[string]string{}
		for _, r := range d.s.libRoots() {
			list = append(list, map[string]string{"path": r.Path, "kind": r.Kind})
		}
		writeJSON(w, http.StatusOK, list)
		return
	}
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, d.listJSON())
		case http.MethodPost:
			source, torrent, root, err := readDownloadRequest(r)
			if err == nil {
				_, err = d.Add(source, torrent, root)
			}
			if err != nil {
				fail(err)
				return
			}
			writeJSON(w, http.StatusOK, d.listJSON())
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	dl := d.find(parts[0])
	if dl == nil {
		fail(errNotFound)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	var err error
	switch {
	case action == "" && r.Method == http.MethodDelete:
		err = d.Remove(dl.ID)
	case action == "search" && r.Method == http.MethodGet:
		var list []candidate
		if list, err = d.search(dl, r.URL.Query().Get("key"), r.URL.Query().Get("q")); err == nil {
			writeJSON(w, http.StatusOK, list)
			return
		}
	case action == "preset" && r.Method == http.MethodPost:
		var req resolveRequest
		if err = readJSON(r, &req); err == nil {
			err = d.setPreset(dl, req)
		}
	case action == "preset" && r.Method == http.MethodDelete:
		d.set(dl, func() { dl.Preset = nil })
		d.save()
	case action == "root" && r.Method == http.MethodPost:
		var req struct{ Root string }
		if err = readJSON(r, &req); err == nil {
			err = d.setRoot(dl, req.Root)
		}
	case action == "resolve" && r.Method == http.MethodPost:
		var req resolveRequest
		if err = readJSON(r, &req); err == nil {
			err = d.resolve(dl, req)
		}
	case action == "discard" && r.Method == http.MethodPost:
		var req struct{ Key string }
		if err = readJSON(r, &req); err == nil {
			err = d.discard(dl, req.Key)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	writeJSON(w, http.StatusOK, d.listJSON())
}

// setRoot changes the library folder of a download that is not filed yet.
func (d *Downloads) setRoot(dl *Download, root string) error {
	if err := d.checkRoot(root); err != nil {
		return err
	}
	d.mu.Lock()
	state := dl.State
	if state == stateDownloading || state == stateAttention {
		dl.Root = root
	}
	d.mu.Unlock()
	if state != stateDownloading && state != stateAttention {
		return errors.New("this download is filed already")
	}
	d.save()
	return nil
}

// readDownloadRequest accepts JSON {"source": "...", "root": "..."} or a
// form with a "torrent" file (and a "root").
func readDownloadRequest(r *http.Request) (source string, torrent []byte, root string, err error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		file, header, err := r.FormFile("torrent")
		if err != nil {
			return "", nil, "", errors.New("no torrent file in the request")
		}
		defer file.Close()
		torrent, err = io.ReadAll(io.LimitReader(file, 8<<20))
		if err != nil || len(torrent) == 0 || torrent[0] != 'd' {
			return "", nil, "", errors.New("this is not a torrent file")
		}
		return filepath.Base(header.Filename), torrent, r.FormValue("root"), nil
	}
	var req struct{ Source, Root string }
	if err := readJSON(r, &req); err != nil {
		return "", nil, "", err
	}
	return req.Source, nil, req.Root, nil
}

func (d *Downloads) listJSON() []map[string]any {
	d.mu.Lock()
	list := make([]Download, len(d.list))
	for i, dl := range d.list {
		list[i] = *dl
	}
	d.mu.Unlock()
	out := make([]map[string]any, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- { // newest first
		dl := list[i]
		m := map[string]any{"id": dl.ID, "source": dl.Source, "name": dl.Name, "state": dl.State, "total": dl.Total,
			"done": dl.Done, "speed": dl.Speed, "error": dl.Error, "log": dl.Log, "added": dl.Added.Unix()}
		if dl.State == stateAttention {
			m["pending"] = d.pending(&dl)
		}
		if dl.Preset != nil {
			m["preset"] = dl.Preset.Label
		}
		if dl.Root != "" {
			m["root"] = dl.Root
		}
		if titles := d.titles(dl.Filed); len(titles) > 0 {
			m["titles"] = titles
		}
		out = append(out, m)
	}
	return out
}

// titles are the movies and series of the library a download's files are
// in, for links to their pages.
func (d *Downloads) titles(files []string) []map[string]any {
	if len(files) == 0 {
		return nil
	}
	cat, err := d.s.lib.Catalog()
	if err != nil {
		return nil
	}
	wanted := map[string]bool{}
	for _, f := range files {
		wanted[f] = true
	}
	seen := map[string]bool{}
	var out []map[string]any
	for _, it := range cat.items {
		if !wanted[it.Path] {
			continue
		}
		t := map[string]any{"id": it.ID, "kind": "movie", "title": it.Title, "localTitle": it.LocalTitle, "year": it.Year, "poster": it.Poster != ""}
		if it.Kind == kindEpisode {
			show := it.Show
			t = map[string]any{"id": show.ID, "kind": "show", "title": show.Title, "localTitle": show.LocalTitle, "year": show.Year, "poster": show.Poster != ""}
		}
		if id := t["id"].(string); !seen[id] {
			seen[id] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["title"].(string) < out[j]["title"].(string) })
	return out
}
