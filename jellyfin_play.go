package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Playback for the Jellyfin apps, as Jellyfin does it:
//
//   - PlaybackInfo is asked with the app's login and its device profile —
//     the containers and codecs its player takes. A file the profile takes
//     is offered as it is; any other is offered converted, as HLS.
//   - The answer carries a play session. Apps then fetch the video without
//     their login (Swiftfin hands the address to the system player or to
//     VLC, which add nothing to it): the play session in the address is
//     what lets them in, for that video only.

// jfPlaySessionLife is how long a play session opens its video: long
// enough for a film paused overnight.
const jfPlaySessionLife = 24 * time.Hour

type jfPlaySession struct {
	user *User
	item string
	at   time.Time
	hls  string // the conversion started for it, if any
}

type jfPlaySessions struct {
	mu   sync.Mutex
	byID map[string]*jfPlaySession
}

// issue starts a play session of a user for a video.
func (p *jfPlaySessions) issue(u *User, item string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byID == nil {
		p.byID = map[string]*jfPlaySession{}
	}
	for id, ps := range p.byID {
		if time.Since(ps.at) > jfPlaySessionLife {
			delete(p.byID, id)
		}
	}
	id := randomHex(16)
	p.byID[id] = &jfPlaySession{user: u, item: item, at: time.Now()}
	return id
}

// find returns a live play session for the video, and keeps it alive.
func (p *jfPlaySessions) find(id, item string) *jfPlaySession {
	p.mu.Lock()
	defer p.mu.Unlock()
	ps := p.byID[strings.ToLower(id)]
	if ps == nil || ps.item != item || time.Since(ps.at) > jfPlaySessionLife {
		return nil
	}
	ps.at = time.Now()
	return ps
}

// jfDeviceProfile is the part of an app's device profile that decides
// whether a file can be played as it is.
type jfDeviceProfile struct {
	DirectPlayProfiles []struct {
		Type, Container, VideoCodec, AudioCodec string
	}
	// How the player wants each subtitle format: Embed (it draws them from
	// the file), External (as a file of their own), Encode (burned in).
	SubtitleProfiles []struct {
		Format, Method string
	}
}

// drawsItself tells whether the player shows a picture subtitle format
// from the file; otherwise it has to be burned in.
func (p *jfDeviceProfile) drawsItself(format string) bool {
	for _, sp := range p.SubtitleProfiles {
		if strings.EqualFold(sp.Format, format) && strings.EqualFold(sp.Method, "Embed") {
			return true
		}
	}
	return false
}

// subtitleOrdinal is a subtitle track's number among the file's subtitle
// tracks (ffmpeg's 0:s:N), and whether it is pictures; -1 if no such track.
func subtitleOrdinal(info probeInfo, index int) (int, bool) {
	n := 0
	for _, st := range info.Streams {
		if st.Type != "subtitle" {
			continue
		}
		if st.Index == index {
			_, image := imageSubtitles[st.Codec]
			return n, image
		}
		n++
	}
	return -1, false
}

// jfContainers are the names a file's container goes by in profiles.
var jfContainers = map[string][]string{
	"mkv": {"mkv", "matroska"}, "webm": {"webm", "mkv", "matroska"},
	"mp4": {"mp4", "m4v"}, "m4v": {"m4v", "mp4"}, "mov": {"mov", "mp4"},
	"ts": {"ts", "mpegts"}, "m2ts": {"m2ts", "mpegts", "ts"},
	"avi": {"avi"}, "mpg": {"mpeg", "mpg"}, "mpeg": {"mpeg", "mpg"}, "wmv": {"wmv", "asf"},
}

// listed tells whether a profile's comma-separated list names one of the
// values; an empty list allows anything.
func listed(list string, values ...string) bool {
	if strings.TrimSpace(list) == "" {
		return true
	}
	for _, item := range strings.Split(strings.ToLower(list), ",") {
		for _, v := range values {
			if strings.TrimSpace(item) == v {
				return true
			}
		}
	}
	return false
}

// takes tells whether the profile plays the file as it is: its container,
// its video codec and the codec of the audio track that will play.
func (p *jfDeviceProfile) takes(container, video, audio string) bool {
	names := jfContainers[container]
	if names == nil {
		names = []string{container}
	}
	for _, d := range p.DirectPlayProfiles {
		if d.Type != "" && !strings.EqualFold(d.Type, "Video") {
			continue
		}
		if listed(d.Container, names...) && listed(d.VideoCodec, video) && (audio == "" || listed(d.AudioCodec, audio)) {
			return true
		}
	}
	return false
}

// playbackInfo answers how an app is to play a video.
func (c *jfContext) playbackInfo() {
	e, ok := c.entry()
	if !ok || e.item == nil {
		if ok {
			c.notFound()
		}
		return
	}
	it := e.item
	var req struct {
		DeviceProfile       *jfDeviceProfile
		AudioStreamIndex    *int
		SubtitleStreamIndex *int
		StartTimeTicks      int64
		EnableDirectPlay    *bool
		EnableTranscoding   *bool
	}
	if c.r.Method == http.MethodPost {
		c.body(&req)
	}
	if req.AudioStreamIndex == nil && c.q["audiostreamindex"] != "" {
		n := c.q.int("audiostreamindex")
		req.AudioStreamIndex = &n
	}
	if req.SubtitleStreamIndex == nil && c.q["subtitlestreamindex"] != "" {
		n := c.q.int("subtitlestreamindex")
		req.SubtitleStreamIndex = &n
	}
	info := c.s.lib.ProbeNow(it) // a file just downloaded may not be measured yet
	play := c.s.plays.issue(c.u, it.ID)
	source := c.mediaSource(it)

	video, audio := "", ""
	if v := info.stream("video"); v != nil {
		video = v.Codec
	}
	audioIndex := -1
	if req.AudioStreamIndex != nil {
		audioIndex = *req.AudioStreamIndex
	} else if i, ok := source["DefaultAudioStreamIndex"].(int); ok {
		audioIndex = i
	}
	for _, st := range info.Streams {
		if st.Type == "audio" && (audio == "" || st.Index == audioIndex) {
			audio = st.Codec
		}
	}
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(it.Path)), ".")
	direct := req.DeviceProfile == nil || req.DeviceProfile.takes(container, video, audio)
	if req.EnableDirectPlay != nil && !*req.EnableDirectPlay {
		direct = false
	}
	// Picture subtitles the player cannot draw are burned into a converted
	// stream.
	burn, subtitle := -1, -1
	if req.SubtitleStreamIndex != nil && *req.SubtitleStreamIndex >= 0 {
		subtitle = *req.SubtitleStreamIndex
		if n, image := subtitleOrdinal(info, subtitle); image && req.DeviceProfile != nil {
			for _, st := range info.Streams {
				if st.Index == subtitle && !req.DeviceProfile.drawsItself(imageSubtitles[st.Codec]) {
					burn, direct = n, false
				}
			}
		}
	}
	canConvert := c.s.ffmpeg != "" && (req.EnableTranscoding == nil || *req.EnableTranscoding)
	if !direct && canConvert {
		// Converted: H.264 (copied when it already is) and AAC, as HLS.
		source["SupportsDirectPlay"], source["SupportsDirectStream"], source["SupportsTranscoding"] = false, false, true
		source["TranscodingSubProtocol"], source["TranscodingContainer"] = "hls", "ts"
		url := fmt.Sprintf("/Videos/%s/master.m3u8?MediaSourceId=%s&PlaySessionId=%s&api_key=%s", it.ID, it.ID, play, c.s.token(c.r))
		if audioIndex >= 0 {
			url += fmt.Sprintf("&AudioStreamIndex=%d", audioIndex)
		}
		if req.StartTimeTicks > 0 {
			url += fmt.Sprintf("&StartTimeTicks=%d", req.StartTimeTicks)
		}
		if burn >= 0 {
			url += fmt.Sprintf("&SubtitleStreamIndex=%d&SubtitleMethod=Encode", subtitle)
		}
		source["TranscodingUrl"] = url
		// The stream has no subtitles of its own: text ones come as files,
		// the burned-in one is in the picture.
		for _, m := range source["MediaStreams"].([]map[string]any) {
			if m["Type"] != "Subtitle" {
				continue
			}
			switch {
			case m["Index"] == subtitle && burn >= 0:
				m["DeliveryMethod"] = "Encode"
			case m["SupportsExternalStream"] == true:
				m["DeliveryMethod"] = "External"
			}
		}
	}
	if c.s.debug != nil {
		c.s.log("JF playback of %q (%s, %s/%s): %s", it.Title, container, video, audio,
			map[bool]string{true: "as it is", false: "converted (HLS)"}[direct || !canConvert])
	}
	c.json(map[string]any{"MediaSources": []any{source}, "PlaySessionId": play})
}

// playUser is who may fetch a video by the play session in its address.
func (c *jfContext) playUser(item string) *User {
	if c.u != nil {
		return c.u
	}
	if ps := c.s.plays.find(c.q["playsessionid"], item); ps != nil {
		return ps.user
	}
	return nil
}

// master starts the conversion of a video for an app and answers with a
// playlist pointing at it.
func (c *jfContext) master() {
	e, ok := c.entry()
	if !ok || e.item == nil {
		if ok {
			c.notFound()
		}
		return
	}
	it := e.item
	u := c.playUser(it.ID)
	if u == nil {
		http.Error(c.w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	// The audio track is named by its index among all tracks; ffmpeg counts
	// the audio tracks alone.
	audio := 0
	if c.q["audiostreamindex"] != "" {
		want := c.q.int("audiostreamindex")
		for _, st := range c.s.lib.Probe(it).Streams {
			if st.Type == "audio" && st.Index < want {
				audio++
			}
		}
	}
	from := time.Duration(c.q.int64("starttimeticks") * 100).Seconds()
	burn := -1 // picture subtitles burned in, as PlaybackInfo decided
	if c.q["subtitlestreamindex"] != "" {
		if n, image := subtitleOrdinal(c.s.lib.ProbeNow(it), c.q.int("subtitlestreamindex")); image {
			burn = n
		}
	}
	ps := c.s.plays.find(c.q["playsessionid"], it.ID)
	// Asked again for the same playback: the same conversion.
	id, copied := "", false
	if ps != nil && ps.hls != "" {
		if v := c.s.hls.vod(ps.hls); v != nil {
			id, copied = v.id, v.copied
		} else if sess := c.s.hls.session(ps.hls); sess != nil {
			id, copied = sess.id, sess.target == hlsCopyTarget
		}
	}
	if id == "" {
		// The whole film, so the player can seek anywhere; a file whose key
		// frames are not known gets the playlist that grows instead.
		v, err := c.s.hls.startVOD(it, u, from, audio, burn)
		if err == nil {
			id, copied = v.id, v.copied
		} else if err == errBusyConverting {
			http.Error(c.w, err.Error(), http.StatusServiceUnavailable)
			return
		} else {
			if c.s.debug != nil {
				c.s.log("JF whole-film conversion of %q not possible (%v): the growing playlist instead", it.Title, err)
			}
			sess, err := c.s.hls.start(it, u, from, audio, burn)
			if err != nil {
				status := http.StatusInternalServerError
				if err == errBusyConverting {
					status = http.StatusServiceUnavailable
				}
				http.Error(c.w, err.Error(), status)
				return
			}
			id, copied = sess.id, sess.target == hlsCopyTarget
		}
		if ps != nil {
			c.s.plays.mu.Lock()
			ps.hls = id
			c.s.plays.mu.Unlock()
		}
	}
	bandwidth := 8_000_000
	if info := c.s.lib.Probe(it); info.Bitrate > 0 && copied {
		bandwidth = int(info.Bitrate) + 200_000 // the video is copied
	}
	resolution := ""
	if v := c.s.lib.Probe(it).stream("video"); v != nil && v.Width > 0 {
		resolution = fmt.Sprintf(",RESOLUTION=%dx%d", v.Width, v.Height)
	}
	c.w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	c.w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(c.w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=%d%s\nhls/%s/index.m3u8\n", bandwidth, resolution, id)
}

// hlsPart serves the playlist and the segments of a conversion; the long
// random session in the address is what lets the player in.
func (c *jfContext) hlsPart() {
	if v := c.s.hls.vod(c.arg["session"]); v != nil {
		v.serve(c.w, c.r, filepath.Base(c.arg["file"]))
		return
	}
	sess := c.s.hls.session(c.arg["session"])
	if sess == nil {
		http.Error(c.w, "this playback has ended", http.StatusNotFound)
		return
	}
	c.s.hls.serve(c.w, c.r, sess, filepath.Base(c.arg["file"]))
}

// stopEncodings ends the conversion of a play session (the app stopped).
func (c *jfContext) stopEncodings() {
	c.s.plays.mu.Lock()
	ps := c.s.plays.byID[strings.ToLower(c.q["playsessionid"])]
	id := ""
	if ps != nil && ps.user.ID == c.u.ID {
		id, ps.hls = ps.hls, ""
	}
	c.s.plays.mu.Unlock()
	if v := c.s.hls.vod(id); v != nil {
		v.stop()
	} else if sess := c.s.hls.session(id); sess != nil {
		c.s.hls.stop(sess)
	}
	c.noContent()
}

func (q jfQuery) int64(key string) int64 { n, _ := strconv.ParseInt(q[key], 10, 64); return n }
