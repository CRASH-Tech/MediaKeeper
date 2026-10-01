package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web
var webFiles embed.FS

// Server is everything -serve runs on one port: the web interface with its
// API, the Jellyfin-compatible API for native clients, and (unless switched
// off) the DLNA media server.
type Server struct {
	roots  []Root // the folders of the library
	root   string // the first of them, which also holds the downloads and the cache
	name   string
	port   int
	cfg    Config
	noTags bool
	guests bool // the web interface can be watched without signing in
	log    func(format string, args ...any)

	prober *prober
	lib    *Library
	auth   *Auth
	dl     *Downloads
	dlna   *DLNAServer
	ffmpeg string

	transcodes  chan struct{} // limits simultaneous ffmpeg processes
	conversions viewerStreams // the converted streams each viewer has open
	hls        *hlsManager
	screens    *screenMaker
	organizing sync.Mutex // one change of the library at a time

	loginMu  sync.Mutex
	failures map[string][]time.Time // failed logins by address
}

// ServerOptions are the command-line choices for -serve.
type ServerOptions struct {
	Roots  []Root
	Name   string
	Port   int
	DLNA   bool
	Guests bool
	NoTags bool
	Config Config
}

func NewServer(o ServerOptions, log func(string, ...any)) (*Server, error) {
	auth, err := OpenAuth(filepath.Join(filepath.Dir(configPath()), "server.json"))
	if err != nil {
		return nil, err
	}
	if len(o.Roots) == 0 {
		return nil, errors.New("no library folders")
	}
	s := &Server{roots: o.Roots, root: o.Roots[0].Path, name: o.Name, port: o.Port, cfg: o.Config, noTags: o.NoTags, guests: o.Guests, log: log,
		prober: newProber(), auth: auth, transcodes: make(chan struct{}, 2), failures: map[string][]time.Time{}}
	s.ffmpeg, _ = exec.LookPath("ffmpeg")
	s.lib = NewLibrary(o.Roots, s.prober)
	if _, err := s.lib.Catalog(); err != nil {
		return nil, err
	}
	if o.DLNA {
		if s.dlna, err = newDLNAServer(o.Roots, o.Name, o.Port, log, s.prober); err != nil {
			return nil, err
		}
	}
	s.dl = NewDownloads(s)
	s.hls = newHLSManager(s)
	s.screens = newScreenMaker(s)
	return s, nil
}

// takeSlot waits a little for one of the conversion slots: a slot is
// freed a moment after a viewer seeks or leaves, which the server notices
// only when ffmpeg has stopped.
func (s *Server) takeSlot(ctx context.Context) bool {
	wait := time.NewTimer(slotWait)
	defer wait.Stop()
	select {
	case s.transcodes <- struct{}{}:
		return true
	case <-ctx.Done():
	case <-wait.C:
	}
	return false
}

var slotWait = 10 * time.Second // a variable for the tests

// viewerStreams are the converted streams in progress, by viewer. A viewer
// watches one at a time: a new one (a seek, another audio track) ends the
// ones before it at once, rather than whenever the browser gets round to
// dropping their connections.
type viewerStreams struct {
	mu   sync.Mutex
	open map[string][]*viewerStream
}

type viewerStream struct {
	cancel context.CancelFunc
	ended  chan struct{}
}

// begin registers a new stream of a viewer and ends their others, waiting
// (briefly) until they have let go of their slots. done unregisters it.
func (v *viewerStreams) begin(parent context.Context, viewer string) (ctx context.Context, done func()) {
	ctx, cancel := context.WithCancel(parent)
	me := &viewerStream{cancel: cancel, ended: make(chan struct{})}
	v.mu.Lock()
	if v.open == nil {
		v.open = map[string][]*viewerStream{}
	}
	old := v.open[viewer]
	v.open[viewer] = []*viewerStream{me}
	v.mu.Unlock()
	for _, o := range old {
		o.cancel()
	}
	for _, o := range old {
		select {
		case <-o.ended:
		case <-time.After(5 * time.Second):
		}
	}
	return ctx, func() {
		cancel()
		close(me.ended)
		v.mu.Lock()
		list := v.open[viewer][:0]
		for _, o := range v.open[viewer] {
			if o != me {
				list = append(list, o)
			}
		}
		v.open[viewer] = list
		v.mu.Unlock()
	}
}

// rootFor is the library folder a file lies in.
func (s *Server) rootFor(path string) Root {
	if r, ok := rootOf(s.roots, path); ok {
		return r
	}
	return s.roots[0]
}

// refresh makes the catalogue and the DLNA tree show a change at once.
func (s *Server) refresh() {
	s.lib.Invalidate()
	if s.dlna != nil {
		s.dlna.mu.Lock()
		s.dlna.scanned = time.Time{}
		s.dlna.mu.Unlock()
	}
}

var dlnaPrefixes = []string{"/rootDesc.xml", "/scpd/", "/ctl/", "/evt/", "/media/", "/art/", "/sub/"}

func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(webFiles, "web")
	files := http.FileServer(http.FS(static))
	var dlna http.Handler
	if s.dlna != nil {
		dlna = s.dlna.Handler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/" || p == "/index.html" || strings.HasPrefix(p, "/static/"):
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
			return
		case strings.HasPrefix(p, "/api/"):
			s.api(w, r)
			return
		case p == "/favicon.ico" || strings.HasPrefix(p, "/apple-touch-icon"):
			// Browsers ask for these by themselves, whatever the page says:
			// the iPhone for its home screen and bookmarks.
			w.Header().Set("Cache-Control", "max-age=86400")
			r2 := *r
			r2.URL = &url.URL{Path: "/static/apple-touch-icon.png"}
			files.ServeHTTP(w, &r2)
			return
		}
		if dlna != nil {
			for _, prefix := range dlnaPrefixes {
				if strings.HasPrefix(p, prefix) {
					dlna.ServeHTTP(w, r)
					return
				}
			}
		}
		s.jellyfin(w, r)
	})
}

var reAuthToken = regexp.MustCompile(`(?i)\bToken="?([^",\s]+)"?`)

// token finds the login token wherever the client puts it: the cookie of
// the web interface, or the headers and query parameter Jellyfin clients use.
func (s *Server) token(r *http.Request) string {
	if c, err := r.Cookie("mk_token"); err == nil && c.Value != "" {
		return c.Value
	}
	for _, h := range []string{"X-Emby-Token", "X-MediaBrowser-Token"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	for _, h := range []string{"Authorization", "X-Emby-Authorization"} {
		if m := reAuthToken.FindStringSubmatch(r.Header.Get(h)); m != nil {
			return m[1]
		}
	}
	for key, values := range r.URL.Query() {
		if strings.EqualFold(key, "api_key") || strings.EqualFold(key, "ApiKey") {
			return values[0]
		}
	}
	return ""
}

func (s *Server) user(r *http.Request) *User { return s.auth.ByToken(s.token(r)) }

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

const (
	loginWindow   = 5 * time.Minute
	loginAttempts = 10
)

// login checks a password, refusing addresses that keep guessing.
func (s *Server) login(r *http.Request, name, password, device string) (*User, string, error) {
	ip := clientIP(r)
	s.loginMu.Lock()
	recent := s.failures[ip][:0]
	for _, t := range s.failures[ip] {
		if time.Since(t) < loginWindow {
			recent = append(recent, t)
		}
	}
	s.failures[ip] = recent
	blocked := len(recent) >= loginAttempts
	s.loginMu.Unlock()
	if blocked {
		return nil, "", errTooManyLogins
	}
	u, token, err := s.auth.Login(name, password, device)
	if err != nil {
		s.loginMu.Lock()
		s.failures[ip] = append(s.failures[ip], time.Now())
		s.loginMu.Unlock()
		s.log("failed login as %q from %s", name, ip)
	}
	return u, token, err
}

var errTooManyLogins = errors.New("too many failed attempts, try again in a few minutes")

// imagePath finds the image of a movie, episode, series or season. An
// episode's picture is its still, or the frame taken from it; without
// either, a thumb is the series backdrop (as wide as a still) and a poster
// falls back to the season and series posters.
func (s *Server) imagePath(cat *Catalog, id, kind string) string {
	backdrop := kind == "backdrop"
	if it := cat.items[id]; it != nil {
		switch {
		case backdrop && it.Kind == kindMovie:
			return it.Backdrop
		case backdrop:
			return it.Show.Backdrop
		case it.Kind == kindMovie:
			return it.Poster
		case s.episodeStill(it) != "":
			return s.episodeStill(it)
		case kind == "thumb":
			return it.Show.Backdrop
		}
		return firstNonEmpty(it.Season.Poster, it.Show.Poster)
	}
	if season := cat.seasons[id]; season != nil {
		if backdrop {
			return season.Show.Backdrop
		}
		return firstNonEmpty(season.Poster, season.Show.Poster)
	}
	if show := cat.shows[id]; show != nil {
		if backdrop {
			return show.Backdrop
		}
		return show.Poster
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// serveVideo streams a file as it is, with Range support for seeking.
func (s *Server) serveVideo(w http.ResponseWriter, r *http.Request, it *CatItem, who string) {
	f, err := os.Open(it.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", mimeOf(it.Path, false))
	if rng := r.Header.Get("Range"); r.Method == http.MethodGet && (rng == "" || strings.HasPrefix(rng, "bytes=0-")) {
		s.log("▶ %s (%s)  %s", who, clientIP(r), filepath.Base(it.Path))
	}
	http.ServeContent(w, r, "", it.ModTime, f)
}

// ensureAdmin makes sure somebody can sign in. The administrator comes
// from MEDIAKEEPER_ADMIN and MEDIAKEEPER_ADMIN_PASSWORD (which also resets
// a forgotten password); on the very first start a password is generated
// and shown once.
func (s *Server) ensureAdmin(ui *UI) error {
	name := os.Getenv("MEDIAKEEPER_ADMIN")
	if name == "" {
		name = "admin"
	}
	if password := os.Getenv("MEDIAKEEPER_ADMIN_PASSWORD"); password != "" {
		_, err := s.auth.SetUser(name, password, true)
		return err
	}
	if s.auth.HasUsers() {
		return nil
	}
	password := randomHex(6)
	if _, err := s.auth.SetUser(name, password, true); err != nil {
		return err
	}
	ui.Box("Administrator account created", []string{
		"User:     " + name,
		"Password: " + password,
		"",
		"It is shown only now. Change it in the web interface (Users), or set",
		"MEDIAKEEPER_ADMIN_PASSWORD to choose your own.",
	})
	return nil
}

// Serve runs the server until the program is interrupted.
func Serve(ui *UI, o ServerOptions) error {
	var logMu sync.Mutex
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		ui.Printf("%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, args...)...)
	}
	s, err := NewServer(o, logf)
	if err != nil {
		return err
	}
	if err := s.ensureAdmin(ui); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", o.Port))
	if err != nil {
		return fmt.Errorf("cannot listen on port %d (try another one with -port): %w", o.Port, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cat, _ := s.lib.Catalog()
	episodes := 0
	for _, show := range cat.Shows {
		episodes += len(show.Episodes())
	}
	if len(o.Roots) == 1 {
		ui.Printf("MediaKeeper server %s: %d movie(s), %d series, %d episode(s) from %s\n",
			ui.Bold(`"`+o.Name+`"`), len(cat.Movies), len(cat.Shows), episodes, o.Roots[0])
	} else {
		ui.Printf("MediaKeeper server %s: %d movie(s), %d series, %d episode(s) from\n",
			ui.Bold(`"`+o.Name+`"`), len(cat.Movies), len(cat.Shows), episodes)
		for _, r := range o.Roots {
			ui.Printf("  %s\n", r)
		}
	}
	ifaces := localInterfaces()
	for _, i := range ifaces {
		ui.Printf("  http://%s:%d/  (%s)\n", i.ip, o.Port, i.ifi.Name)
	}
	ui.Printf("Open the address in a browser, or add it as a server in a Jellyfin app.\n")

	var discovery *ssdpServer
	if s.dlna != nil {
		discovery = newSSDP(s.dlna, ifaces)
		if err := discovery.start(); err != nil {
			ui.Printf("%s\n", ui.Yellow("DLNA discovery is off ("+err.Error()+"): players will not find the server by themselves."))
		}
	}
	for tool, what := range map[string]string{
		"ffprobe": "durations and codecs are unknown, clients may refuse to play some files",
		"ffmpeg":  "the web player cannot convert formats the browser does not play",
		"aria2c":  "torrents and magnet links cannot be downloaded (plain links still work)",
	} {
		if _, err := exec.LookPath(tool); err != nil {
			ui.Printf("%s\n", ui.Dim(tool+" is not installed: "+what+"."))
		}
	}
	ui.Printf("Press Ctrl+C to stop.\n\n")

	go func() { // watch progress is written out in the background
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				s.auth.Save()
			}
		}
	}()

	go s.screens.run()
	server := &http.Server{Handler: s.Handler()}
	failed := make(chan error, 1)
	go func() { failed <- server.Serve(listener) }()
	select {
	case err = <-failed:
	case <-ctx.Done():
		ui.Printf("\nStopping…\n")
	}
	if discovery != nil {
		discovery.stop() // tells the players that the server is gone
	}
	s.dl.Close()
	s.hls.stopAll()
	s.screens.close()
	s.auth.Save()
	shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
	return err
}
