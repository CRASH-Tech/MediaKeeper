package main

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Screenshots of movies, shown on their pages, and a still of every episode
// that has none of its own (named after the video, -thumb.jpg), so that the
// list of episodes does not show the series poster cropped. They are taken
// in the background by ffmpeg and kept in a hidden folder of the library,
// where media centers and the library scan do not look:
//
//	<cache>/screenshots/<movie id>/01.jpg ... 08.jpg
//	<cache>/stills/<episode id>.jpg
//
// The cache is .cache in the first library folder unless the settings
// (-cache, MEDIAKEEPER_CACHE, server.cache) put it elsewhere.

const (
	screenCount = 8
	screenWidth = 1280
	// Frames near the ends are logos, opening titles and credits.
	screenFrom, screenTo = 0.10, 0.90
	// Between whole passes over the library, for titles added meanwhile.
	screenRescan = 5 * time.Minute
	// An episode's still: a third of the way in, past the recap and the
	// opening titles.
	stillAt, stillWidth = 0.3, 640
)

type screenJob struct {
	id     string
	jitter bool // take other frames than last time
	still  bool // an episode's still, not a movie's screenshots
}

type screenMaker struct {
	s *Server

	mu      sync.Mutex
	urgent  []screenJob     // asked for by someone looking at the page
	pending map[string]bool // queued or being taken
	failed  map[string]bool // ffmpeg could not take them: not retried until a restart
	wake    chan struct{}
	stop    chan struct{}
}

func newScreenMaker(s *Server) *screenMaker {
	return &screenMaker{s: s,
		pending: map[string]bool{}, failed: map[string]bool{}, wake: make(chan struct{}, 1), stop: make(chan struct{})}
}

// The folders follow the settings, which may change while the server runs.
func (m *screenMaker) shotsDir() string  { return filepath.Join(m.s.cacheRoot(), "screenshots") }
func (m *screenMaker) stillsDir() string { return filepath.Join(m.s.cacheRoot(), "stills") }

func (m *screenMaker) folder(id string) string { return filepath.Join(m.shotsDir(), id) }

func (m *screenMaker) stillPath(id string) string { return filepath.Join(m.stillsDir(), id+".jpg") }

// episodeStill is the picture of an episode: its own still, or the frame
// taken from it; "" while there is neither.
func (s *Server) episodeStill(it *CatItem) string {
	if it.Thumb != "" {
		return it.Thumb
	}
	if s.screens != nil {
		if p := s.screens.stillPath(it.ID); exists(p) {
			return p
		}
	}
	return ""
}

// shots lists the screenshots of a movie in order.
func (m *screenMaker) shots(id string) []string {
	files, _ := filepath.Glob(filepath.Join(m.folder(id), "[0-9][0-9].jpg"))
	sort.Strings(files)
	return files
}

// request puts a movie at the front of the queue.
func (m *screenMaker) request(id string, jitter bool) {
	m.mu.Lock()
	if jitter {
		delete(m.failed, id)
	}
	if !m.pending[id] || jitter {
		m.pending[id] = true
		m.urgent = append(m.urgent, screenJob{id: id, jitter: jitter})
	}
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *screenMaker) state(id string) (shots []string, pending bool) {
	m.mu.Lock()
	pending = m.pending[id]
	m.mu.Unlock()
	if pending {
		return nil, true
	}
	return m.shots(id), false
}

// run takes screenshots until the server stops: first those somebody is
// waiting for, then, in a quiet moment, every movie that has none.
func (m *screenMaker) run() {
	if m.s.ffmpeg == "" {
		return
	}
	m.prune()
	for {
		job, urgent := m.next()
		if job.id == "" {
			select {
			case <-m.stop:
				return
			case <-m.wake:
			case <-time.After(screenRescan):
			}
			continue
		}
		// Background work waits while somebody watches a converted video:
		// both need the processor.
		for !urgent && len(m.s.transcodes) > 0 {
			select {
			case <-m.stop:
				return
			case <-time.After(10 * time.Second):
			}
		}
		var err error
		key := job.id
		if job.still {
			key = "still:" + job.id
			err = m.takeStill(job.id)
		} else {
			err = m.take(job)
		}
		m.mu.Lock()
		delete(m.pending, key)
		if err != nil {
			m.failed[key] = true
		}
		m.mu.Unlock()
		what := map[bool]string{true: "a still", false: "screenshots"}[job.still]
		if err != nil {
			m.s.log("screenshots: %s of %s: %v", what, m.s.nameOf(job.id), err)
		} else {
			m.s.log("screenshots: %s of %s taken", what, m.s.nameOf(job.id))
		}
		select {
		case <-m.stop:
			return
		default:
		}
	}
}

func (m *screenMaker) close() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

// next returns the next job: an urgent one, else an episode without a
// still (one frame, quick, and seen in every list of episodes), else a
// movie without screenshots.
func (m *screenMaker) next() (screenJob, bool) {
	m.mu.Lock()
	if len(m.urgent) > 0 {
		job := m.urgent[0]
		m.urgent = m.urgent[1:]
		m.mu.Unlock()
		return job, true
	}
	m.mu.Unlock()
	cat, err := m.s.lib.Catalog()
	if err != nil {
		return screenJob{}, false
	}
	for _, show := range cat.Shows {
		for _, it := range show.Episodes() {
			key := "still:" + it.ID
			m.mu.Lock()
			if it.Thumb == "" && !m.pending[key] && !m.failed[key] && !exists(m.stillPath(it.ID)) {
				m.pending[key] = true
				m.mu.Unlock()
				return screenJob{id: it.ID, still: true}, false
			}
			m.mu.Unlock()
		}
	}
	for _, it := range cat.Movies {
		m.mu.Lock()
		skip := m.pending[it.ID] || m.failed[it.ID]
		if !skip && len(m.shots(it.ID)) == 0 {
			m.pending[it.ID] = true
			m.mu.Unlock()
			return screenJob{id: it.ID}, false
		}
		m.mu.Unlock()
	}
	return screenJob{}, false
}

// take replaces a movie's screenshots. The new set is made in a temporary
// folder, so the page never shows half of it.
func (m *screenMaker) take(job screenJob) error {
	cat, err := m.s.lib.Catalog()
	if err != nil {
		return err
	}
	it := cat.items[job.id]
	if it == nil {
		return nil // gone meanwhile
	}
	info := m.s.prober.probe(it.Path) // the duration has to be known now, not some time later
	duration := info.Duration.Seconds()
	if duration <= 0 {
		duration = float64(it.Runtime * 60)
	}
	if duration <= 0 {
		return fmt.Errorf("%s: the length of the video is unknown", it.Title)
	}
	filters := fmt.Sprintf("thumbnail=40,scale='min(%d,iw)':-2", screenWidth)
	if v := info.stream("video"); v != nil && v.Interlaced() {
		filters = "bwdif=mode=send_frame," + filters
	}

	if err := os.MkdirAll(m.shotsDir(), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(m.shotsDir(), ".new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	step := (screenTo - screenFrom) / screenCount
	for i := 0; i < screenCount; i++ {
		at := screenFrom + step*(float64(i)+0.5)
		if job.jitter { // a clearly different moment, still within its share of the film
			shift := (0.2 + rand.Float64()*0.25) * step
			if rand.IntN(2) == 0 {
				shift = -shift
			}
			at += shift
		}
		out := filepath.Join(tmp, fmt.Sprintf("%02d.jpg", i+1))
		cmd := exec.Command(m.s.ffmpeg, "-nostdin", "-v", "error", "-ss", strconv.FormatFloat(at*duration, 'f', 2, 64),
			"-i", it.Path, "-map", "0:v:0", "-vf", filters, "-frames:v", "1", "-q:v", "4", "-y", out)
		if msg, err := cmd.CombinedOutput(); err != nil || !exists(out) {
			return fmt.Errorf("%s: %v %s", it.Title, err, lastLine(string(msg)))
		}
	}
	dst := m.folder(job.id)
	os.RemoveAll(dst)
	return os.Rename(tmp, dst)
}

// takeStill takes the still of an episode.
func (m *screenMaker) takeStill(id string) error {
	cat, err := m.s.lib.Catalog()
	if err != nil {
		return err
	}
	it := cat.items[id]
	if it == nil || it.Thumb != "" {
		return nil // gone, or given a still of its own meanwhile
	}
	info := m.s.prober.probe(it.Path)
	duration := info.Duration.Seconds()
	if duration <= 0 {
		duration = float64(it.Runtime * 60)
	}
	if duration <= 0 {
		return fmt.Errorf("%s: the length of the video is unknown", it.Rel)
	}
	filters := fmt.Sprintf("thumbnail=40,scale='min(%d,iw)':-2", stillWidth)
	if v := info.stream("video"); v != nil && v.Interlaced() {
		filters = "bwdif=mode=send_frame," + filters
	}
	if err := os.MkdirAll(m.stillsDir(), 0o755); err != nil {
		return err
	}
	dst := m.stillPath(id)
	tmp := dst + ".tmp.jpg" // the page never gets half a file
	defer os.Remove(tmp)
	cmd := exec.Command(m.s.ffmpeg, "-nostdin", "-v", "error", "-ss", strconv.FormatFloat(stillAt*duration, 'f', 2, 64),
		"-i", it.Path, "-map", "0:v:0", "-vf", filters, "-frames:v", "1", "-q:v", "4", "-y", tmp)
	if msg, err := cmd.CombinedOutput(); err != nil || !exists(tmp) {
		return fmt.Errorf("still of %s: %v %s", it.Rel, err, lastLine(string(msg)))
	}
	return os.Rename(tmp, dst)
}

// prune removes the screenshots and stills of titles that are not in the
// library any more (deleted, or renamed: the id follows the file name), and
// the stills of episodes that got one of their own.
func (m *screenMaker) prune() {
	cat, err := m.s.lib.Catalog()
	if err != nil {
		return
	}
	entries, _ := os.ReadDir(m.shotsDir())
	for _, e := range entries {
		if cat.items[e.Name()] == nil {
			os.RemoveAll(filepath.Join(m.shotsDir(), e.Name()))
		}
	}
	stills, _ := os.ReadDir(m.stillsDir())
	for _, e := range stills {
		if it := cat.items[strings.TrimSuffix(e.Name(), ".jpg")]; it == nil || it.Thumb != "" {
			os.Remove(filepath.Join(m.stillsDir(), e.Name()))
		}
	}
}

// api serves /api/screens/{id}[/{n}.jpg | /regenerate].
func (m *screenMaker) api(w http.ResponseWriter, r *http.Request, u *User, id, rest string) {
	cat, err := m.s.lib.Catalog()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	if it := cat.items[id]; it == nil || it.Kind != kindMovie {
		apiError(w, http.StatusNotFound, errNotFound)
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		if m.s.ffmpeg == "" {
			writeJSON(w, http.StatusOK, map[string]any{"state": "unavailable", "shots": []string{}})
			return
		}
		shots, pending := m.state(id)
		m.mu.Lock()
		failed := m.failed[id]
		m.mu.Unlock()
		if len(shots) == 0 && !pending && !failed {
			m.request(id, false) // somebody is looking: do it now
			pending = true
		}
		urls := []string{}
		for _, f := range shots {
			st, _ := os.Stat(f)
			urls = append(urls, fmt.Sprintf("/api/screens/%s/%s?v=%d", id, filepath.Base(f), st.ModTime().Unix()))
		}
		state := "ready"
		switch {
		case pending:
			state = "pending"
		case failed:
			state = "failed"
		}
		writeJSON(w, http.StatusOK, map[string]any{"state": state, "shots": urls})
	case rest == "regenerate" && r.Method == http.MethodPost:
		if !u.Admin {
			apiError(w, http.StatusForbidden, errors.New("only an administrator can change screenshots"))
			return
		}
		if m.s.ffmpeg == "" {
			apiError(w, http.StatusNotImplemented, errors.New("ffmpeg is not installed on the server"))
			return
		}
		m.request(id, true)
		m.s.log("%s asked for new screenshots of %s", u.Name, m.s.nameOf(id))
		writeJSON(w, http.StatusOK, map[string]string{"state": "pending"})
	case strings.HasSuffix(rest, ".jpg") && r.Method == http.MethodGet:
		file := filepath.Join(m.folder(id), filepath.Base(rest))
		if len(filepath.Base(rest)) != len("01.jpg") || !exists(file) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=86400") // the address changes with the file
		http.ServeFile(w, r, file)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
