package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// streamInfo is one track of a media file.
type streamInfo struct {
	Index    int
	Type     string // video, audio, subtitle
	Codec    string
	Profile  string
	Language string
	Title    string
	Width    int
	Height   int
	Channels int
	Default  bool

	Level         int
	BitRate       int
	BitDepth      int
	Refs          int
	SampleRate    int
	FrameRate     float64
	PixelFormat   string
	FieldOrder    string  // "progressive", or tt/bb/tb/bt for interlaced video
	FieldRate     float64 // r_frame_rate: for interlaced video usually the rate of fields
	AspectRatio   string
	TimeBase      string
	ChannelLayout string
}

// Interlaced reports video made of alternating fields (broadcast, DVD, some
// Blu-rays). Apple devices do not show such H.264 at all, and browsers show
// it combed and jerky, so it is always converted, never copied.
func (s *streamInfo) Interlaced() bool {
	switch s.FieldOrder {
	case "tt", "bb", "tb", "bt":
		return true
	}
	return false
}

// ratio reads ffprobe's "24000/1001".
func ratio(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	n, _ := strconv.ParseFloat(num, 64)
	d, _ := strconv.ParseFloat(den, 64)
	if !ok || d == 0 {
		return n
	}
	return math.Round(n/d*1000) / 1000
}

// probeInfo is what ffprobe measures in a file.
type probeInfo struct {
	Duration      time.Duration
	Bitrate       int
	Width, Height int // of the first video track
	Streams       []streamInfo
}

func (p probeInfo) stream(kind string) *streamInfo {
	for i := range p.Streams {
		if p.Streams[i].Type == kind {
			return &p.Streams[i]
		}
	}
	return nil
}

type probeJob struct {
	path string
	size int64
	mod  time.Time
}

func (j probeJob) key() string {
	return fmt.Sprintf("%s|%d|%d", j.path, j.size, j.mod.UnixNano())
}

// prober measures durations, frame sizes and codecs in the background, so
// that clients can show a progress bar and decide whether they can play a
// file. Without ffprobe it stays empty and the runtime from the .nfo is
// used instead.
type prober struct {
	tool   string
	mu     sync.Mutex
	known  map[string]probeInfo // by path + size + mtime
	queued map[string]bool
	queue  chan probeJob
}

func newProber() *prober {
	p := &prober{known: map[string]probeInfo{}, queued: map[string]bool{}, queue: make(chan probeJob, 8192)}
	p.tool, _ = exec.LookPath("ffprobe")
	if p.tool != "" {
		go p.work()
	}
	return p
}

func (p *prober) get(path string, size int64, mod time.Time) (probeInfo, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	info, ok := p.known[probeJob{path, size, mod}.key()]
	return info, ok
}

// request schedules a file that was not measured yet.
func (p *prober) request(path string, size int64, mod time.Time) {
	if p.tool == "" {
		return
	}
	job := probeJob{path, size, mod}
	p.mu.Lock()
	_, done := p.known[job.key()]
	waiting := p.queued[job.key()]
	if !done && !waiting {
		p.queued[job.key()] = true
	}
	p.mu.Unlock()
	if done || waiting {
		return
	}
	select {
	case p.queue <- job:
	default: // a huge library: the rest is picked up by the next scan
		p.mu.Lock()
		delete(p.queued, job.key())
		p.mu.Unlock()
	}
}

func (p *prober) work() {
	for job := range p.queue {
		info := p.probe(job.path)
		p.mu.Lock()
		p.known[job.key()] = info // an unreadable file is remembered too
		delete(p.queued, job.key())
		p.mu.Unlock()
	}
}

func (p *prober) probe(path string) probeInfo {
	var info probeInfo
	out, err := exec.Command(p.tool, "-v", "error", "-show_entries",
		"format=duration,bit_rate:stream=index,codec_type,codec_name,profile,level,width,height,channels,channel_layout,sample_rate,bit_rate,"+
			"pix_fmt,avg_frame_rate,r_frame_rate,field_order,display_aspect_ratio,bits_per_raw_sample,refs,time_base:stream_tags=language,title:stream_disposition=default",
		"-of", "json", path).Output()
	if err != nil {
		return info
	}
	var res struct {
		Streams []struct {
			Index    int    `json:"index"`
			Type     string `json:"codec_type"`
			Codec    string `json:"codec_name"`
			Profile  string `json:"profile"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Channels int    `json:"channels"`
			Level    int    `json:"level"`
			Refs     int    `json:"refs"`
			Layout   string `json:"channel_layout"`
			Rate     string `json:"sample_rate"`
			BitRate  string `json:"bit_rate"`
			PixFmt   string `json:"pix_fmt"`
			FPS      string `json:"avg_frame_rate"`
			RFPS     string `json:"r_frame_rate"`
			Fields   string `json:"field_order"`
			Aspect   string `json:"display_aspect_ratio"`
			Depth    string `json:"bits_per_raw_sample"`
			TimeBase string `json:"time_base"`
			Tags     struct {
				Language string `json:"language"`
				Title    string `json:"title"`
			} `json:"tags"`
			Disposition struct {
				Default int `json:"default"`
			} `json:"disposition"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			Bitrate  string `json:"bit_rate"`
		} `json:"format"`
	}
	if json.Unmarshal(out, &res) != nil {
		return info
	}
	if d, err := time.ParseDuration(res.Format.Duration + "s"); err == nil {
		info.Duration = d
	}
	info.Bitrate, _ = strconv.Atoi(res.Format.Bitrate)
	for _, s := range res.Streams {
		if s.Type != "video" && s.Type != "audio" && s.Type != "subtitle" {
			continue // attachments, data
		}
		if s.Type == "video" && (s.Codec == "mjpeg" || s.Codec == "png") {
			continue // an embedded cover, not a video track
		}
		info.Streams = append(info.Streams, streamInfo{Index: s.Index, Type: s.Type, Codec: s.Codec, Profile: s.Profile,
			Language: s.Tags.Language, Title: s.Tags.Title, Width: s.Width, Height: s.Height,
			Channels: s.Channels, Default: s.Disposition.Default == 1,
			Level: s.Level, Refs: s.Refs, ChannelLayout: s.Layout, SampleRate: atoi(s.Rate), BitRate: atoi(s.BitRate),
			PixelFormat: s.PixFmt, FrameRate: ratio(s.FPS), FieldOrder: s.Fields, FieldRate: ratio(s.RFPS), AspectRatio: s.Aspect, BitDepth: atoi(s.Depth), TimeBase: s.TimeBase})
	}
	if v := info.stream("video"); v != nil {
		info.Width, info.Height = v.Width, v.Height
	}
	return info
}
