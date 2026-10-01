package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Subtitles of a video: the tracks inside the file and the .srt files next
// to it. Text tracks are given to players as WebVTT; the tracks inside the
// file are taken out by ffmpeg once — all of them in one reading of the
// file — and kept in the cache. Picture tracks (Blu-ray PGS, DVD) cannot be
// shown by browsers or Apple's players over the video: they are burned into
// the picture of a converted stream.

// imageSubtitles are the picture subtitle codecs, with Jellyfin's names.
var imageSubtitles = map[string]string{
	"hdmv_pgs_subtitle": "pgssub", "dvd_subtitle": "dvdsub", "dvb_subtitle": "dvbsub", "xsub": "xsub",
}

// subtitleTrack is a subtitle track as the web player lists it.
type subtitleTrack struct {
	Key     string `json:"key"` // "e3": stream 3 of the file; "x0": the first .srt next to it
	Label   string `json:"label"`
	Lang    string `json:"lang,omitempty"`
	Image   bool   `json:"image,omitempty"` // pictures: only burned into a converted stream
	Default bool   `json:"default,omitempty"`
	Forced  bool   `json:"forced,omitempty"`
	Ordinal int    `json:"ordinal"` // among the file's subtitle tracks: what to burn in (ffmpeg's 0:s:N)
	stream  int
}

var languageNames = map[string]string{
	"eng": "English", "en": "English", "rus": "Russian", "ru": "Russian", "ukr": "Ukrainian", "uk": "Ukrainian",
	"fre": "French", "fra": "French", "fr": "French", "ger": "German", "deu": "German", "de": "German",
	"spa": "Spanish", "es": "Spanish", "ita": "Italian", "it": "Italian", "por": "Portuguese", "pt": "Portuguese",
	"dut": "Dutch", "nld": "Dutch", "nl": "Dutch", "swe": "Swedish", "sv": "Swedish", "nob": "Norwegian",
	"nor": "Norwegian", "no": "Norwegian", "dan": "Danish", "da": "Danish", "fin": "Finnish", "fi": "Finnish",
	"pol": "Polish", "pl": "Polish", "cze": "Czech", "ces": "Czech", "cs": "Czech", "jpn": "Japanese", "ja": "Japanese",
	"chi": "Chinese", "zho": "Chinese", "zh": "Chinese", "kor": "Korean", "ko": "Korean", "tur": "Turkish", "tr": "Turkish",
	"heb": "Hebrew", "he": "Hebrew", "ara": "Arabic", "ar": "Arabic", "gre": "Greek", "ell": "Greek", "el": "Greek",
	"hun": "Hungarian", "hu": "Hungarian", "rum": "Romanian", "ron": "Romanian", "ro": "Romanian", "bul": "Bulgarian",
}

func languageName(code string) string {
	if name, ok := languageNames[strings.ToLower(code)]; ok {
		return name
	}
	return code
}

// subtitleTracks lists the subtitles a player can offer: text tracks of the
// file, picture tracks of the file, and .srt files next to it.
func (s *Server) subtitleTracks(it *CatItem) []subtitleTrack {
	var out []subtitleTrack
	ordinal := 0
	for _, st := range s.lib.Probe(it).Streams {
		if st.Type != "subtitle" {
			continue
		}
		n := ordinal
		ordinal++
		_, image := imageSubtitles[st.Codec]
		if !image && !jfTextSubtitles[st.Codec] {
			continue // neither text nor a known kind of picture
		}
		label := firstNonEmpty(st.Title, languageName(st.Language), fmt.Sprintf("Track %d", n+1))
		if st.Title != "" && st.Language != "" && !strings.Contains(strings.ToLower(st.Title), strings.ToLower(languageName(st.Language))) {
			label = languageName(st.Language) + " — " + st.Title
		}
		if st.Forced {
			label += " (forced)"
		}
		out = append(out, subtitleTrack{Key: fmt.Sprintf("e%d", st.Index), Label: label, Lang: st.Language,
			Image: image, Default: st.Default, Forced: st.Forced, Ordinal: n, stream: st.Index})
	}
	stem := strings.TrimSuffix(filepath.Base(it.Path), filepath.Ext(it.Path))
	for i, path := range it.Subs {
		// "Film.en.srt", "Film.forced.ru.srt": what is between the names.
		extra := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), stem), filepath.Ext(path)), ".")
		label := "External"
		lang := ""
		if extra != "" {
			parts := strings.Split(extra, ".")
			lang = parts[len(parts)-1]
			label = languageName(lang) + " (file)"
		}
		out = append(out, subtitleTrack{Key: fmt.Sprintf("x%d", i), Label: label, Lang: lang, Ordinal: -1, stream: -1})
	}
	return out
}

// subtitleSRT is the text of a text track as SRT: an .srt next to the file,
// or a track of the file taken out by ffmpeg.
func (s *Server) subtitleSRT(it *CatItem, key string) (string, error) {
	switch {
	case strings.HasPrefix(key, "x") || isNumber(key): // ("3": an .srt, as earlier versions numbered them)
		n, _ := strconv.Atoi(strings.TrimPrefix(key, "x"))
		if n < 0 || n >= len(it.Subs) {
			return "", errNotFound
		}
		return subtitleText(it.Subs[n])
	case strings.HasPrefix(key, "e"):
		stream, err := strconv.Atoi(key[1:])
		if err != nil {
			return "", errNotFound
		}
		return s.embeddedSubtitle(it, stream)
	}
	return "", errNotFound
}

// subtitleCache takes the text tracks out of files, one file at a time.
type subtitleCache struct {
	mu      sync.Mutex
	working map[string]chan struct{} // files being read
}

// embeddedSubtitle is a text track of the file as SRT. Taking tracks out
// reads the whole file, so all its text tracks are taken at once and kept
// in the cache, under a name that changes with the file.
func (s *Server) embeddedSubtitle(it *CatItem, stream int) (string, error) {
	tracks := map[int]bool{}
	for _, st := range s.lib.ProbeNow(it).Streams {
		if st.Type == "subtitle" && jfTextSubtitles[st.Codec] {
			tracks[st.Index] = true
		}
	}
	if !tracks[stream] {
		return "", errNotFound
	}
	sum := md5.Sum([]byte(fmt.Sprintf("%s|%d|%d", it.Path, it.Size, it.ModTime.UnixNano())))
	dir := filepath.Join(s.cacheDir, "subtitles", hex.EncodeToString(sum[:8]))
	file := filepath.Join(dir, fmt.Sprintf("%d.srt", stream))
	if data, err := os.ReadFile(file); err == nil {
		return string(data), nil
	}
	if s.ffmpeg == "" {
		return "", errors.New("ffmpeg is not installed on the server")
	}

	c := &s.subs
	c.mu.Lock()
	if c.working == nil {
		c.working = map[string]chan struct{}{}
	}
	if wait, busy := c.working[dir]; busy { // somebody is taking them out already
		c.mu.Unlock()
		<-wait
		data, err := os.ReadFile(file)
		return string(data), err
	}
	done := make(chan struct{})
	c.working[dir] = done
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.working, dir)
		c.mu.Unlock()
		close(done)
	}()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	args := []string{"-nostdin", "-v", "error", "-y", "-i", it.Path}
	for index := range tracks {
		args = append(args, "-map", fmt.Sprintf("0:%d", index), "-c:s", "srt", "-f", "srt", filepath.Join(dir, fmt.Sprintf("%d.srt.tmp", index)))
	}
	var stderr bytes.Buffer
	cmd := exec.Command(s.ffmpeg, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("the subtitles could not be taken out: %s", lastLine(stderr.String()))
	}
	for index := range tracks {
		tmp := filepath.Join(dir, fmt.Sprintf("%d.srt.tmp", index))
		os.Rename(tmp, strings.TrimSuffix(tmp, ".tmp"))
	}
	data, err := os.ReadFile(file)
	return string(data), err
}

// serveSubtitle answers with a text track as WebVTT, its times moved back by
// offset seconds: a converted stream that starts later starts at zero.
func (s *Server) serveSubtitle(w http.ResponseWriter, r *http.Request, it *CatItem, key string, offset float64) {
	text, err := s.subtitleSRT(it, key)
	if err == errNotFound {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	io.WriteString(w, srtToVTT(text, offset))
}

// srtToVTT turns SRT into WebVTT, shifting the times.
func srtToVTT(text string, offset float64) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = reSRTTime.ReplaceAllStringFunc(text, func(m string) string {
		p := reSRTTime.FindStringSubmatch(m)
		ms := (atoi(p[1])*3600+atoi(p[2])*60+atoi(p[3]))*1000 + atoi(p[4]) - int(offset*1000)
		ms = max(ms, 0)
		return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
	})
	return "WEBVTT\n\n" + text
}
