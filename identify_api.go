package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// What the web interface shares between "a download needs you" and "fix a
// title of the library": searching the sources for a unit and applying the
// administrator's choice.

// candidate is a search result offered to the administrator.
type candidate struct {
	Source        string `json:"source"`
	SourceName    string `json:"sourceName"`
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	OriginalTitle string `json:"originalTitle,omitempty"`
	Year          int    `json:"year,omitempty"`
}

// resolveRequest is the administrator's decision about a unit.
type resolveRequest struct {
	Key     string `json:"key"`    // downloads: which unit of the download
	Source  string `json:"source"` // a candidate from the search ...
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`     // ... or an IMDb number, tmdb:ID, a link
	AsMovie bool   `json:"asMovie"` // a series kept as one file
	Season  int    `json:"season"`  // for a file without an episode number
	Episode int    `json:"episode"`
}

var errNeedEpisode = errors.New("this is a series: give the season and the episode of the file, or keep it as a movie")

// candidatesFor looks a unit up in all sources: by the text the
// administrator typed, or by the unit's own title.
func candidatesFor(a *App, u *Unit, query string) []candidate {
	var found []Found
	if query = strings.TrimSpace(query); query != "" {
		// "Форсаж 2026": the year is a hint, not a part of the title.
		title, year := cleanTitle(query)
		if title == "" {
			title = query
		}
		found = a.search(u.Kind, title, year)
	} else {
		found = a.search(u.Kind, u.Title, u.Year)
		if cyr := toCyrillic(u.Title); cyr != "" {
			found = mergeFound(found, a.search(u.Kind, cyr, u.Year))
		}
	}
	list := []candidate{}
	for _, f := range found {
		for i, r := range f.Results {
			if i == 6 {
				break
			}
			list = append(list, candidate{r.Source, f.Provider.Name(), r.ID, r.Kind, r.Title, r.OriginalTitle, r.Year})
		}
	}
	return list
}

// matchFor loads the chosen catalogue entry and settles the cases the
// console dialog asks about: a series picked for a file without an episode
// number, a movie picked for what looked like an episode.
func matchFor(a *App, u *Unit, req resolveRequest) (*Match, error) {
	var m *Match
	var err error
	if req.Ref != "" {
		ref, ok := parseRef(strings.TrimSpace(req.Ref))
		if !ok {
			return nil, errors.New("not an IMDb number, tmdb:ID, kp:ID, tvmaze:ID or a link to one of the catalogues")
		}
		m, err = a.lookupRef(ref, u.Kind)
	} else {
		m, err = a.hub.Load(SearchResult{Source: req.Source, Kind: req.Kind, ID: req.ID})
	}
	if err != nil {
		return nil, err
	}
	switch {
	case m.Show != nil && u.Kind == kindMovie && req.AsMovie:
		m = &Match{Movie: m.Show.AsMovie()}
	case m.Show != nil && u.Kind == kindMovie:
		if req.Episode < 1 {
			return nil, errNeedEpisode
		}
		f := u.Files[0]
		f.Guess.IsSeries, f.Guess.Season, f.Guess.Episodes = true, req.Season, []int{req.Episode}
		u.Kind = kindTV
	case m.Movie != nil && u.Kind == kindTV:
		if len(u.Files) > 1 {
			return nil, fmt.Errorf("%q is a movie, but there are %d files", m.Movie.Title, len(u.Files))
		}
		u.Files[0].Guess.IsSeries = false
		u.Kind = kindMovie
	}
	return m, nil
}

// firstFailure finds what went wrong in the organizer's output.
func firstFailure(out string) error {
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "✗") {
			return errors.New(strings.TrimSpace(strings.TrimPrefix(line, "✗")))
		}
	}
	return nil
}

// fixUnit prepares re-identifying a title of the library: an organizer over
// the whole library, the unit made of the title's files, and the files
// MediaKeeper generated for the current, wrong identity.
func (s *Server) fixUnit(id string, out *bytes.Buffer) (a *App, u *Unit, generated []string, err error) {
	cat, err := s.lib.Catalog()
	if err != nil {
		return nil, nil, nil, err
	}
	providers, _, err := buildProviders(s.cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	ui := NewUI(strings.NewReader(""), out)
	a = &App{ui: ui, root: s.root, out: s.root, yes: true, refresh: true, noTags: s.noTags}
	a.hub = NewHub(providers, func(name, reason string) { ui.Printf("(source %s is off: %s)\n", name, reason) })
	if a.files, err = Scan(s.root); err != nil {
		return nil, nil, nil, err
	}
	byPath := map[string]*MediaFile{}
	for _, f := range a.files {
		byPath[f.Path] = f
	}
	sidecar := func(it *CatItem, suffix string) string {
		return strings.TrimSuffix(it.Path, filepath.Ext(it.Path)) + suffix
	}

	var items []*CatItem
	show := cat.shows[id]
	if it := cat.items[id]; it != nil && it.Kind == kindEpisode {
		show = it.Show // an episode stands for its series
	} else if it != nil {
		items = []*CatItem{it}
		u = &Unit{Kind: kindMovie, Title: it.Title, Year: it.Year}
		generated = append(generated, sidecar(it, ".nfo"))
	}
	if show != nil {
		items = show.Episodes()
		u = &Unit{Kind: kindTV, Title: show.Title, Year: show.Year}
		for _, ep := range items {
			generated = append(generated, sidecar(ep, ".nfo"), sidecar(ep, "-thumb.jpg"))
		}
		if show.Dir != "" {
			generated = append(generated, filepath.Join(show.Dir, "tvshow.nfo"))
			seasonPosters, _ := filepath.Glob(filepath.Join(show.Dir, "season*-poster.jpg"))
			generated = append(generated, seasonPosters...)
		}
	}
	if u == nil {
		return nil, nil, nil, errNotFound
	}
	for _, it := range items {
		f := byPath[it.Path]
		if f == nil {
			return nil, nil, nil, errors.New("the files have changed, reload the page")
		}
		u.Files = append(u.Files, f)
	}
	// Artwork belongs to the title only if the folder does.
	if _, own := a.library(u.Files); own != "" {
		generated = append(generated, filepath.Join(own, "poster.jpg"), filepath.Join(own, "backdrop.jpg"))
	}
	return a, u, generated, nil
}

// fix re-identifies a title of the library as something else: its files are
// renamed and moved as for a new title, and its .nfo and artwork replaced.
// The run is journaled, so "mediakeeper -undo" puts the names back.
func (s *Server) fix(id string, req resolveRequest) error {
	s.organizing.Lock()
	defer s.organizing.Unlock()

	var out bytes.Buffer
	a, u, generated, err := s.fixUnit(id, &out)
	if err != nil {
		return err
	}
	m, err := matchFor(a, u, req)
	if err != nil {
		return err
	}
	a.localize(m)
	plan := &Plan{}
	if m.Show != nil {
		if err := a.AddShow(plan, u, m.Show); err != nil {
			return err
		}
	} else {
		a.AddMovie(plan, u.Files[0], m.Movie)
	}
	// Nothing is touched if the new name is taken by another title.
	for _, it := range plan.Items {
		switch {
		case it.Conflict != "":
			return errors.New(it.Conflict)
		case it.IsDir || it.Move.Src == "" || it.From == it.Move.Dst:
		case exists(it.Move.Dst):
			return fmt.Errorf("%s is already in the library", a.rel(s.root, it.Move.Dst))
		}
	}
	for _, path := range generated {
		os.Remove(path)
	}
	a.Apply(plan)
	s.refresh()
	if err := firstFailure(out.String()); err != nil {
		return err
	}
	title := ""
	if m.Show != nil {
		title = withYear(m.Show.Title, m.Show.Year)
	} else {
		title = withYear(m.Movie.Title, m.Movie.Year)
	}
	s.log("%q is now %s", u.Title, title)
	return nil
}

// fixAPI serves /api/fix/{id} for administrators.
func (s *Server) fixAPI(w http.ResponseWriter, r *http.Request, id, action string) {
	var err error
	switch {
	case action == "search" && r.Method == http.MethodGet:
		var out bytes.Buffer
		a, u, _, ferr := s.fixUnit(id, &out)
		if err = ferr; err == nil {
			writeJSON(w, http.StatusOK, candidatesFor(a, u, r.URL.Query().Get("q")))
			return
		}
	case action == "" && r.Method == http.MethodPost:
		var req resolveRequest
		if err = readJSON(r, &req); err == nil {
			if err = s.fix(id, req); err == nil {
				writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
				return
			}
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	status := http.StatusBadRequest
	if err == errNotFound {
		status = http.StatusNotFound
	}
	apiError(w, status, err)
}
