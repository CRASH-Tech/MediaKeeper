package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

// Editing a series by hand: its description in tvshow.nfo, its artwork and
// season posters, and the titles and descriptions of its episodes. The
// series' title, genres and catalogue numbers are also in the tags of every
// episode's file, so changing them retags the episodes.

var errNoShowFolder = errors.New("the episodes of this series are not in a folder of their own: " +
	"fill the fields in from a catalogue and let the files be renamed, which puts them into one")

// showFolder is the folder a series is described in: the one with its
// tvshow.nfo, or else the folder holding nothing but its episodes. A series
// whose episodes lie among other files has none.
func (s *Server) showFolder(show *CatShow) (string, error) {
	if show.Dir != "" {
		return show.Dir, nil
	}
	var out bytes.Buffer
	a, u, _, err := s.fixUnit(show.ID, &out)
	if err != nil {
		return "", err
	}
	_, own := a.library(u.Files)
	if base := filepath.Base(own); strings.EqualFold(base, showsFolder) || strings.EqualFold(base, moviesFolder) {
		return "", nil // the shared folder of all series, not this one's
	}
	return own, nil
}

// showNode is the description of a series as the episodes' tags need it.
func (s *Server) showNode(show *CatShow) *xmlNode {
	if show.Dir != "" {
		if nfo, err := readNFOFile(filepath.Join(show.Dir, "tvshow.nfo"), "tvshow"); err == nil && !nfo.created {
			return &nfo.root
		}
	}
	n := &xmlNode{}
	n.set("title", show.Title)
	return n
}

// episodeMeta is the part of an episode's description that can be edited.
type episodeMeta struct {
	Title string `json:"title"`
	Aired string `json:"aired"` // YYYY-MM-DD
	Plot  string `json:"plot"`
}

func (e *episodeMeta) check() error {
	if e.Aired != "" && !reDate.MatchString(e.Aired) {
		return errors.New("the date must look like 2001-09-26")
	}
	return nil
}

// episodeView is an episode in the list of the edit form.
type episodeView struct {
	ID         string `json:"id"`
	Season     int    `json:"season"`
	Episode    int    `json:"episode"`
	EpisodeEnd int    `json:"episodeEnd"`
	File       string `json:"file"`
	Thumb      bool   `json:"thumb"`
	episodeMeta
}

func episodeNFOPath(ep *CatItem) string {
	return strings.TrimSuffix(ep.Path, filepath.Ext(ep.Path)) + ".nfo"
}

// episodeTags are the tags of an episode's file: its own title and
// description, and the series it belongs to.
func episodeTags(ep *CatItem, e, show *xmlNode) *TagInfo {
	t := tagsFor(metaFromNFO(show), show, "")
	t.Show, t.Season, t.Episode = t.Title, ep.Season.Number, ep.Episode
	t.Title, t.Date, t.Description = e.text("title"), e.text("aired"), e.text("plot")
	if t.Title == "" {
		t.Title = strings.TrimSuffix(filepath.Base(ep.Path), filepath.Ext(ep.Path))
	}
	return t
}

var reSeasonPart = regexp.MustCompile(`^season(\d{2})$`)

// showMetaAPI serves /api/meta/{series id}:
//
//	GET              the description as it is
//	GET episodes     the episodes with their titles and descriptions
//	POST lookup      the fields filled in from a catalogue entry
//	POST             a new description (JSON metaSave)
//	POST poster      a new poster, backdrop, or season poster (season01)
func (s *Server) showMetaAPI(w http.ResponseWriter, r *http.Request, show *CatShow, part string) {
	dir, err := s.showFolder(show)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	switch {
	case part == "" && r.Method == http.MethodGet:
		m := movieMeta{Title: show.Title, Year: show.Year}
		if dir != "" {
			nfo, err := readNFOFile(filepath.Join(dir, "tvshow.nfo"), "tvshow")
			if err != nil {
				apiError(w, http.StatusBadRequest, err)
				return
			}
			if !nfo.created {
				m = metaFromNFO(&nfo.root)
				m.Title = firstNonEmpty(m.Title, show.Title)
			}
		}
		writeJSON(w, http.StatusOK, struct {
			movieMeta
			NoFolder bool `json:"noFolder,omitempty"`
		}{m, dir == ""})
	case part == "episodes" && r.Method == http.MethodGet:
		list := []episodeView{}
		for _, ep := range show.Episodes() {
			v := episodeView{ID: ep.ID, Season: ep.Season.Number, Episode: ep.Episode, EpisodeEnd: ep.EpisodeEnd,
				File: filepath.Base(ep.Path), Thumb: ep.Thumb != ""}
			if nfo, err := readNFOFile(episodeNFOPath(ep), "episodedetails"); err == nil {
				v.Title, v.Aired, v.Plot = nfo.root.text("title"), nfo.root.text("aired"), nfo.root.text("plot")
			}
			list = append(list, v)
		}
		writeJSON(w, http.StatusOK, list)
	case part == "lookup" && r.Method == http.MethodPost:
		var req resolveRequest
		if err := readJSON(r, &req); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		filled, err := s.lookupMeta(show.ID, req)
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, filled)
	case part == "" && r.Method == http.MethodPost:
		var m metaSave
		if err := readJSON(r, &m); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		if err := m.check(); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		var res map[string]any
		if m.Rename && m.Source != "" {
			res, err = s.refileShow(show, m)
		} else {
			res, err = s.saveShow(show, dir, m)
		}
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	case r.Method == http.MethodPost && (part == "poster" || part == "backdrop" || reSeasonPart.MatchString(part)):
		if dir == "" {
			apiError(w, http.StatusBadRequest, errNoShowFolder)
			return
		}
		dst := filepath.Join(dir, part+".jpg")
		if reSeasonPart.MatchString(part) {
			dst = filepath.Join(dir, part+"-poster.jpg")
		}
		if err := saveUploadedImage(r, dst); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		s.refresh()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": s.showIn(dir, show.ID)})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// saveShow writes the edited description into tvshow.nfo, takes the
// catalogue's artwork if asked to, and carries a new title, genres or
// catalogue numbers over to the episodes.
func (s *Server) saveShow(show *CatShow, dir string, m metaSave) (map[string]any, error) {
	if dir == "" {
		return nil, errNoShowFolder
	}
	nfo, err := readNFOFile(filepath.Join(dir, "tvshow.nfo"), "tvshow")
	if err != nil {
		return nil, err
	}
	s.organizing.Lock()
	before := tagsFor(metaFromNFO(&nfo.root), &nfo.root, "")
	m.apply(&nfo.root)
	if m.Source != "" {
		nfo.setSource(m.Source, m.SourceID, firstNonEmpty(m.SourceKind, kindTV), m.IDs)
	}
	err = nfo.write()
	after := tagsFor(metaFromNFO(&nfo.root), &nfo.root, "")
	var problems []string
	renamed := before.Title != after.Title
	if err == nil && renamed { // the episodes name their series too
		for _, ep := range show.Episodes() {
			e, err := readNFOFile(episodeNFOPath(ep), "episodedetails")
			if err != nil || e.created || e.root.child("showtitle") == nil {
				continue
			}
			e.root.set("showtitle", m.Title)
			if err := e.write(); err != nil {
				problems = append(problems, err.Error())
			}
		}
	}
	s.organizing.Unlock()
	if err != nil {
		return nil, err
	}
	for kind, url := range map[string]string{"poster": m.PosterURL, "backdrop": m.BackdropURL} {
		if url == "" {
			continue
		}
		if err := DownloadFile(url, filepath.Join(dir, kind+".jpg")); err != nil {
			problems = append(problems, fmt.Sprintf("the %s could not be downloaded: %v", kind, err))
		}
	}
	s.refresh()
	s.log("description of the series %q edited by hand", m.Title)

	tags := false
	if !s.noTags && (renamed || before.IMDb != after.IMDb || before.TMDB != after.TMDB ||
		strings.Join(before.Genres, ",") != strings.Join(after.Genres, ",")) {
		var jobs []tagJob
		for _, ep := range show.Episodes() {
			e, err := readNFOFile(episodeNFOPath(ep), "episodedetails")
			if err != nil {
				continue
			}
			jobs = append(jobs, tagJob{ep.Path, episodeTags(ep, &e.root, &nfo.root)})
		}
		tags = fileTags.write(s, jobs...)
	}
	// A series described for the first time is known by its folder from now
	// on: what users did with it follows.
	id := s.showIn(dir, show.ID)
	if id != show.ID {
		s.auth.Moved(map[string]string{show.ID: id})
	}
	return map[string]any{"ok": true, "tags": tags, "problems": problems, "id": id}, nil
}

// refileShow files the series anew after the catalogue entry the fields
// were filled in from: the folder and the episodes are renamed, every
// episode gets its title, description and still from the catalogue. The
// description is then the one from the form, with the changes made by hand.
func (s *Server) refileShow(show *CatShow, m metaSave) (map[string]any, error) {
	req := resolveRequest{Source: m.Source, ID: m.SourceID, Kind: firstNonEmpty(m.SourceKind, kindTV)}
	dir, err := s.fix(show.ID, req, fixOptions{
		keepArt: m.PosterURL == "" && m.BackdropURL == "",
		edit: func(match *Match) {
			if sh := match.Show; sh != nil { // named the way the form says
				sh.Title, sh.Year, sh.Genres = m.Title, m.Year, m.Genres
			}
		},
	})
	if err != nil {
		return nil, err
	}
	nfo, err := readNFOFile(filepath.Join(dir, "tvshow.nfo"), "tvshow")
	if err != nil {
		return nil, err
	}
	s.organizing.Lock()
	m.apply(&nfo.root)
	nfo.setSource(m.Source, m.SourceID, firstNonEmpty(m.SourceKind, kindTV), m.IDs)
	err = nfo.write()
	s.organizing.Unlock()
	if err != nil {
		return nil, err
	}
	s.refresh()
	return map[string]any{"ok": true, "tags": false, "problems": []string{}, "id": s.showIn(dir, "")}, nil
}

// showIn finds the series described in dir after a change: its id follows
// the folder.
func (s *Server) showIn(dir, otherwise string) string {
	if cat, err := s.lib.Catalog(); err == nil {
		for _, show := range cat.Shows {
			if show.Dir == dir {
				return show.ID
			}
		}
	}
	return otherwise
}

// episodeMetaAPI serves /api/meta/{episode id}:
//
//	GET              the title, date and description
//	POST             new ones (JSON episodeMeta)
//	POST thumb       a new still
func (s *Server) episodeMetaAPI(w http.ResponseWriter, r *http.Request, ep *CatItem, part string) {
	nfo, err := readNFOFile(episodeNFOPath(ep), "episodedetails")
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	switch {
	case part == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, episodeMeta{nfo.root.text("title"), nfo.root.text("aired"), nfo.root.text("plot")})
	case part == "" && r.Method == http.MethodPost:
		var e episodeMeta
		if err := readJSON(r, &e); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		if err := e.check(); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		s.organizing.Lock()
		nfo.root.set("title", e.Title)
		if nfo.created { // what a media center needs to place it
			nfo.root.set("showtitle", ep.Show.Title)
			nfo.root.set("season", fmt.Sprint(ep.Season.Number))
			nfo.root.set("episode", fmt.Sprint(ep.Episode))
		}
		nfo.root.set("aired", e.Aired)
		nfo.root.set("plot", e.Plot)
		err := nfo.write()
		s.organizing.Unlock()
		if err != nil {
			apiError(w, http.StatusInternalServerError, err)
			return
		}
		s.refresh()
		tags := !s.noTags && fileTags.write(s, tagJob{ep.Path, episodeTags(ep, &nfo.root, s.showNode(ep.Show))})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tags": tags})
	case part == "thumb" && r.Method == http.MethodPost:
		if err := saveUploadedImage(r, strings.TrimSuffix(ep.Path, filepath.Ext(ep.Path))+"-thumb.jpg"); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		s.refresh()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
