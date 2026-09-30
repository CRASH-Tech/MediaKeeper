package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HLS is how Safari (macOS, iPad, iPhone) gets converted video. It refuses
// to play a plain stream of unknown length — it downloads such a stream to
// the end first — but plays HLS natively: a playlist of short segments that
// ffmpeg keeps adding to while the film is being watched.
//
// A session is one ffmpeg run from a given moment of a video. Seeking
// outside what has been produced starts a new session.

const (
	hlsSegmentSeconds = 6
	hlsIdle           = 3 * time.Minute // nobody asks for anything: the viewer has left

	// A copied video track can only be cut at its key frames, which may lie
	// 10-20 s apart, so its segments vary in length. The playlist announces
	// their maximum (EXT-X-TARGETDURATION), and it must never change while
	// the video plays: ffmpeg raises it whenever a longer segment comes, and
	// Safari then rejects every later version of the playlist and stops a
	// few minutes in. So the announced maximum is fixed for the session:
	// exact for a re-encoded track, generous for a copied one.
	hlsCopyTarget = 30
	// With such a long target Safari rereads the playlist only every half
	// minute, so the first version it gets must already reach that far.
	hlsCopyFirstSegments = 5
	hlsAhead             = 20 // segments ffmpeg may run ahead of the viewer ...
	hlsResume            = 10 // ... and where it is let go again
)

type hlsSession struct {
	id, dir, user string
	cmd           *exec.Cmd
	stderr        bytes.Buffer
	exited        chan struct{}

	target        int // the fixed EXT-X-TARGETDURATION of the playlist
	firstSegments int // segments the first playlist waits for

	mu      sync.Mutex
	last    time.Time // of the latest request
	lastSeg int       // the highest segment asked for
	paused  bool
	stopped bool
}

type hlsManager struct {
	s        *Server
	mu       sync.Mutex
	sessions map[string]*hlsSession
}

func newHLSManager(s *Server) *hlsManager {
	return &hlsManager{s: s, sessions: map[string]*hlsSession{}}
}

// start launches a conversion of the video from the given second. A user
// watches one converted video at a time: their earlier sessions are ended,
// which is also what a seek amounts to.
func (m *hlsManager) start(it *CatItem, user *User, from float64, audio int) (*hlsSession, error) {
	s := m.s
	if s.ffmpeg == "" {
		return nil, errors.New("ffmpeg is not installed on the server")
	}
	m.mu.Lock()
	var old []*hlsSession
	for _, sess := range m.sessions {
		if sess.user == user.ID {
			old = append(old, sess)
		}
	}
	m.mu.Unlock()
	for _, sess := range old {
		m.stop(sess)
	}
	select {
	case s.transcodes <- struct{}{}:
	default:
		return nil, errBusyConverting
	}
	dir, err := os.MkdirTemp("", "mediakeeper-hls-")
	if err != nil {
		<-s.transcodes
		return nil, err
	}
	args := []string{"-nostdin", "-v", "error"}
	if from > 0 {
		args = append(args, "-ss", strconv.FormatFloat(from, 'f', 3, 64))
	}
	args = append(args, "-i", it.Path)
	convert, copied := s.convertArgs(it, audio, true)
	args = append(args, convert...)
	args = append(args, "-f", "hls", "-hls_time", strconv.Itoa(hlsSegmentSeconds), "-hls_list_size", "0",
		"-hls_playlist_type", "event", "-hls_flags", "independent_segments+temp_file",
		"-hls_segment_filename", filepath.Join(dir, "seg%05d.ts"), filepath.Join(dir, "index.m3u8"))

	sess := &hlsSession{id: randomHex(12), dir: dir, user: user.ID, last: time.Now(), exited: make(chan struct{}),
		target: hlsSegmentSeconds, firstSegments: 1}
	if copied {
		sess.target, sess.firstSegments = hlsCopyTarget, hlsCopyFirstSegments
	}
	sess.cmd = exec.Command(s.ffmpeg, args...)
	sess.cmd.Stderr = &sess.stderr
	if err := sess.cmd.Start(); err != nil {
		os.RemoveAll(dir)
		<-s.transcodes
		return nil, err
	}
	go func() {
		sess.cmd.Wait()
		close(sess.exited)
	}()
	m.mu.Lock()
	m.sessions[sess.id] = sess
	m.mu.Unlock()
	go m.watch(sess)
	s.log("▶ %s  %s  [converted for HLS, from %s]", user.Name, it.Title, time.Duration(from)*time.Second)
	return sess, nil
}

var errBusyConverting = errors.New("the server is busy converting other videos")

// watch ends a session nobody reads, and holds ffmpeg back when it has run
// far ahead of the viewer: copying a video track is much faster than
// watching it, and the whole film would land in the temporary folder.
func (m *hlsManager) watch(sess *hlsSession) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		sess.mu.Lock()
		idle, asked, stopped := time.Since(sess.last), sess.lastSeg, sess.stopped
		sess.mu.Unlock()
		if stopped {
			return
		}
		if idle > hlsIdle {
			m.stop(sess)
			return
		}
		segments, _ := filepath.Glob(filepath.Join(sess.dir, "seg*.ts"))
		ahead := len(segments) - asked
		sess.mu.Lock()
		if ahead > hlsAhead && !sess.paused {
			sess.paused = pauseProcess(sess.cmd.Process, true)
		} else if ahead < hlsResume && sess.paused {
			pauseProcess(sess.cmd.Process, false)
			sess.paused = false
		}
		sess.mu.Unlock()
	}
}

func (m *hlsManager) stop(sess *hlsSession) {
	sess.mu.Lock()
	if sess.stopped {
		sess.mu.Unlock()
		return
	}
	sess.stopped = true
	sess.mu.Unlock()
	m.mu.Lock()
	delete(m.sessions, sess.id)
	m.mu.Unlock()
	sess.cmd.Process.Kill() // ends a paused process too
	<-sess.exited
	os.RemoveAll(sess.dir)
	<-m.s.transcodes
}

func (m *hlsManager) stopAll() {
	m.mu.Lock()
	var all []*hlsSession
	for _, sess := range m.sessions {
		all = append(all, sess)
	}
	m.mu.Unlock()
	for _, sess := range all {
		m.stop(sess)
	}
}

// api serves /api/hls/...:
//
//	POST   /api/hls/start/{video}       {start, audio} -> {url}
//	GET    /api/hls/s/{session}/index.m3u8, seg00000.ts
//	DELETE /api/hls/s/{session}
func (m *hlsManager) api(w http.ResponseWriter, r *http.Request, u *User, parts []string) {
	if len(parts) == 2 && parts[0] == "start" && r.Method == http.MethodPost {
		cat, err := m.s.lib.Catalog()
		if err != nil {
			apiError(w, http.StatusInternalServerError, err)
			return
		}
		it := cat.items[parts[1]]
		if it == nil {
			apiError(w, http.StatusNotFound, errNotFound)
			return
		}
		var req struct {
			Start float64 `json:"start"`
			Audio int     `json:"audio"`
		}
		readJSON(r, &req)
		sess, err := m.start(it, u, req.Start, req.Audio)
		if err != nil {
			status := http.StatusInternalServerError
			if err == errBusyConverting {
				status = http.StatusServiceUnavailable
			}
			apiError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": sess.id, "url": "/api/hls/s/" + sess.id + "/index.m3u8"})
		return
	}
	if len(parts) < 2 || parts[0] != "s" {
		apiError(w, http.StatusNotFound, errNotFound)
		return
	}
	m.mu.Lock()
	sess := m.sessions[parts[1]]
	m.mu.Unlock()
	if sess == nil || sess.user != u.ID {
		apiError(w, http.StatusNotFound, errors.New("this playback session has ended"))
		return
	}
	if r.Method == http.MethodDelete {
		m.stop(sess)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	name := ""
	if len(parts) > 2 {
		name = filepath.Base(parts[2])
	}
	sess.mu.Lock()
	sess.last = time.Now()
	sess.mu.Unlock()
	switch {
	case name == "index.m3u8":
		// ffmpeg writes the playlist once the first segment is complete;
		// the first answer waits for as many segments as the session needs.
		path := filepath.Join(sess.dir, name)
		ready := func() bool {
			if !exists(path) {
				return false
			}
			sess.mu.Lock()
			served := sess.lastSeg > 0 || sess.paused
			sess.mu.Unlock()
			if served {
				return true
			}
			segments, _ := filepath.Glob(filepath.Join(sess.dir, "seg*.ts"))
			select {
			case <-sess.exited:
				return true // a short video: this is all there is
			default:
				return len(segments) >= sess.firstSegments
			}
		}
		for deadline := time.Now().Add(30 * time.Second); !ready(); time.Sleep(100 * time.Millisecond) {
			select {
			case <-sess.exited:
				if !exists(path) {
					m.s.log("ffmpeg: %s", lastLine(sess.stderr.String()))
					apiError(w, http.StatusInternalServerError, errors.New("the conversion failed"))
					return
				}
			case <-r.Context().Done():
				return
			default:
			}
			if time.Now().After(deadline) {
				apiError(w, http.StatusGatewayTimeout, errors.New("the conversion is too slow to start"))
				return
			}
		}
		playlist, err := os.ReadFile(path)
		if err != nil {
			apiError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(reTargetDuration.ReplaceAll(playlist, []byte(fmt.Sprintf("#EXT-X-TARGETDURATION:%d", sess.target))))
	case strings.HasPrefix(name, "seg") && strings.HasSuffix(name, ".ts"):
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "seg"), ".ts"))
		sess.mu.Lock()
		sess.lastSeg = max(sess.lastSeg, n)
		sess.mu.Unlock()
		w.Header().Set("Content-Type", "video/mp2t")
		http.ServeFile(w, r, filepath.Join(sess.dir, name))
	default:
		apiError(w, http.StatusNotFound, errNotFound)
	}
}

// convertArgs are the ffmpeg options that turn any video into what
// browsers play: H.264 and stereo AAC. A video track that already is H.264
// is copied, which costs almost nothing. For HLS a re-encoded track gets a
// key frame at every segment boundary.
var reTargetDuration = regexp.MustCompile(`#EXT-X-TARGETDURATION:\d+`)

// convertArgs returns the options and whether the video track is copied.
func (s *Server) convertArgs(it *CatItem, audio int, hls bool) (args []string, copied bool) {
	info := s.lib.Probe(it)
	args = []string{"-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d?", max(audio, 0)), "-sn", "-dn", "-map_chapters", "-1"}
	if v := info.stream("video"); v != nil && v.Codec == "h264" && !strings.Contains(v.Profile, "10") {
		args, copied = append(args, "-c:v", "copy"), true
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
			"-vf", "scale='min(1920,iw)':-2")
		if hls {
			args = append(args, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", hlsSegmentSeconds))
		}
	}
	return append(args, "-c:a", "aac", "-ac", "2", "-b:a", "192k"), copied
}
