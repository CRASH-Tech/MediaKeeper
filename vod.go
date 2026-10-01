package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A whole-film HLS conversion, for the players that only seek in a playlist
// that lists the whole film (the Apple TV's own player: with a playlist that
// grows while ffmpeg works, it shows a live broadcast and no seek bar).
//
// The playlist lists every segment, with its exact length, from the start.
// The segments are made on demand: ffmpeg runs from where the player asks,
// is held back when it gets far ahead, and is started anew from another
// segment when the player jumps there. Every run cuts at the same times, so
// segments made by different runs fit together:
//
//   - a re-encoded video track gets a key frame every hlsSegmentSeconds,
//     and is cut there;
//   - a copied one can only be cut at its own key frames, which Matroska
//     files list in their index; the cuts are the first key frames past
//     every hlsSegmentSeconds (and not followed by another within a
//     second, see run).
//
// ffmpeg keeps the film's own time stamps (-copyts), so a segment says
// where in the film it is whichever run made it.
//
// Files whose key frames are not known (no index) get the growing playlist
// of hls.go instead.

type vodSession struct {
	id, dir, user string
	item          *CatItem
	audio         int
	burn          int       // a picture subtitle track burned in, or -1
	hw            string    // the graphics card of the running ffmpeg, "" for the processor
	cuts          []float64 // where each segment begins; the last one ends at duration
	duration      float64
	copied        bool
	m             *hlsManager

	mu      sync.Mutex
	cmd     *exec.Cmd
	exited  chan struct{}
	stderr  *bytes.Buffer
	runFrom int // the first segment of the running ffmpeg
	done    map[int]bool
	listed  int64 // how much of the run's segment list has been read
	last    time.Time
	lastSeg int
	paused  bool
	stopped bool
}

// vodCuts are the segment start times of a video: for a copied track its
// key frames at least hlsSegmentSeconds apart, else every hlsSegmentSeconds.
// A key frame closely followed by another is passed over: ffmpeg starts a
// run there only with a margin (see run), which must not reach the next.
func vodCuts(keyframes []float64, duration float64) []float64 {
	cuts := []float64{0}
	if keyframes == nil {
		for t := float64(hlsSegmentSeconds); t < duration-1; t += hlsSegmentSeconds {
			cuts = append(cuts, t)
		}
		return cuts
	}
	for i, k := range keyframes {
		next := duration
		if i+1 < len(keyframes) {
			next = keyframes[i+1]
		}
		if k-cuts[len(cuts)-1] >= hlsSegmentSeconds && k < duration-1 && next-k >= vodSeekMargin*2 {
			cuts = append(cuts, k)
		}
	}
	return cuts
}

// vodSeekMargin: ffmpeg, seeking a copied track to a moment, starts at the
// key frame before it only when the moment is a little past that key frame;
// asked for the key frame itself, it starts at the one before.
const vodSeekMargin = 0.5

// startVOD prepares a whole-film conversion of a video from a moment, or
// says why it cannot (the caller falls back to the growing playlist).
func (m *hlsManager) startVOD(it *CatItem, user *User, from float64, audio, burn int) (*vodSession, error) {
	s := m.s
	if s.ffmpeg == "" {
		return nil, errors.New("ffmpeg is not installed on the server")
	}
	info := s.lib.ProbeNow(it)
	duration := info.Duration.Seconds()
	if duration <= 0 {
		return nil, errors.New("the length of the video is unknown")
	}
	copied := s.convertArgs(it, convertOptions{audio: audio, hls: true, burn: burn}).copied
	var keyframes []float64
	if copied {
		var err error
		if !strings.EqualFold(filepath.Ext(it.Path), ".mkv") && !strings.EqualFold(filepath.Ext(it.Path), ".webm") {
			return nil, errNoCues
		}
		if keyframes, err = mkvKeyframes(it.Path); err != nil {
			return nil, err
		}
	}
	// A user watches one conversion at a time: the earlier ones end.
	m.mu.Lock()
	var oldHLS []*hlsSession
	var oldVOD []*vodSession
	for _, sess := range m.sessions {
		if sess.user == user.ID {
			oldHLS = append(oldHLS, sess)
		}
	}
	for _, v := range m.vods {
		if v.user == user.ID {
			oldVOD = append(oldVOD, v)
		}
	}
	m.mu.Unlock()
	for _, sess := range oldHLS {
		m.stop(sess)
	}
	for _, v := range oldVOD {
		v.stop()
	}
	if !s.takeSlot(context.Background()) {
		return nil, errBusyConverting
	}
	dir, err := os.MkdirTemp("", "mediakeeper-vod-")
	if err != nil {
		<-s.transcodes
		return nil, err
	}
	v := &vodSession{id: randomHex(12), dir: dir, user: user.ID, item: it, audio: audio, burn: burn, duration: duration,
		cuts: vodCuts(keyframes, duration), copied: copied, m: m, done: map[int]bool{}, last: time.Now()}
	m.mu.Lock()
	m.vods[v.id] = v
	m.mu.Unlock()
	v.mu.Lock()
	err = v.run(v.segmentAt(from))
	v.mu.Unlock()
	if err != nil {
		v.stop()
		return nil, err
	}
	go v.watch()
	s.log("▶ %s  %s  [converted for HLS, whole film, from %s]", user.Name, it.Title, time.Duration(from)*time.Second)
	return v, nil
}

// segmentAt is the segment that holds a moment of the video.
func (v *vodSession) segmentAt(t float64) int {
	n := 0
	for i, c := range v.cuts {
		if c <= t {
			n = i
		}
	}
	return n
}

func (v *vodSession) end(n int) float64 {
	if n+1 < len(v.cuts) {
		return v.cuts[n+1]
	}
	return v.duration
}

// run starts ffmpeg at segment n, ending the run before. Locked by the caller.
func (v *vodSession) run(n int) error {
	v.kill()
	s := v.m.s
	from := v.cuts[n]
	// A re-encoded track gets a key frame every hlsSegmentSeconds counted
	// from where the run starts (ffmpeg's t is the run's own clock, even
	// with -copyts); runs start on that grid, so all of them agree.
	conv := s.convertArgs(v.item, convertOptions{audio: v.audio, hls: true, burn: v.burn})
	args := append([]string{"-nostdin", "-v", "error", "-copyts"}, conv.input...)
	if from > 0 {
		start := from // a re-encoded track starts at the very moment
		if v.copied {
			start += vodSeekMargin // a copied one at the key frame before: the cut
		}
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args, "-i", v.item.Path)
	args = append(args, conv.output...)
	var times []string
	for _, c := range v.cuts[n+1:] {
		times = append(times, strconv.FormatFloat(c, 'f', 3, 64))
	}
	list := filepath.Join(v.dir, fmt.Sprintf("run%05d.csv", n))
	args = append(args, "-muxdelay", "0", "-muxpreload", "0",
		"-f", "segment", "-segment_format", "mpegts", "-segment_start_number", strconv.Itoa(n),
		"-segment_list", list, "-segment_list_type", "csv", "-segment_list_flags", "+live")
	if len(times) > 0 {
		args = append(args, "-segment_times", strings.Join(times, ","))
	} else {
		args = append(args, "-segment_time", "100000")
	}
	args = append(args, filepath.Join(v.dir, "seg%05d.ts"))
	cmd := exec.Command(s.ffmpeg, args...)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	v.cmd, v.exited, v.stderr, v.runFrom, v.listed, v.paused, v.hw = cmd, exited, stderr, n, 0, false, conv.hw
	return nil
}

// kill ends the running ffmpeg, if any. Locked by the caller.
func (v *vodSession) kill() {
	if v.cmd == nil {
		return
	}
	v.cmd.Process.Kill() // ends a paused process too
	<-v.exited
	v.cmd = nil
}

// refresh reads which segments the running ffmpeg has finished: its list
// gets a line for each. Locked by the caller.
func (v *vodSession) refresh() {
	if v.cmd == nil {
		return
	}
	f, err := os.Open(filepath.Join(v.dir, fmt.Sprintf("run%05d.csv", v.runFrom)))
	if err != nil {
		return
	}
	defer f.Close()
	f.Seek(v.listed, 0)
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			break // a line not yet complete is read the next time
		}
		v.listed += int64(len(line))
		name, _, _ := strings.Cut(strings.TrimSpace(line), ",")
		if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "seg"), ".ts")); err == nil {
			v.done[n] = true
		}
	}
}

// produced is the highest segment of the current run that is finished.
func (v *vodSession) produced() int {
	n := v.runFrom - 1
	for v.done[n+1] {
		n++
	}
	return n
}

// segment waits until segment n is there, starting ffmpeg at it when the
// running one would not get there soon.
func (v *vodSession) segment(r *http.Request, n int) (string, error) {
	path := filepath.Join(v.dir, fmt.Sprintf("seg%05d.ts", n))
	deadline := time.Now().Add(60 * time.Second)
	for {
		v.mu.Lock()
		if v.stopped {
			v.mu.Unlock()
			return "", errors.New("this playback has ended")
		}
		v.last = time.Now()
		v.refresh()
		if v.done[n] {
			v.lastSeg = n
			v.mu.Unlock()
			return path, nil
		}
		ahead := v.cmd != nil && n >= v.runFrom && n <= v.produced()+4
		if v.cmd != nil && ahead {
			select {
			case <-v.exited: // finished or failed before getting there
				ahead = false
			default:
			}
		}
		if !ahead {
			if err := v.run(n); err != nil {
				v.mu.Unlock()
				return "", err
			}
		}
		if v.paused { // the player came back for it
			pauseProcess(v.cmd.Process, false)
			v.paused = false
		}
		exited, stderr := v.exited, v.stderr
		v.mu.Unlock()
		select {
		case <-r.Context().Done():
			return "", r.Context().Err()
		case <-exited:
			v.mu.Lock()
			v.refresh()
			ok, hw := v.done[n], v.hw
			if !ok && hw != "" && v.cmd != nil {
				// The card could not do this file: once more on the processor.
				v.m.s.hwGaveUp(v.item.Path, lastLine(stderr.String()))
				err := v.run(n)
				v.mu.Unlock()
				if err != nil {
					return "", err
				}
				continue
			}
			v.mu.Unlock()
			if !ok {
				return "", fmt.Errorf("the conversion failed: %s", lastLine(stderr.String()))
			}
		case <-time.After(150 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return "", errors.New("the conversion is too slow")
		}
	}
}

// watch holds ffmpeg back when it gets far ahead of the player, and ends the
// session nobody asks anything of.
func (v *vodSession) watch() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		v.mu.Lock()
		if v.stopped {
			v.mu.Unlock()
			return
		}
		if time.Since(v.last) > hlsIdle {
			v.mu.Unlock()
			v.stop()
			return
		}
		v.refresh()
		if v.cmd != nil {
			ahead := v.produced() - v.lastSeg
			if ahead > hlsAhead && !v.paused {
				v.paused = pauseProcess(v.cmd.Process, true)
			} else if ahead < hlsResume && v.paused {
				pauseProcess(v.cmd.Process, false)
				v.paused = false
			}
		}
		v.mu.Unlock()
	}
}

func (v *vodSession) stop() {
	v.mu.Lock()
	if v.stopped {
		v.mu.Unlock()
		return
	}
	v.stopped = true
	v.kill()
	v.mu.Unlock()
	v.m.mu.Lock()
	delete(v.m.vods, v.id)
	v.m.mu.Unlock()
	os.RemoveAll(v.dir)
	<-v.m.s.transcodes
}

// playlist lists the whole film.
func (v *vodSession) playlist() []byte {
	longest := 0.0
	for i := range v.cuts {
		longest = math.Max(longest, v.end(i)-v.cuts[i])
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n",
		int(math.Ceil(longest)))
	for i := range v.cuts {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg%05d.ts\n", v.end(i)-v.cuts[i], i)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.Bytes()
}

// serve answers for the playlist or a segment.
func (v *vodSession) serve(w http.ResponseWriter, r *http.Request, name string) {
	switch {
	case name == "index.m3u8":
		v.mu.Lock()
		v.last = time.Now()
		v.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(v.playlist())
	case strings.HasPrefix(name, "seg") && strings.HasSuffix(name, ".ts"):
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "seg"), ".ts"))
		if err != nil || n < 0 || n >= len(v.cuts) {
			http.NotFound(w, r)
			return
		}
		path, err := v.segment(r, n)
		if err != nil {
			if r.Context().Err() == nil {
				v.m.s.log("HLS: segment %d of %s: %v", n, v.item.Title, err)
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
			}
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		http.ServeFile(w, r, path)
	default:
		http.NotFound(w, r)
	}
}

func (m *hlsManager) vod(id string) *vodSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.vods[id]
}
