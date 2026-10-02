package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Artwork taken again from the catalogues: when it could not be downloaded
// while the description was written (a catalogue's image server out of
// reach), or to replace it. A title's catalogue entry is the one its .nfo
// records, so nothing has to be identified again.

// entryOf is the catalogue entry an .nfo describes: the one MediaKeeper
// recorded, else its TMDB or IMDb number.
func entryOf(nfoPath, kind string) (SearchResult, bool) {
	data, err := os.ReadFile(nfoPath)
	if err != nil {
		return SearchResult{}, false
	}
	if m := reNFOMarker.FindSubmatch(data); m != nil {
		return SearchResult{Source: string(m[1]), ID: string(m[2]), Kind: string(m[3])}, true
	}
	if info := readNFO(nfoPath); info != nil {
		if id := nfoID(info, "tmdb"); id != "" {
			return SearchResult{Source: "tmdb", ID: id, Kind: kind}, true
		}
		if id := nfoID(info, "imdb"); id != "" {
			return SearchResult{Source: "imdb", ID: id, Kind: kind}, true
		}
	}
	return SearchResult{}, false
}

// loadEntry loads the entry; when its catalogue is out of reach, the same
// title from another one, by the IMDb number the .nfo has.
func loadEntry(hub *Hub, nfoPath string, entry SearchResult) (*Match, error) {
	m, err := hub.Load(entry)
	if err == nil || entry.Source == "imdb" {
		return m, err
	}
	if info := readNFO(nfoPath); info != nil {
		if imdb := nfoID(info, "imdb"); imdb != "" {
			if other, err2 := hub.LoadByIMDb(imdb); err2 == nil {
				return other, nil
			}
		}
	}
	return nil, err
}

var errNoEntry = errors.New("the description names no catalogue entry: fill it in from a catalogue first")

// artworkHub is a set of sources for taking artwork.
func (s *Server) artworkHub() (*Hub, error) {
	providers, _, err := buildProviders(s.config())
	if err != nil {
		return nil, err
	}
	return NewHub(providers, func(name, reason string) { s.log("source %s is off: %s", name, reason) }), nil
}

// artwork counts what one title got, and gives up on an image server that
// cannot be reached: one picture after another waiting for it would take
// all day.
type artwork struct {
	Fetched  int      `json:"fetched"`
	Problems []string `json:"problems"`
	Source   string   `json:"source"` // the catalogue the pictures come from

	title   string
	log     func(string, ...any)
	picture func() // told of every picture downloaded
	down    map[string]int
	skipped map[string]int
}

func newArtwork(title string, log func(string, ...any), picture func()) *artwork {
	return &artwork{Problems: []string{}, title: title, log: log, picture: picture, down: map[string]int{}, skipped: map[string]int{}}
}

func (a *artwork) from(hub *Hub, source string) {
	a.Source = source
	if p := hub.Get(source); p != nil {
		a.Source = p.Name()
	}
}

func (a *artwork) get(imageURL, dst string, replace bool) {
	if imageURL == "" || (!replace && exists(dst)) {
		return
	}
	host := imageHost(imageURL)
	if a.down[host] >= 2 {
		a.skipped[host]++
		return
	}
	if err := DownloadFile(imageURL, dst); err != nil {
		if !isHTTPStatus(err) { // the server is out of reach
			a.down[host]++
		}
		a.Problems = append(a.Problems, fmt.Sprintf("%s: %v", filepath.Base(dst), err))
		a.log("artwork of %q: %s not downloaded from %s: %v", a.title, filepath.Base(dst), host, err)
		return
	}
	a.Fetched++
	a.log("artwork of %q: %s downloaded from %s", a.title, filepath.Base(dst), host)
	if a.picture != nil {
		a.picture()
	}
}

// done adds what was skipped to the problems.
func (a *artwork) done() {
	for host, n := range a.skipped {
		a.Problems = append(a.Problems, fmt.Sprintf("%s is out of reach: %d more picture(s) not tried", host, n))
		a.log("artwork of %q: %s is out of reach, %d picture(s) skipped", a.title, host, n)
	}
}

// fetchArtwork takes the artwork of a movie or a series from its catalogue
// entry: what is missing, or with replace the poster, the backdrop and the
// season posters anew. Episode stills are only added where there are none.
func (s *Server) fetchArtwork(hub *Hub, id string, replace bool, res *artwork) error {
	cat, err := s.lib.Catalog()
	if err != nil {
		return err
	}
	defer res.done()
	if it := cat.items[id]; it != nil && it.Kind == kindMovie {
		nfo := nfoPathFor(it, it.Root)
		entry, ok := entryOf(nfo, kindMovie)
		if !ok {
			return errNoEntry
		}
		m, err := loadEntry(hub, nfo, entry)
		if err != nil {
			return err
		}
		mv := m.Movie
		if mv == nil {
			mv = m.Show.AsMovie() // a series kept as one file
		}
		res.from(hub, mv.Source)
		res.get(mv.Poster, artPath(it, it.Root, "poster"), replace)
		res.get(mv.Backdrop, artPath(it, it.Root, "backdrop"), replace)
		return nil
	}
	show := cat.shows[id]
	if show == nil {
		return errNotFound
	}
	dir, err := s.showFolder(show)
	if err != nil {
		return err
	}
	if dir == "" {
		return errNoShowFolder
	}
	nfo := filepath.Join(dir, "tvshow.nfo")
	entry, ok := entryOf(nfo, kindTV)
	if !ok {
		return errNoEntry
	}
	m, err := loadEntry(hub, nfo, entry)
	if err != nil {
		return err
	}
	if m.Show == nil {
		return fmt.Errorf("%s is a movie in the catalogue, not a series", m.Movie.Title)
	}
	sh := m.Show
	res.from(hub, sh.Source)
	res.get(sh.Poster, filepath.Join(dir, "poster.jpg"), replace)
	res.get(sh.Backdrop, filepath.Join(dir, "backdrop.jpg"), replace)
	p := hub.Get(sh.Source)
	for _, cs := range show.Seasons {
		var season *Season
		if p != nil {
			if season, err = p.Season(sh.ID, cs.Number); hub.check(p, err) != nil {
				season = nil
			}
		}
		poster := sh.SeasonPosters[cs.Number]
		if season != nil && season.Poster != "" {
			poster = season.Poster
		}
		res.get(poster, filepath.Join(dir, fmt.Sprintf("season%02d-poster.jpg", cs.Number)), replace)
		for _, ep := range cs.Episodes {
			e := season.Episode(ep.Episode)
			if e == nil {
				continue
			}
			stem := strings.TrimSuffix(filepath.Base(ep.Path), filepath.Ext(ep.Path))
			res.get(e.Thumb, filepath.Join(filepath.Dir(ep.Path), stem+"-thumb.jpg"), false)
		}
	}
	return nil
}

// artworkJob takes artwork in the background — for one title from its
// edit sheet, or what is missing in the whole library from the settings —
// and the pages watch how it goes. One at a time.
type missingArtwork struct {
	mu                          sync.Mutex
	running                     bool
	done, total, fetched, fails int
	last                        string   // the title being looked at
	result                      *artwork // of a job for one title
	err                         string
}

func (j *missingArtwork) view() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := map[string]any{"running": j.running, "done": j.done, "total": j.total, "fetched": j.fetched, "failed": j.fails, "title": j.last}
	if j.result != nil {
		v["result"] = j.result
	}
	if j.err != "" {
		v["error"] = j.err
	}
	return v
}

type artTitle struct{ id, name string }

// startArtwork runs a job over the titles.
func (s *Server) startArtwork(todo []artTitle, replace bool, what string) error {
	j := &s.artworkJob
	hub, err := s.artworkHub()
	if err != nil {
		return err
	}
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return errors.New("artwork is being downloaded already: wait until it is done")
	}
	j.running, j.done, j.total, j.fetched, j.fails, j.result, j.err = true, 0, len(todo), 0, 0, nil, ""
	j.mu.Unlock()
	s.log("artwork: downloading %s (%d title(s))", what, len(todo))
	go func() {
		for _, t := range todo {
			j.mu.Lock()
			j.last = t.name
			j.mu.Unlock()
			res := newArtwork(t.name, s.log, func() {
				j.mu.Lock()
				j.fetched++
				j.mu.Unlock()
			})
			err := s.fetchArtwork(hub, t.id, replace, res)
			if err != nil {
				s.log("artwork of %q: %v", t.name, err)
			}
			j.mu.Lock()
			j.done++
			if err != nil || len(res.Problems) > 0 {
				j.fails++
			}
			if len(todo) == 1 {
				j.result = res
				if err != nil {
					j.err = err.Error()
				}
			}
			j.mu.Unlock()
			if len(hub.Active()) == 0 {
				s.log("artwork: no catalogue is left to ask, stopping")
				break
			}
		}
		s.refresh()
		j.mu.Lock()
		j.running, j.last = false, ""
		s.log("artwork: done — %d picture(s) downloaded for %d title(s), %d with problems", j.fetched, j.done, j.fails)
		j.mu.Unlock()
	}()
	return nil
}

// fetchMissingArtwork starts a job over the movies and series that lack a
// poster, a backdrop, a season poster or an episode still.
func (s *Server) fetchMissingArtwork() error {
	cat, err := s.lib.Catalog()
	if err != nil {
		return err
	}
	var todo []artTitle
	for _, it := range cat.Movies {
		if it.Poster == "" || it.Backdrop == "" {
			todo = append(todo, artTitle{it.ID, it.Title})
		}
	}
	for _, show := range cat.Shows {
		lacks := show.Poster == "" || show.Backdrop == ""
		for _, season := range show.Seasons {
			lacks = lacks || season.Poster == ""
			for _, ep := range season.Episodes {
				lacks = lacks || ep.Thumb == ""
			}
		}
		if lacks && show.Dir != "" {
			todo = append(todo, artTitle{show.ID, show.Title})
		}
	}
	return s.startArtwork(todo, false, "what is missing in the library")
}

// artworkAPI serves /api/artwork for administrators: GET how the job goes,
// POST starts it for the whole library.
func (s *Server) artworkAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.artworkJob.view())
	case http.MethodPost:
		if err := s.fetchMissingArtwork(); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, s.artworkJob.view())
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// titleArtworkAPI serves POST /api/meta/{id}/artwork {replace}: a job for
// the artwork of one title, watched at /api/artwork.
func (s *Server) titleArtworkAPI(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct{ Replace bool }
	if err := readJSON(r, &req); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	cat, err := s.lib.Catalog()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	name := ""
	if it := cat.items[id]; it != nil && it.Kind == kindMovie {
		name = it.Title
	} else if show := cat.shows[id]; show != nil {
		name = show.Title
	} else {
		apiError(w, http.StatusNotFound, errNotFound)
		return
	}
	what := "what is missing for " + name
	if req.Replace {
		what = "new artwork for " + name
	}
	if err := s.startArtwork([]artTitle{{id, name}}, req.Replace, what); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.artworkJob.view())
}
