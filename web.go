package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The API of the built-in web interface. Everything needs a login; the
// download and user endpoints need an administrator.

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		return errors.New("malformed request")
	}
	return nil
}

// guest is the visitor who has not signed in: they may browse and watch.
// A cookie tells one guest's browser from another's, so that their video
// conversions do not end each other's.
func (s *Server) guest(w http.ResponseWriter, r *http.Request) *User {
	id := ""
	if c, err := r.Cookie("mk_guest"); err == nil && len(c.Value) == 32 {
		id = c.Value
	} else {
		id = randomHex(16)
		http.SetCookie(w, &http.Cookie{Name: "mk_guest", Value: id, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 3600})
	}
	return &User{ID: "guest-" + id, Name: "guest", Guest: true}
}

func userJSON(u *User) map[string]any {
	return map[string]any{"id": u.ID, "name": u.Name, "admin": u.Admin}
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	arg := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}

	if parts[0] == "login" && r.Method == http.MethodPost {
		var req struct{ Username, Password string }
		if err := readJSON(r, &req); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		u, token, err := s.login(r, req.Username, req.Password, "web")
		if err != nil {
			status := http.StatusUnauthorized
			if err == errTooManyLogins {
				status = http.StatusTooManyRequests
			}
			apiError(w, status, err)
			return
		}
		// Strict: the cookie is never sent by requests other sites make.
		http.SetCookie(w, &http.Cookie{Name: "mk_token", Value: token, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 3600})
		writeJSON(w, http.StatusOK, userJSON(u))
		return
	}

	u := s.user(r)
	if u == nil && s.guests {
		u = s.guest(w, r)
	}
	if u == nil {
		apiError(w, http.StatusUnauthorized, errors.New("sign in first"))
		return
	}
	switch parts[0] {
	case "me":
		me := userJSON(u)
		me["guest"] = u.Guest
		writeJSON(w, http.StatusOK, me)
		return
	case "logout":
		s.auth.Logout(s.token(r))
		http.SetCookie(w, &http.Cookie{Name: "mk_token", Path: "/", MaxAge: -1})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	case "downloads":
		if !u.Admin {
			apiError(w, http.StatusForbidden, errors.New("only an administrator can download"))
			return
		}
		s.dl.api(w, r, parts[1:])
		return
	case "fix":
		if !u.Admin {
			apiError(w, http.StatusForbidden, errors.New("only an administrator can change titles"))
			return
		}
		s.fixAPI(w, r, arg(1), arg(2))
		return
	case "hls":
		s.hls.api(w, r, u, parts[1:])
		return
	case "meta":
		if !u.Admin {
			apiError(w, http.StatusForbidden, errors.New("only an administrator can edit descriptions"))
			return
		}
		s.metaAPI(w, r, arg(1), arg(2))
		return
	case "screens":
		s.screens.api(w, r, u, arg(1), strings.Join(parts[min(2, len(parts)):], "/"))
		return
	case "users":
		if !u.Admin {
			apiError(w, http.StatusForbidden, errors.New("only an administrator can manage users"))
			return
		}
		s.usersAPI(w, r, u, arg(1))
		return
	}

	cat, err := s.lib.Catalog()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	switch parts[0] {
	case "library":
		writeJSON(w, http.StatusOK, s.libraryJSON(cat, u))
	case "image":
		if file := s.imagePath(cat, arg(1), arg(2)); file != "" {
			w.Header().Set("Cache-Control", "private, max-age=3600")
			http.ServeFile(w, r, file)
		} else {
			http.NotFound(w, r)
		}
	case "item", "stream", "transcode", "subs", "progress":
		it := cat.items[arg(1)]
		if it == nil {
			apiError(w, http.StatusNotFound, errNotFound)
			return
		}
		switch parts[0] {
		case "item":
			writeJSON(w, http.StatusOK, s.itemJSON(it, u, true))
		case "stream":
			s.serveVideo(w, r, it, u.Name)
		case "transcode":
			s.transcode(w, r, it, u)
		case "subs":
			n, _ := strconv.Atoi(strings.TrimSuffix(arg(2), ".vtt"))
			offset, _ := strconv.ParseFloat(r.URL.Query().Get("offset"), 64)
			s.subtitles(w, r, it, n, offset)
		case "progress":
			var req struct {
				Position float64 `json:"position"`
				Played   *bool   `json:"played"`
			}
			if err := readJSON(r, &req); err != nil {
				apiError(w, http.StatusBadRequest, err)
				return
			}
			if u.Guest {
				// Nothing is remembered for somebody without an account.
			} else if req.Played != nil { // marked by hand
				s.auth.Update(u.ID, it.ID, func(p *Progress) { p.Played, p.Position = *req.Played, 0 })
			} else {
				s.auth.Watch(u.ID, it.ID, req.Position, s.lib.Duration(it).Seconds())
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		}
	default:
		apiError(w, http.StatusNotFound, errNotFound)
	}
}

func (s *Server) usersAPI(w http.ResponseWriter, r *http.Request, me *User, id string) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.auth.List())
	case http.MethodPost:
		var req struct {
			Name, Password string
			Admin          bool
		}
		if err := readJSON(r, &req); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		if strings.EqualFold(req.Name, me.Name) && !req.Admin {
			apiError(w, http.StatusBadRequest, errors.New("you cannot take the administrator role from yourself"))
			return
		}
		u, err := s.auth.SetUser(req.Name, req.Password, req.Admin)
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, userJSON(u))
	case http.MethodDelete:
		if id == me.ID {
			apiError(w, http.StatusBadRequest, errors.New("you cannot delete yourself"))
			return
		}
		if err := s.auth.DeleteUser(id); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// itemJSON describes a video for the web interface; with details it adds
// what the player needs.
func (s *Server) itemJSON(it *CatItem, u *User, details bool) map[string]any {
	p := s.auth.Progress(u.ID, it.ID)
	m := map[string]any{
		"id": it.ID, "kind": it.Kind, "title": it.Title, "localTitle": it.LocalTitle, "year": it.Year,
		"duration": s.lib.Duration(it).Seconds(), "position": p.Position, "played": p.Played,
		"added": it.ModTime.Unix(), "rating": it.Rating, "genres": it.Genres,
	}
	if it.Kind == kindEpisode {
		m["episode"], m["episodeEnd"], m["season"] = it.Episode, it.EpisodeEnd, it.Season.Number
		m["show"], m["showId"] = it.Show.Title, it.Show.ID
		// The version of its still, so that a new one is not taken from
		// the browser's cache; "" while there is none.
		m["thumb"] = ""
		if still := s.episodeStill(it); still != "" {
			if st, err := os.Stat(still); err == nil {
				m["thumb"] = strconv.FormatInt(st.ModTime().Unix(), 36)
			}
		}
	} else {
		m["poster"], m["backdrop"] = it.Poster != "", it.Backdrop != ""
	}
	if !details {
		return m
	}
	m["plot"], m["originalTitle"], m["tagline"], m["date"] = it.Plot, it.OriginalTitle, it.Tagline, it.Date
	m["imdb"], m["size"], m["file"] = it.IMDb, it.Size, it.Rel
	if len(s.roots) > 1 { // which of the library folders
		m["file"] = filepath.Join(filepath.Base(it.Root), it.Rel)
	}
	info := s.lib.Probe(it)
	var audio []map[string]any
	for _, st := range info.Streams {
		if st.Type == "audio" {
			audio = append(audio, map[string]any{"language": st.Language, "title": st.Title, "codec": st.Codec, "channels": st.Channels})
		}
	}
	m["audio"], m["subtitles"] = audio, len(it.Subs)
	if v := info.stream("video"); v != nil {
		m["video"] = fmt.Sprintf("%s %dx%d", v.Codec, v.Width, v.Height)
	}
	m["canTranscode"] = s.ffmpeg != ""
	// With the tracks known, the player is told whether the browser can
	// take the file as it is; unknown is left for the browser to try.
	if v, a := info.stream("video"), info.stream("audio"); v != nil {
		m["direct"] = browserContainers[strings.ToLower(filepath.Ext(it.Path))] && browserVideo[v.Codec] &&
			!strings.Contains(v.Profile, "10") && !v.Interlaced() && (a == nil || browserAudio[a.Codec])
	}
	return m
}

func (s *Server) libraryJSON(cat *Catalog, u *User) map[string]any {
	people := func(cast []Person) []map[string]string {
		out := make([]map[string]string, 0, len(cast))
		for _, p := range cast {
			out = append(out, map[string]string{"name": p.Name, "role": p.Role})
		}
		return out
	}
	movies := make([]map[string]any, 0, len(cat.Movies))
	for _, it := range cat.Movies {
		m := s.itemJSON(it, u, false)
		m["plot"], m["tagline"], m["originalTitle"], m["mpaa"], m["date"] = it.Plot, it.Tagline, it.OriginalTitle, it.MPAA, it.Date
		m["countries"], m["studios"], m["directors"], m["writers"] = it.Countries, it.Studios, it.Directors, it.Writers
		m["cast"], m["imdb"] = people(it.Cast), it.IMDb
		movies = append(movies, m)
	}
	shows := make([]map[string]any, 0, len(cat.Shows))
	for _, show := range cat.Shows {
		seasons := make([]map[string]any, 0, len(show.Seasons))
		for _, season := range show.Seasons {
			episodes := make([]map[string]any, 0, len(season.Episodes))
			for _, ep := range season.Episodes {
				e := s.itemJSON(ep, u, false)
				e["plot"], e["date"] = ep.Plot, ep.Date
				episodes = append(episodes, e)
			}
			seasons = append(seasons, map[string]any{"id": season.ID, "number": season.Number, "poster": season.Poster != "", "episodes": episodes})
		}
		shows = append(shows, map[string]any{
			"id": show.ID, "title": show.Title, "localTitle": show.LocalTitle, "year": show.Year, "plot": show.Plot,
			"genres": show.Genres, "rating": show.Rating, "poster": show.Poster != "", "backdrop": show.Backdrop != "",
			"added": show.ModTime.Unix(), "seasons": seasons,
			"originalTitle": show.OriginalTitle, "mpaa": show.MPAA, "date": show.Date, "status": show.Status,
			"studios": show.Studios, "cast": people(show.Cast), "imdb": show.IMDb,
		})
	}
	return map[string]any{"name": s.name, "movies": movies, "shows": shows}
}

// What current browsers play without help. Matroska is on the list because
// Chrome and Firefox open it when the tracks inside are these.
var (
	browserContainers = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".webm": true, ".mkv": true}
	browserVideo      = map[string]bool{"h264": true, "vp8": true, "vp9": true, "av1": true}
	browserAudio      = map[string]bool{"aac": true, "mp3": true, "opus": true, "vorbis": true, "flac": true}
)

// transcode streams a video in a form browsers play: MP4 with H.264 and
// AAC. A track that already is what browsers accept is copied, which costs
// almost nothing; anything else is re-encoded on the fly. The stream
// cannot be seeked, so the player asks for a new one from another start;
// the viewer's earlier stream is ended first, which is what a seek is.
func (s *Server) transcode(w http.ResponseWriter, r *http.Request, it *CatItem, u *User) {
	if s.ffmpeg == "" {
		apiError(w, http.StatusNotImplemented, errors.New("ffmpeg is not installed on the server"))
		return
	}
	ctx, done := s.conversions.begin(r.Context(), u.ID)
	defer done()
	if !s.takeSlot(ctx) {
		if ctx.Err() == nil {
			apiError(w, http.StatusServiceUnavailable, errBusyConverting)
		}
		return
	}
	defer func() { <-s.transcodes }()
	who := u.Name
	q := r.URL.Query()
	start, _ := strconv.ParseFloat(q.Get("start"), 64)
	audio, _ := strconv.Atoi(q.Get("audio"))

	args := []string{"-nostdin", "-v", "error"}
	if start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args, "-i", it.Path)
	convert, _ := s.convertArgs(it, audio, false)
	args = append(args, convert...)
	args = append(args, "-f", "mp4", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "pipe:1")

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s.ffmpeg, args...) // killed when the viewer leaves or seeks
	cmd.Stdout, cmd.Stderr = w, &stderr
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	s.log("▶ %s (%s)  %s  [converted, from %s]", who, clientIP(r), it.Title, time.Duration(start)*time.Second)
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		s.log("ffmpeg: %v: %s", err, lastLine(stderr.String()))
	}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

var reSRTTime = regexp.MustCompile(`(\d+):(\d\d):(\d\d)[,.](\d\d\d)`)

// subtitleText reads a subtitle file as UTF-8. Old Russian subtitles are
// usually in Windows-1251.
func subtitleText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if utf8.Valid(data) {
		return string(data), nil
	}
	var b strings.Builder
	for _, c := range data {
		switch {
		case c < 0x80:
			b.WriteByte(c)
		case c >= 0xC0:
			b.WriteRune(rune(c) - 0xC0 + 'А')
		case c == 0xA8:
			b.WriteRune('Ё')
		case c == 0xB8:
			b.WriteRune('ё')
		default:
			b.WriteRune('?')
		}
	}
	return b.String(), nil
}

// subtitles serves an .srt file as WebVTT, the format browsers accept. The
// cues are shifted back by offset seconds: a converted stream that starts
// in the middle of the film counts its time from zero.
func (s *Server) subtitles(w http.ResponseWriter, r *http.Request, it *CatItem, n int, offset float64) {
	if n < 0 || n >= len(it.Subs) {
		http.NotFound(w, r)
		return
	}
	text, err := subtitleText(it.Subs[n])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = reSRTTime.ReplaceAllStringFunc(text, func(m string) string {
		p := reSRTTime.FindStringSubmatch(m)
		ms := (atoi(p[1])*3600+atoi(p[2])*60+atoi(p[3]))*1000 + atoi(p[4]) - int(offset*1000)
		ms = max(ms, 0)
		return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
	})
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	io.WriteString(w, "WEBVTT\n\n"+text)
}
