package main

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A graphics card is used only when the settings ask for one and a test
// conversion works.
func TestDetectHW(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	var said []string
	log := func(f string, a ...any) { said = append(said, f) }
	for _, choice := range []string{"", "none", "off"} {
		if h := detectHW(ffmpeg, choice, log); h != nil {
			t.Errorf("%q: %+v", choice, h)
		}
	}
	if h := detectHW(ffmpeg, "vaapi:/dev/null", log); h != nil { // not a card
		t.Errorf("a device that is no card: %+v", h)
	}
	if h := detectHW(ffmpeg, "teapot", log); h != nil || !strings.Contains(strings.Join(said, "\n"), "unknown method") {
		t.Errorf("an unknown method: %+v %v", h, said)
	}
	if h := detectHW("", "auto", log); h != nil {
		t.Errorf("without ffmpeg: %+v", h)
	}
}

// Each card's way of converting, and the processor taking over a file the
// card failed on.
func TestHWConversion(t *testing.T) {
	p := newProber()
	s := &Server{lib: NewLibrary([]Root{{Path: t.TempDir()}}, p), prober: p, log: func(string, ...any) {}}
	it := &CatItem{Path: "/films/x.mkv", Size: 1}
	s.prober.known[probeJob{it.Path, it.Size, it.ModTime}.key()] = probeInfo{Streams: []streamInfo{
		{Index: 0, Type: "video", Codec: "hevc", Profile: "Main 10", FieldOrder: "progressive"}}}
	for method, want := range map[string][]string{
		"vaapi": {"-hwaccel vaapi", "scale_vaapi", "h264_vaapi", "-force_key_frames"},
		"qsv":   {"-hwaccel qsv", "vpp_qsv", "h264_qsv"},
		"nvenc": {"-hwaccel cuda", "scale_cuda", "h264_nvenc", "-forced-idr 1"},
	} {
		s.live.hw = &hwAccel{method: method, device: "/dev/dri/renderD128"}
		conv := s.convertArgs(it, convertOptions{hls: true, burn: -1})
		joined := strings.Join(append(conv.input, conv.output...), " ")
		for _, w := range want {
			if !strings.Contains(joined, w) {
				t.Errorf("%s lacks %q: %s", method, w, joined)
			}
		}
		if conv.hw != method || strings.Contains(joined, "libx264") {
			t.Errorf("%s: %s", method, joined)
		}
		// Burned-in subtitles: drawn on the processor, then onto the card.
		conv = s.convertArgs(it, convertOptions{burn: 0})
		if joined := strings.Join(append(conv.input, conv.output...), " "); !strings.Contains(joined, "[0:s:0]overlay") || strings.Contains(joined, "-hwaccel ") {
			t.Errorf("%s with subtitles: %s", method, joined)
		}
	}
	s.hwGaveUp(it.Path, "test")
	if conv := s.convertArgs(it, convertOptions{burn: -1}); conv.hw != "" || !strings.Contains(strings.Join(conv.output, " "), "libx264") {
		t.Errorf("after the card failed: %+v", conv)
	}
}

// A whole-film conversion whose card fails goes on, on the processor.
func TestHWFallback(t *testing.T) {
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg is not installed")
	}
	s, srv := serverFixture(t)
	s.ffmpeg, s.prober.tool = ffmpeg, ffprobe
	s.live.hw = &hwAccel{method: "vaapi", device: "/dev/null"} // a card that fails at once
	avi := filepath.Join(s.firstRoot(), "Old (1999)", "Old (1999).avi")
	os.MkdirAll(filepath.Dir(avi), 0o755)
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=12:s=320x240:r=25", "-c:v", "mpeg4", avi).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	s.refresh()
	cat, _ := s.lib.Catalog()
	var it *CatItem
	for _, m := range cat.Movies {
		if m.Title == "Old" {
			it = m
		}
	}
	u := s.auth.ByToken("")
	if u == nil {
		u = &User{ID: "tester", Name: "tester"}
	}
	v, err := s.hls.startVOD(it, u, 0, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	_ = srv
	path, err := v.segment(httptest.NewRequest("GET", "/", nil), 1)
	if err != nil || !exists(path) {
		t.Fatalf("segment after the card failed: %v", err)
	}
	if !s.hwFailed(it.Path) || v.hw != "" {
		t.Errorf("the file is not left to the processor: failed %v, hw %q", s.hwFailed(it.Path), v.hw)
	}
}
