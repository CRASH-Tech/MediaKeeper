package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Text subtitles inside a file and next to it are listed with their
// languages, taken out once and given as WebVTT; Jellyfin apps get them as
// files of their own.
func TestSubtitles(t *testing.T) {
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg is not installed")
	}
	s, srv := serverFixture(t) // (it empties PATH)
	s.ffmpeg, s.prober.tool = ffmpeg, ffprobe
	dir := filepath.Join(s.firstRoot(), "Clip (2020)")
	os.MkdirAll(dir, 0o755)
	srt := func(name, text string) string {
		path := filepath.Join(t.TempDir(), name)
		os.WriteFile(path, []byte("1\n00:00:02,000 --> 00:00:04,500\n"+text+"\n"), 0o644)
		return path
	}
	film := filepath.Join(dir, "Clip (2020).mkv")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=6:s=320x240:r=25",
		"-i", srt("en.srt", "Hello"), "-i", srt("ru.srt", "Привет"),
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-c:s", "srt",
		"-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=rus", film).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	os.WriteFile(filepath.Join(dir, "Clip (2020).uk.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nПривіт\n"), 0o644)
	s.refresh()
	cat, _ := s.lib.Catalog()
	var it *CatItem
	for _, m := range cat.Movies {
		if m.Title == "Clip" {
			it = m
		}
	}
	s.lib.ProbeNow(it)

	kid := newBrowser(t, srv, "kid")
	var item struct{ SubtitleTracks []subtitleTrack }
	kid.json("/api/item/"+it.ID, &item)
	var labels []string
	for _, tr := range item.SubtitleTracks {
		labels = append(labels, tr.Key+" "+tr.Label)
	}
	if got := strings.Join(labels, " | "); got != "e1 English | e2 Russian | x0 Ukrainian (file)" {
		t.Fatalf("tracks: %s", got)
	}

	// Taken out of the file, as WebVTT, shifted for a stream that starts later.
	status, vtt := kid.get("/api/subs/" + it.ID + "/e2.vtt?offset=1")
	if status != 200 || !strings.HasPrefix(vtt, "WEBVTT") || !strings.Contains(vtt, "Привет") || !strings.Contains(vtt, "00:00:01.000 --> 00:00:03.500") {
		t.Fatalf("embedded track: %d\n%s", status, vtt)
	}
	cached, _ := filepath.Glob(filepath.Join(s.cacheRoot(), "subtitles", "*", "*.srt"))
	if len(cached) != 2 {
		t.Errorf("both text tracks are taken out at once, into the cache: %v", cached)
	}
	if _, vtt := kid.get("/api/subs/" + it.ID + "/e1.vtt"); !strings.Contains(vtt, "Hello") {
		t.Errorf("the other track:\n%s", vtt)
	}
	if _, vtt := kid.get("/api/subs/" + it.ID + "/x0.vtt"); !strings.Contains(vtt, "Привіт") {
		t.Errorf("the file next to it:\n%s", vtt)
	}
	if status, _ := kid.get("/api/subs/" + it.ID + "/e0.vtt"); status != http.StatusNotFound { // the video track
		t.Errorf("not a subtitle track: %d", status)
	}

	// Jellyfin: a track of the file by its index, the file next to it after them.
	b := newBrowser(t, srv, "")
	var login struct{ AccessToken string }
	_, body := b.do("POST", "/Users/AuthenticateByName", map[string]string{"Username": "kid", "Pw": "kid-password"},
		map[string]string{"Authorization": `MediaBrowser Client="Swiftfin", Device="TV", DeviceId="tv", Version="1"`})
	mustUnmarshal(t, body, &login)
	for path, want := range map[string]string{"/Subtitles/2/0/Stream.srt": "Привет", "/Subtitles/3/0/Stream.vtt": "Привіт"} {
		if _, text := b.get("/Videos/" + it.ID + "/" + it.ID + path + "?api_key=" + login.AccessToken); !strings.Contains(text, want) {
			t.Errorf("%s: %q", path, text)
		}
	}
}

// Picture subtitles (Blu-ray) are burned in when the player cannot draw
// them, and left to the player when it can.
func TestPictureSubtitles(t *testing.T) {
	s, srv := serverFixture(t)
	s.ffmpeg = "ffmpeg" // only the decision is tested: nothing is run
	cat, _ := s.lib.Catalog()
	var film *CatItem
	for _, m := range cat.Movies {
		if strings.HasPrefix(m.Title, "Iron Man") {
			film = m
		}
	}
	// What ffprobe would say of a Blu-ray rip: H.264, AC-3, two picture tracks.
	s.prober.known[probeJob{film.Path, film.Size, film.ModTime}.key()] = probeInfo{Duration: 600e9, Streams: []streamInfo{
		{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080},
		{Index: 1, Type: "audio", Codec: "ac3", Channels: 6, Default: true},
		{Index: 2, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "eng"},
		{Index: 3, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "fre"},
	}}
	tracks := s.subtitleTracks(film)
	if len(tracks) != 3 || !tracks[1].Image || tracks[1].Ordinal != 1 || tracks[1].Label != "French" || tracks[2].Label != "English (file)" { // and the .srt next to it
		t.Fatalf("tracks: %+v", tracks)
	}
	conv := s.convertArgs(film, convertOptions{hls: true, burn: 1})
	joined := strings.Join(conv.output, " ")
	if conv.copied || !strings.Contains(joined, "[0:s:1]overlay") || !strings.Contains(joined, "libx264") {
		t.Errorf("burning in: %s", joined)
	}

	b := newBrowser(t, srv, "")
	var login struct{ AccessToken string }
	app := map[string]string{"Authorization": `MediaBrowser Client="Swiftfin", Device="TV", DeviceId="tv", Version="1"`}
	_, body := b.do("POST", "/Users/AuthenticateByName", map[string]string{"Username": "kid", "Pw": "kid-password"}, app)
	mustUnmarshal(t, body, &login)
	app["Authorization"] += `, Token="` + login.AccessToken + `"`
	ask := func(method string) (direct bool, url string) {
		var a struct {
			MediaSources []struct {
				SupportsDirectPlay bool
				TranscodingUrl     string
				MediaStreams       []struct {
					Index          int
					DeliveryMethod string
				}
			}
		}
		profile := map[string]any{
			"DirectPlayProfiles": []map[string]string{{"Type": "Video", "Container": "mkv", "VideoCodec": "h264", "AudioCodec": "ac3"}},
			"SubtitleProfiles":   []map[string]string{{"Format": "pgssub", "Method": method}},
		}
		_, body := b.do("POST", "/Items/"+film.ID+"/PlaybackInfo", map[string]any{"DeviceProfile": profile, "SubtitleStreamIndex": 3}, app)
		mustUnmarshal(t, body, &a)
		src := a.MediaSources[0]
		for _, st := range src.MediaStreams {
			if st.Index == 3 && method == "Encode" && st.DeliveryMethod != "Encode" {
				t.Errorf("the burned-in track is delivered by %q", st.DeliveryMethod)
			}
		}
		return src.SupportsDirectPlay, src.TranscodingUrl
	}
	if direct, url := ask("Embed"); !direct || url != "" {
		t.Errorf("a player that draws them gets the file: %v %s", direct, url)
	}
	if direct, url := ask("Encode"); direct || !strings.Contains(url, "SubtitleStreamIndex=3") {
		t.Errorf("a player that cannot gets them burned in: %v %s", direct, url)
	}
}
