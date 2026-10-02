package main

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// What each user keeps for themselves: their rating of a title, their
// watchlist, favourites and notes, and the history of what they watched.
// Guests (the web interface without signing in) have none of it.

// mineJSON adds a user's own marks to the description of a title; only
// what is set, to keep the library small.
func mineJSON(m map[string]any, p Progress) {
	if p.Rating > 0 {
		m["myRating"] = p.Rating
	}
	if !p.Planned.IsZero() {
		m["planned"] = p.Planned.Unix()
	}
	if p.Favorite {
		m["favorite"] = true
	}
	if p.Note != "" {
		m["note"] = p.Note
	}
	if !p.LastPlayed.IsZero() {
		m["lastPlayed"] = p.LastPlayed.Unix()
	}
}

var errNoAccount = errors.New("sign in to keep your own ratings, lists and history")

// mineAPI serves POST /api/mine/{title}: {rating, planned, favorite, note},
// each optional. A title is a movie, an episode or a series.
func (s *Server) mineAPI(w http.ResponseWriter, r *http.Request, cat *Catalog, u *User, id string) {
	if u.Guest {
		apiError(w, http.StatusForbidden, errNoAccount)
		return
	}
	if cat.items[id] == nil && cat.shows[id] == nil {
		apiError(w, http.StatusNotFound, errNotFound)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Rating   *int    `json:"rating"`
		Planned  *bool   `json:"planned"`
		Favorite *bool   `json:"favorite"`
		Note     *string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	if req.Rating != nil && (*req.Rating < 0 || *req.Rating > 10) {
		apiError(w, http.StatusBadRequest, errors.New("a rating goes from 1 to 10 (0 removes it)"))
		return
	}
	if req.Note != nil && len(*req.Note) > 4000 {
		apiError(w, http.StatusBadRequest, errors.New("the note is too long"))
		return
	}
	p := s.auth.Update(u.ID, id, func(p *Progress) {
		if req.Rating != nil {
			p.Rating = *req.Rating
		}
		if req.Planned != nil {
			switch {
			case !*req.Planned:
				p.Planned = time.Time{}
			case p.Planned.IsZero():
				p.Planned = time.Now()
			}
		}
		if req.Favorite != nil {
			p.Favorite = *req.Favorite
		}
		if req.Note != nil {
			p.Note = strings.TrimSpace(*req.Note)
		}
	})
	name := ""
	if it := cat.items[id]; it != nil {
		name = titleOf(it)
	} else {
		name = withYear(cat.shows[id].Title, cat.shows[id].Year)
	}
	switch {
	case req.Rating != nil && *req.Rating > 0:
		s.log("★ %s rated %s %s/5", u.Name, name, strings.TrimSuffix(strconv.FormatFloat(float64(*req.Rating)/2, 'f', 1, 64), ".0"))
	case req.Rating != nil:
		s.log("%s removed their rating of %s", u.Name, name)
	case req.Planned != nil:
		s.log("%s %s", u.Name, map[bool]string{true: "put " + name + " on their watchlist", false: "took " + name + " off their watchlist"}[*req.Planned])
	case req.Favorite != nil:
		s.log("%s %s", u.Name, map[bool]string{true: "added " + name + " to their favorites", false: "removed " + name + " from their favorites"}[*req.Favorite])
	case req.Note != nil:
		s.log("%s wrote a note on %s", u.Name, name)
	}
	m := map[string]any{"ok": true}
	mineJSON(m, p)
	writeJSON(w, http.StatusOK, m)
}

// historyAPI serves a user's history:
//
//	GET    /api/history?after={cursor}&limit={n}  the entries, latest first
//	DELETE /api/history/{id}                     forget one
//	DELETE /api/history                          forget all
func (s *Server) historyAPI(w http.ResponseWriter, r *http.Request, cat *Catalog, u *User, id string) {
	if u.Guest {
		apiError(w, http.StatusForbidden, errNoAccount)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		n, _ := strconv.ParseInt(id, 10, 64)
		if id != "" && n <= 0 {
			apiError(w, http.StatusNotFound, errNotFound)
			return
		}
		if err := s.auth.ForgetHistory(u.ID, n); err != nil {
			status := http.StatusInternalServerError
			if err == errNotFound {
				status = http.StatusNotFound
			}
			apiError(w, status, err)
			return
		}
		if id == "" {
			s.log("%s cleared their history", u.Name)
		} else {
			s.log("%s removed an entry from their history", u.Name)
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case http.MethodGet:
		q := r.URL.Query()
		after := parseHistoryCursor(q.Get("after"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		entries, err := s.auth.History(u.ID, after, limit)
		if err != nil {
			apiError(w, http.StatusInternalServerError, err)
			return
		}
		list := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			list = append(list, s.historyJSON(cat, e))
		}
		out := map[string]any{"entries": list}
		if len(entries) == limit {
			last := entries[len(entries)-1]
			out["next"] = HistoryCursor{last.Started.Unix(), last.ID}.String()
		}
		if after.Started == 0 {
			now := time.Now()
			out["stats"] = s.auth.Stats(u.ID, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()))
		}
		writeJSON(w, http.StatusOK, out)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// historyJSON is a history entry with what the page needs to show it: a
// link and a picture while the title is still in the library.
func (s *Server) historyJSON(cat *Catalog, e HistoryEntry) map[string]any {
	m := map[string]any{
		"id": e.ID, "kind": e.Kind, "title": e.Title, "year": e.Year, "showTitle": e.ShowTitle,
		"season": e.Season, "episode": e.Episode, "started": e.Started.Unix(), "ended": e.Ended.Unix(),
		"watched": e.Watched, "position": e.Position, "duration": e.Duration, "finished": e.Finished,
	}
	it := cat.items[e.ItemID]
	switch {
	case it != nil && it.Kind == kindMovie:
		m["itemId"], m["link"] = it.ID, "#movie/"+it.ID
		if it.Poster != "" {
			m["image"] = "/api/image/" + it.ID + "/poster"
		}
	case it != nil:
		m["itemId"], m["link"] = it.ID, "#show/"+it.Show.ID
		if still := s.episodeStill(it); still != "" {
			if st, err := os.Stat(still); err == nil {
				m["image"], m["wide"] = "/api/image/"+it.ID+"/thumb?v="+strconv.FormatInt(st.ModTime().Unix(), 36), true
			}
		} else if it.Show.Poster != "" {
			m["image"] = "/api/image/" + it.Show.ID + "/poster"
		}
	case cat.shows[e.ShowID] != nil: // the episode is gone, the series is not
		show := cat.shows[e.ShowID]
		m["link"] = "#show/" + show.ID
		if show.Poster != "" {
			m["image"] = "/api/image/" + show.ID + "/poster"
		}
	}
	return m
}
