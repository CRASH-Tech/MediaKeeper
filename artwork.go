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

// artwork counts what one title got.
type artwork struct {
	Fetched  int      `json:"fetched"`
	Problems []string `json:"problems"`
	Source   string   `json:"source"` // the catalogue the pictures come from
}

func (a *artwork) from(hub *Hub, source string) {
	a.Source = source
	if p := hub.Get(source); p != nil {
		a.Source = p.Name()
	}
}

func (a *artwork) get(url, dst string, replace bool) {
	if url == "" || (!replace && exists(dst)) {
		return
	}
	if err := DownloadFile(url, dst); err != nil {
		a.Problems = append(a.Problems, fmt.Sprintf("%s: %v", filepath.Base(dst), err))
		return
	}
	a.Fetched++
}

// fetchArtwork takes the artwork of a movie or a series from its catalogue
// entry: what is missing, or with replace the poster, the backdrop and the
// season posters anew. Episode stills are only added where there are none.
func (s *Server) fetchArtwork(hub *Hub, id string, replace bool) (*artwork, error) {
	cat, err := s.lib.Catalog()
	if err != nil {
		return nil, err
	}
	res := &artwork{Problems: []string{}}
	if it := cat.items[id]; it != nil && it.Kind == kindMovie {
		nfo := nfoPathFor(it, it.Root)
		entry, ok := entryOf(nfo, kindMovie)
		if !ok {
			return nil, errNoEntry
		}
		m, err := loadEntry(hub, nfo, entry)
		if err != nil {
			return nil, err
		}
		mv := m.Movie
		if mv == nil {
			mv = m.Show.AsMovie() // a series kept as one file
		}
		res.from(hub, mv.Source)
		res.get(mv.Poster, artPath(it, it.Root, "poster"), replace)
		res.get(mv.Backdrop, artPath(it, it.Root, "backdrop"), replace)
		return res, nil
	}
	show := cat.shows[id]
	if show == nil {
		return nil, errNotFound
	}
	dir, err := s.showFolder(show)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, errNoShowFolder
	}
	nfo := filepath.Join(dir, "tvshow.nfo")
	entry, ok := entryOf(nfo, kindTV)
	if !ok {
		return nil, errNoEntry
	}
	m, err := loadEntry(hub, nfo, entry)
	if err != nil {
		return nil, err
	}
	if m.Show == nil {
		return nil, fmt.Errorf("%s is a movie in the catalogue, not a series", m.Movie.Title)
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
	return res, nil
}

// missingArtwork is the background job that takes the artwork missing in
// the whole library: what the page of settings starts and watches.
type missingArtwork struct {
	mu                          sync.Mutex
	running                     bool
	done, total, fetched, fails int
	last                        string // the title being looked at
}

func (j *missingArtwork) view() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	return map[string]any{"running": j.running, "done": j.done, "total": j.total, "fetched": j.fetched, "failed": j.fails, "title": j.last}
}

// fetchMissingArtwork goes through the movies and series that lack a poster,
// a backdrop, a season poster or an episode still.
func (s *Server) fetchMissingArtwork() error {
	j := &s.artworkJob
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return errors.New("the artwork is being fetched already")
	}
	j.running = true
	j.mu.Unlock()
	cat, err := s.lib.Catalog()
	hub, herr := s.artworkHub()
	if err == nil {
		err = herr
	}
	if err != nil {
		j.mu.Lock()
		j.running = false
		j.mu.Unlock()
		return err
	}
	type title struct{ id, name string }
	var todo []title
	for _, it := range cat.Movies {
		if it.Poster == "" || it.Backdrop == "" {
			todo = append(todo, title{it.ID, it.Title})
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
			todo = append(todo, title{show.ID, show.Title})
		}
	}
	j.mu.Lock()
	j.done, j.total, j.fetched, j.fails = 0, len(todo), 0, 0
	j.mu.Unlock()
	go func() {
		for _, t := range todo {
			j.mu.Lock()
			j.last = t.name
			j.mu.Unlock()
			res, err := s.fetchArtwork(hub, t.id, false)
			j.mu.Lock()
			j.done++
			if err != nil || len(res.Problems) > 0 {
				j.fails++
			}
			if res != nil {
				j.fetched += res.Fetched
			}
			j.mu.Unlock()
			if len(hub.Active()) == 0 {
				break // no source left to ask
			}
		}
		s.refresh()
		j.mu.Lock()
		j.running, j.last = false, ""
		s.log("missing artwork: %d image(s) taken for %d title(s), %d with problems", j.fetched, j.done, j.fails)
		j.mu.Unlock()
	}()
	return nil
}

// artworkAPI serves /api/artwork for administrators: GET how the job goes,
// POST starts it.
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

// titleArtworkAPI serves POST /api/meta/{id}/artwork {replace}: the artwork
// of one title from its catalogue entry.
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
	hub, err := s.artworkHub()
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.fetchArtwork(hub, id, req.Replace)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	s.refresh()
	if res.Fetched == 0 && len(res.Problems) > 0 {
		apiError(w, http.StatusBadGateway, fmt.Errorf("nothing could be downloaded: %s", strings.Join(res.Problems, "; ")))
		return
	}
	writeJSON(w, http.StatusOK, res)
}
