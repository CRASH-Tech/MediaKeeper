package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// What happens on the server is told on its standard output, for whoever
// runs it: who signs in, what is watched and how far, what is rated,
// edited, downloaded, what appears in the library.

// titleOf names a video for the log: "Title (Year)" or "Show S01E02".
func titleOf(it *CatItem) string {
	if it.Kind == kindEpisode && it.Show != nil {
		name := fmt.Sprintf("%s S%02dE%02d", it.Show.Title, it.Season.Number, it.Episode)
		if it.Title != "" && it.Title != it.Show.Title {
			name += " " + it.Title
		}
		return name
	}
	return withYear(it.Title, it.Year)
}

// clock is a position for the log: "1:02:03".
func clock(sec float64) string {
	d := time.Duration(sec) * time.Second
	return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}

// watchLog keeps viewing in the log readable: players report the position
// every few seconds, the log tells how far a viewer got every few minutes,
// the end, and a stop.
type watchLog struct {
	mu   sync.Mutex
	last map[string]time.Time // user and video -> when last told
}

const watchLogEvery = 5 * time.Minute

// watched tells of a report of a player: how far, finished, stopped.
func (s *Server) watched(u *User, it *CatItem, before, after Progress, position, duration float64, stopped bool) {
	key := u.ID + "/" + it.ID
	l := &s.watchLog
	l.mu.Lock()
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	due := time.Since(l.last[key]) >= watchLogEvery
	if due || stopped || (after.Played && !before.Played) {
		l.last[key] = time.Now()
	}
	l.mu.Unlock()
	share := ""
	if duration > 0 {
		share = fmt.Sprintf(" (%d%%)", min(100, int(position*100/duration)))
	}
	switch {
	case after.Played && !before.Played:
		s.log("✓ %s watched %s to the end", u.Name, titleOf(it))
	case stopped:
		s.log("■ %s stopped %s at %s of %s%s", u.Name, titleOf(it), clock(position), clock(duration), share)
	case due && position > 0:
		s.log("… %s is watching %s: %s of %s%s", u.Name, titleOf(it), clock(position), clock(duration), share)
	}
}

// catalogChanges tells what appeared in the library and what is gone.
func catalogChanges(old, cat *Catalog, log func(string, ...any)) {
	if log == nil {
		return
	}
	episodes := 0
	for _, it := range cat.items {
		if it.Kind == kindEpisode {
			episodes++
		}
	}
	if old == nil {
		log("library: %d movie(s), %d series, %d episode(s)", len(cat.Movies), len(cat.Shows), episodes)
		return
	}
	var added, gone []string
	for id, it := range cat.items {
		if it.Kind == kindMovie && old.items[id] == nil {
			added = append(added, titleOf(it))
		}
	}
	for id, it := range old.items {
		if it.Kind == kindMovie && cat.items[id] == nil {
			gone = append(gone, titleOf(it))
		}
	}
	for id, show := range cat.shows {
		if old.shows[id] == nil {
			added = append(added, withYear(show.Title, show.Year)+" (series)")
		} else if n, was := len(show.Episodes()), len(old.shows[id].Episodes()); n > was {
			added = append(added, fmt.Sprintf("%s: %d new episode(s)", show.Title, n-was))
		}
	}
	for id, show := range old.shows {
		if cat.shows[id] == nil {
			gone = append(gone, withYear(show.Title, show.Year)+" (series)")
		}
	}
	tell := func(sign string, list []string) {
		sort.Strings(list)
		for i, t := range list {
			if i == 10 {
				log("library: %s and %d more", sign, len(list)-10)
				break
			}
			log("library: %s %s", sign, t)
		}
	}
	tell("+", added)
	tell("−", gone)
}

// statusRecorder notes the status of an answer: an action is told only
// when it worked.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// told runs a handler and, when it succeeded, tells what was done.
func told(w http.ResponseWriter, handle func(http.ResponseWriter), tell func()) {
	rec := &statusRecorder{w, http.StatusOK}
	handle(rec)
	if rec.status < 400 && tell != nil {
		tell()
	}
}

// nameOf names a title or a video of the catalogue by its id, for the log.
func (s *Server) nameOf(id string) string {
	cat, err := s.lib.Catalog()
	if err != nil {
		return id
	}
	if it := cat.items[id]; it != nil {
		return titleOf(it)
	}
	if show := cat.shows[id]; show != nil {
		return withYear(show.Title, show.Year)
	}
	for _, show := range cat.Shows {
		for _, season := range show.Seasons {
			if season.ID == id {
				return fmt.Sprintf("%s season %d", show.Title, season.Number)
			}
		}
	}
	return id
}

// editTold is what an edit of a title is, for the log; "" for what only
// reads.
func editTold(method, part string) string {
	if method != http.MethodPost {
		return ""
	}
	switch {
	case part == "":
		return "saved the description of"
	case part == "lookup" || part == "episodes":
		return ""
	case part == "thumb":
		return "uploaded a new still for"
	case reSeasonPart.MatchString(part):
		return "uploaded a new poster of season " + strings.TrimLeft(strings.TrimPrefix(part, "season"), "0") + " for"
	}
	return "uploaded a new " + part + " for"
}
