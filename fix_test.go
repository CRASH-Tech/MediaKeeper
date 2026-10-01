package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A title identified wrongly is given another identity from the web
// interface: its files move and are renamed as for a new title, and the old
// description and artwork go away.
func TestFixTitle(t *testing.T) {
	s, srv := serverFixture(t)
	boss, kid := newBrowser(t, srv, "boss"), newBrowser(t, srv, "kid")
	var lib libraryView
	boss.json("/api/library", &lib)
	iron, loose, show := lib.Movies[0].ID, lib.Movies[1].ID, lib.Shows[0].ID

	if status, _ := kid.post("/api/fix/"+iron, resolveRequest{Ref: "tmdb:10"}); status != http.StatusForbidden {
		t.Errorf("a viewer fixes titles: %d, want 403", status)
	}
	if status, _ := kid.get("/api/fix/" + iron + "/search?q=x"); status != http.StatusForbidden {
		t.Errorf("a viewer searches for a fix: %d, want 403", status)
	}

	var candidates []candidate
	boss.json("/api/fix/"+iron+"/search?q=Iron+Man", &candidates)
	if len(candidates) == 0 || candidates[0].Title != "Железный человек" {
		t.Fatalf("candidates: %+v", candidates)
	}

	// A movie in its own folder: the folder is replaced with everything in
	// it, the generated files of the old identity are dropped.
	if status, body := boss.post("/api/fix/"+iron, resolveRequest{Ref: "tmdb:10"}); status != 200 {
		t.Fatalf("fix: %d %s", status, body)
	}
	dir := filepath.Join(s.firstRoot(), moviesFolder, "На линии огня (1993)")
	for name, want := range map[string]string{
		"На линии огня (1993).mkv":    "0123456789",
		"На линии огня (1993).en.srt": "-->",
		"На линии огня (1993).nfo":    `source="tmdb" id="10"`,
	} {
		if data, _ := os.ReadFile(filepath.Join(dir, name)); !strings.Contains(string(data), want) {
			t.Errorf("%s: %q\n  %s", name, data, strings.Join(tree(t, s.firstRoot()), "\n  "))
		}
	}
	for _, gone := range []string{"Iron Man (2008)", moviesFolder + "/На линии огня (1993)/poster.jpg", moviesFolder + "/На линии огня (1993)/Iron Man (2008).nfo"} {
		if exists(filepath.Join(s.firstRoot(), filepath.FromSlash(gone))) {
			t.Errorf("%s is still there", gone)
		}
	}

	// A loose file, by picking a candidate.
	if status, body := boss.post("/api/fix/"+loose, resolveRequest{Source: "tmdb", ID: "777", Kind: kindMovie}); status != 200 {
		t.Fatalf("fix a loose file: %d %s", status, body)
	}
	if !exists(filepath.Join(s.firstRoot(), moviesFolder, "Мятеж (2025)", "Мятеж (2025).avi")) {
		t.Errorf("the loose file was not filed:\n  %s", strings.Join(tree(t, s.firstRoot()), "\n  "))
	}
	// An identity whose name is taken by another file is refused, and
	// nothing moves.
	taken := filepath.Join(s.firstRoot(), moviesFolder, "Мятеж (2025)", "Мятеж (2025).mkv")
	os.WriteFile(taken, []byte("another copy"), 0o644)
	defer os.Remove(taken)
	s.refresh()
	boss.json("/api/library", &lib)
	var line string
	for _, m := range lib.Movies {
		if m.Title == "На линии огня" {
			line = m.ID
		}
	}
	if status, body := boss.post("/api/fix/"+line, resolveRequest{Ref: "tmdb:777"}); status != 400 || !strings.Contains(body, "already in the library") {
		t.Errorf("fix into a taken name: %d %s", status, body)
	}
	if !exists(filepath.Join(dir, "На линии огня (1993).nfo")) {
		t.Errorf("a refused fix removed the description")
	}
	os.Remove(taken)
	s.refresh()

	// A whole series: every episode moves and gets its new name.
	if status, body := boss.post("/api/fix/"+show, resolveRequest{Ref: "tv:314"}); status != 200 {
		t.Fatalf("fix a series: %d %s", status, body)
	}
	ent := filepath.Join(s.firstRoot(), showsFolder, "Звёздный путь - Энтерпрайз (2001)")
	for _, want := range []string{
		"tvshow.nfo", "poster.jpg",
		"Season 01/Звёздный путь - Энтерпрайз S01E01-E02 - Разорванный круг (1) + Разорванный круг (2).mkv",
		"Season 01/Звёздный путь - Энтерпрайз S01E03.mkv", // the catalogue has no such episode: no title
		"Season 02/Звёздный путь - Энтерпрайз S02E01 - Ударная волна - Часть 2.mkv",
	} {
		if !exists(filepath.Join(ent, filepath.FromSlash(want))) {
			t.Errorf("missing %s\n  %s", want, strings.Join(tree(t, s.firstRoot()), "\n  "))
		}
	}
	if exists(filepath.Join(s.firstRoot(), "Star Trek - Enterprise (2001)")) {
		t.Errorf("the old series folder is still there")
	}
	for _, path := range tree(t, s.firstRoot()) {
		if strings.Contains(path, "Broken Bow") || strings.Contains(path, "Shockwave") {
			t.Errorf("a file of the old identity is left: %s", path)
		}
	}

	// The catalogue shows the result at once, and the run can be undone.
	boss.json("/api/library", &lib)
	if len(lib.Shows) != 1 || lib.Shows[0].Title != "Звёздный путь: Энтерпрайз" || len(lib.Movies) != 2 {
		t.Errorf("library after fixes: %+v", lib)
	}
	if len(journals(s.firstRoot())) != 3 {
		t.Errorf("journals: %d, want one per fix", len(journals(s.firstRoot())))
	}
	if status, _ := boss.post("/api/fix/0123456789abcdef0123456789abcdef", resolveRequest{Ref: "tmdb:10"}); status != 404 {
		t.Errorf("unknown title: %d", status)
	}
}

// The library as the web interface shows it carries what titles can be
// grouped by.
func TestLibraryGroups(t *testing.T) {
	s, srv := serverFixture(t)
	nfo := filepath.Join(s.firstRoot(), "Iron Man (2008)", "Iron Man (2008).nfo")
	os.WriteFile(nfo, []byte(nfoHeader+`<movie><title>Iron Man</title><year>2008</year><genre>Action</genre><genre>Sci-Fi</genre>
<country>USA</country><studio>Marvel</studio><director>Jon Favreau</director><credits>Mark Fergus</credits>
<actor><name>Robert Downey Jr.</name><role>Tony Stark</role></actor><actor><name>Jeff Bridges</name></actor></movie>`), 0o644)
	s.refresh()
	var lib struct {
		Movies []struct {
			Title                                          string
			Genres, Countries, Studios, Directors, Writers []string
			Cast                                           []struct{ Name, Role string }
		}
	}
	newBrowser(t, srv, "kid").json("/api/library", &lib)
	m := lib.Movies[0]
	if m.Title != "Iron Man" || strings.Join(m.Genres, ",") != "Action,Sci-Fi" || strings.Join(m.Countries, ",") != "USA" ||
		strings.Join(m.Studios, ",") != "Marvel" || strings.Join(m.Directors, ",") != "Jon Favreau" || strings.Join(m.Writers, ",") != "Mark Fergus" ||
		len(m.Cast) != 2 || m.Cast[0].Name != "Robert Downey Jr." || m.Cast[0].Role != "Tony Stark" {
		t.Errorf("groups: %+v", m)
	}
}

// Converted video for Safari: a session produces an HLS playlist and its
// segments, and is cleaned up when it ends.
func TestHLS(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	s, srv := serverFixture(t)
	s.ffmpeg = ffmpeg
	video := filepath.Join(s.firstRoot(), "Clip (2020).avi")
	gen := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=14:s=160x120:r=10",
		"-f", "lavfi", "-i", "sine=d=14", "-c:v", "mpeg4", "-c:a", "mp3", "-shortest", video)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	s.refresh()
	kid := newBrowser(t, srv, "kid")
	var lib libraryView
	kid.json("/api/library", &lib)
	id := ""
	for _, m := range lib.Movies {
		if m.Title == "Clip" {
			id = m.ID
		}
	}

	var started struct{ ID, URL string }
	if status, body := kid.post("/api/hls/start/"+id, map[string]any{"start": 2, "audio": 0}); status != 200 {
		t.Fatalf("start: %d %s", status, body)
	}
	// A second start by the same user replaces the first session.
	status, body := kid.post("/api/hls/start/"+id, map[string]any{"start": 0})
	if status != 200 {
		t.Fatalf("start: %d %s", status, body)
	}
	mustUnmarshal(t, body, &started)
	s.hls.mu.Lock()
	sessions, dir := len(s.hls.sessions), s.hls.sessions[started.ID].dir
	s.hls.mu.Unlock()
	if sessions != 1 {
		t.Errorf("sessions of one user: %d, want 1", sessions)
	}

	status, playlist := kid.get(started.URL) // waits for the first segment
	if status != 200 || !strings.HasPrefix(playlist, "#EXTM3U") || !strings.Contains(playlist, "seg00000.ts") || !strings.Contains(playlist, "EVENT") ||
		!strings.Contains(playlist, "#EXT-X-TARGETDURATION:6\n") {
		t.Fatalf("playlist: %d\n%s", status, playlist)
	}
	status, segment := kid.get(strings.Replace(started.URL, "index.m3u8", "seg00000.ts", 1))
	if status != 200 || len(segment) < 188 || segment[0] != 0x47 { // MPEG-TS packets start with 0x47
		t.Errorf("segment: %d, %d bytes", status, len(segment))
	}
	// Another user cannot read the session; files outside it are not served.
	if status, _ := newBrowser(t, srv, "boss").get(started.URL); status != 404 {
		t.Errorf("another user's session: %d", status)
	}
	if status, _ := kid.get(strings.Replace(started.URL, "index.m3u8", "..%2F..%2Fetc%2Fpasswd", 1)); status != 404 {
		t.Errorf("path outside the session: %d", status)
	}

	if status, _ := kid.do("DELETE", "/api/hls/s/"+started.ID, nil, nil); status != 200 {
		t.Errorf("end the session: %d", status)
	}
	if exists(dir) {
		t.Errorf("the session folder was not removed")
	}
	if status, _ := kid.get(started.URL); status != 404 {
		t.Errorf("an ended session still answers: %d", status)
	}
	if len(s.transcodes) != 0 {
		t.Errorf("conversion slots still taken: %d", len(s.transcodes))
	}
}

// Interlaced H.264 (broadcast, DVD, some Blu-rays) plays only as sound on
// Apple devices and jerky in browsers: it is converted with deinterlacing,
// while ordinary H.264 is still copied.
func TestInterlacedIsConverted(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	dir := t.TempDir()
	video := func(name string, extra ...string) *CatItem {
		path := filepath.Join(dir, name)
		args := append([]string{"-v", "error", "-f", "lavfi", "-i", "testsrc=d=2:s=320x240:r=25", "-c:v", "libx264"}, extra...)
		if out, err := exec.Command(ffmpeg, append(args, path)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		st, _ := os.Stat(path)
		return &CatItem{Path: path, Size: st.Size(), ModTime: st.ModTime()}
	}
	interlaced := video("interlaced.mkv", "-flags", "+ilme+ildct", "-top", "1")
	progressive := video("progressive.mkv")

	p := newProber()
	s := &Server{lib: NewLibrary([]Root{{Path: dir}}, p)}
	for _, c := range []struct {
		it                  *CatItem
		copied, deinterlace bool
	}{{interlaced, false, true}, {progressive, true, false}} {
		info := p.probe(c.it.Path)
		p.known[probeJob{c.it.Path, c.it.Size, c.it.ModTime}.key()] = info
		conv := s.convertArgs(c.it, convertOptions{hls: true, burn: -1})
		copied, joined := conv.copied, strings.Join(conv.output, " ")
		if copied != c.copied || strings.Contains(joined, "bwdif") != c.deinterlace {
			t.Errorf("%s (field order %q): copied %v, args %s", filepath.Base(c.it.Path), info.stream("video").FieldOrder, copied, joined)
		}
	}
}

// Screenshots are taken in the background, shown to everyone and retaken by
// an administrator.
func TestScreenshots(t *testing.T) {
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg is not installed")
	}
	s, srv := serverFixture(t) // (it empties PATH)
	s.ffmpeg, s.prober.tool = ffmpeg, ffprobe
	clip := filepath.Join(s.firstRoot(), "Clip (2020)", "Clip (2020).mkv")
	os.MkdirAll(filepath.Dir(clip), 0o755)
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=60:s=320x240:r=10", "-c:v", "libx264", clip).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	os.MkdirAll(filepath.Join(s.screens.shotsDir(), "0123456789abcdef0123456789abcdef"), 0o755) // of a title that is gone
	s.refresh()
	go s.screens.run()
	defer s.screens.close()

	kid := newBrowser(t, srv, "kid")
	var lib libraryView
	kid.json("/api/library", &lib)
	id := ""
	for _, m := range lib.Movies {
		if m.Title == "Clip" {
			id = m.ID
		}
	}
	type state struct {
		State string
		Shots []string
	}
	wait := func() state {
		t.Helper()
		var st state
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			kid.json("/api/screens/"+id, &st)
			if st.State != "pending" {
				return st
			}
		}
		t.Fatalf("screenshots are still pending")
		return st
	}
	st := wait()
	if st.State != "ready" || len(st.Shots) != screenCount {
		t.Fatalf("screenshots: %+v", st)
	}
	status, body := kid.get(st.Shots[0])
	if status != 200 || !strings.HasPrefix(body, "\xff\xd8") { // a JPEG
		t.Errorf("screenshot: %d, %d bytes", status, len(body))
	}
	if exists(filepath.Join(s.screens.shotsDir(), "0123456789abcdef0123456789abcdef")) {
		t.Errorf("screenshots of a title that is gone were not removed")
	}
	var cat struct{ Movies []struct{ Title string } }
	kid.json("/api/library", &cat)
	for _, m := range cat.Movies {
		if strings.Contains(m.Title, "01") {
			t.Errorf("a screenshot was taken for a movie: %+v", cat.Movies)
		}
	}

	// Only an administrator retakes them; the new set replaces the old.
	if status, _ := kid.post("/api/screens/"+id+"/regenerate", nil); status != http.StatusForbidden {
		t.Errorf("a viewer retakes screenshots: %d", status)
	}
	read := func() (all []string) {
		for _, f := range s.screens.shots(id) {
			data, _ := os.ReadFile(f)
			all = append(all, string(data))
		}
		return all
	}
	before := read()
	boss := newBrowser(t, srv, "boss")
	if status, body := boss.post("/api/screens/"+id+"/regenerate", nil); status != 200 {
		t.Fatalf("regenerate: %d %s", status, body)
	}
	if again := wait(); again.State != "ready" || len(again.Shots) != screenCount {
		t.Fatalf("after regenerating: %+v", again)
	}
	if reflect.DeepEqual(before, read()) {
		t.Errorf("the regenerated screenshots are the same frames")
	}
	if status, _ := kid.get("/api/screens/" + id + "/../../config.yaml"); status == 200 {
		t.Errorf("a path outside the screenshots: %d", status)
	}
}

// An episode without a still of its own gets a frame taken from it; until
// then its thumb is the series backdrop, never the poster cropped.
func TestEpisodeStills(t *testing.T) {
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg is not installed")
	}
	s, srv := serverFixture(t) // (it empties PATH)
	s.ffmpeg, s.prober.tool = ffmpeg, ffprobe
	ent := filepath.Join(s.firstRoot(), "Star Trek - Enterprise (2001)")
	video := filepath.Join(ent, "Season 01", "Star Trek - Enterprise S01E03 - Fight or Flight.mkv")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=d=30:s=640x360:r=10", "-c:v", "libx264", video).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	os.WriteFile(filepath.Join(ent, "backdrop.jpg"), []byte("BACKDROP"), 0o644)
	s.refresh()

	kid := newBrowser(t, srv, "kid")
	episode := func() (id, thumb string) {
		var lib struct {
			Shows []struct {
				Seasons []struct {
					Episodes []struct{ ID, Title, Thumb string }
				}
			}
		}
		kid.json("/api/library", &lib)
		for _, e := range lib.Shows[0].Seasons[0].Episodes {
			if e.Title == "Episode 3" {
				return e.ID, e.Thumb
			}
		}
		t.Fatalf("no episode 3: %+v", lib)
		return
	}
	id, thumb := episode()
	if thumb != "" {
		t.Errorf("a still before one was taken: %q", thumb)
	}
	if _, body := kid.get("/api/image/" + id + "/thumb"); body != "BACKDROP" {
		t.Errorf("thumb without a still: %q", body)
	}

	if err := s.screens.takeStill(id); err != nil {
		t.Fatal(err)
	}
	s.refresh()
	if _, thumb = episode(); thumb == "" {
		t.Fatalf("the library does not know the still")
	}
	status, body := kid.get("/api/image/" + id + "/thumb?v=" + thumb)
	if status != 200 || !strings.HasPrefix(body, "\xff\xd8") {
		t.Errorf("still: %d %q", status, body[:min(len(body), 20)])
	}
	cat, _ := s.lib.Catalog()
	if got := s.imagePath(cat, id, "poster"); got != s.screens.stillPath(id) {
		t.Errorf("the Jellyfin primary image of the episode: %s", got)
	}

	// A still of its own replaces the frame, which is then removed.
	os.WriteFile(strings.TrimSuffix(video, ".mkv")+"-thumb.jpg", []byte("OWN"), 0o644)
	s.refresh()
	s.screens.prune()
	if exists(s.screens.stillPath(id)) {
		t.Errorf("the taken still was kept next to the episode's own")
	}
	if _, body := kid.get("/api/image/" + id + "/thumb"); body != "OWN" {
		t.Errorf("the episode's own still: %q", body)
	}
}

// A seek starts a new stream while the browser has not yet dropped the old
// one: the viewer's old stream is ended to make room, and only somebody
// else's streams can make the server busy.
func TestSeekFreesTheViewersSlot(t *testing.T) {
	defer func(w time.Duration) { slotWait = w }(slotWait)
	slotWait = 200 * time.Millisecond
	s := &Server{transcodes: make(chan struct{}, 2)}
	hold := func(viewer string) {
		ctx, done := s.conversions.begin(context.Background(), viewer)
		if !s.takeSlot(ctx) {
			t.Fatalf("%s got no slot", viewer)
		}
		go func() { <-ctx.Done(); <-s.transcodes; done() }() // like ffmpeg ending with its request
	}
	hold("someone else")
	hold("viewer")
	start := time.Now()
	hold("viewer") // the seek: the viewer's first stream makes room
	if time.Since(start) > 150*time.Millisecond {
		t.Errorf("the seek waited %v", time.Since(start))
	}
	ctx, done := s.conversions.begin(context.Background(), "a third viewer")
	defer done()
	if s.takeSlot(ctx) {
		t.Errorf("a third viewer got a slot while two others are watching")
	}
}

// A Jellyfin app gets a file its player takes as it is, any other one
// converted as HLS; the players it hands the address to bring no login, and
// the play session in the address lets them in — to that video only.
func TestJellyfinPlayback(t *testing.T) {
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg is not installed")
	}
	s, srv := serverFixture(t) // (it empties PATH)
	s.ffmpeg, s.prober.tool = ffmpeg, ffprobe
	film := filepath.Join(s.firstRoot(), "Clip (2020)", "Clip (2020).mkv")
	os.MkdirAll(filepath.Dir(film), 0o755)
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=40:s=320x240:r=25", "-f", "lavfi", "-i", "sine=d=40",
		"-c:v", "libx264", "-g", "50", "-c:a", "ac3", "-shortest", film).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	s.refresh()
	cat, _ := s.lib.Catalog()
	var clip, other string
	for _, it := range cat.Movies {
		if it.Title == "Clip" {
			clip = it.ID
		} else {
			other = it.ID
		}
	}

	b := newBrowser(t, srv, "")
	app := `MediaBrowser Client="Swiftfin tvOS", Device="Apple TV", DeviceId="tv1", Version="1.6.1"`
	var login struct{ AccessToken string }
	_, body := b.do("POST", "/Users/AuthenticateByName", map[string]string{"Username": "kid", "Pw": "kid-password"}, map[string]string{"Authorization": app})
	mustUnmarshal(t, body, &login)
	auth := map[string]string{"Authorization": app + `, Token="` + login.AccessToken + `"`}
	type source struct {
		SupportsDirectPlay, SupportsTranscoding bool
		TranscodingUrl                          string
	}
	type answer struct {
		MediaSources  []source
		PlaySessionId string
	}
	info := func(profile map[string]any) answer {
		t.Helper()
		var a answer
		status, body := b.do("POST", "/Items/"+clip+"/PlaybackInfo", map[string]any{"DeviceProfile": profile}, auth)
		if status != 200 {
			t.Fatalf("PlaybackInfo: %d %s", status, body)
		}
		mustUnmarshal(t, body, &a)
		return a
	}
	native := map[string]any{"DirectPlayProfiles": []map[string]string{{"Type": "Video", "Container": "mp4,m4v", "VideoCodec": "h264,hevc", "AudioCodec": "aac,ac3"}}}
	vlc := map[string]any{"DirectPlayProfiles": []map[string]string{{"Type": "Video", "VideoCodec": "h264,hevc,mpeg4", "AudioCodec": "aac,ac3,dts"}}}

	// The system player takes no Matroska: converted.
	conv := info(native)
	if src := conv.MediaSources[0]; src.SupportsDirectPlay || !src.SupportsTranscoding || !strings.Contains(src.TranscodingUrl, "master.m3u8") || !strings.Contains(src.TranscodingUrl, conv.PlaySessionId) {
		t.Fatalf("for the system player: %+v", conv)
	}
	// VLC takes it as it is.
	direct := info(vlc)
	if src := direct.MediaSources[0]; !src.SupportsDirectPlay || src.TranscodingUrl != "" {
		t.Fatalf("for VLC: %+v", direct)
	}

	// The file, as the player asks for it: no login, the play session.
	plain := newBrowser(t, srv, "")
	if status, body := plain.do("GET", "/Videos/"+clip+"/stream?static=true&playSessionId="+direct.PlaySessionId, nil, map[string]string{"Range": "bytes=0-3"}); status != 206 || len(body) != 4 {
		t.Errorf("by the play session: %d", status)
	}
	for name, path := range map[string]string{
		"no play session":    "/Videos/" + clip + "/stream?static=true",
		"a made-up one":      "/Videos/" + clip + "/stream?static=true&playSessionId=0123456789abcdef0123456789abcdef",
		"another video's":    "/Videos/" + other + "/stream?static=true&playSessionId=" + direct.PlaySessionId,
		"a conversion of it": "/Videos/" + other + "/master.m3u8?PlaySessionId=" + conv.PlaySessionId,
	} {
		if status, _ := plain.get(path); status != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, status)
		}
	}

	// The conversion: a master playlist, its media playlist, a segment.
	url := conv.MediaSources[0].TranscodingUrl
	url = url[:strings.Index(url, "&api_key=")] // the player may well drop the login
	status, master := plain.get(url)
	if status != 200 || !strings.Contains(master, "#EXT-X-STREAM-INF:BANDWIDTH=") {
		t.Fatalf("master playlist: %d %s", status, master)
	}
	lines := strings.Split(strings.TrimSpace(master), "\n")
	media := "/Videos/" + clip + "/" + strings.TrimSpace(lines[len(lines)-1])
	status, playlist := plain.get(media)
	// The whole film, from the start: the player can seek anywhere.
	if status != 200 || !strings.Contains(playlist, "#EXT-X-PLAYLIST-TYPE:VOD") || !strings.HasSuffix(playlist, "#EXT-X-ENDLIST\n") ||
		strings.Count(playlist, "#EXTINF") != 7 { // key frames every 2 s, segments from 6 s: 0, 6, 12 ... 36
		t.Fatalf("media playlist %s: %d\n%s", media, status, playlist)
	}
	if again, _ := plain.get(url); again != 200 || len(s.hls.vods) != 1 {
		t.Errorf("asked again: %d, %d conversions", again, len(s.hls.vods))
	}
	segment := func(n int) string { return media[:strings.LastIndex(media, "/")+1] + fmt.Sprintf("seg%05d.ts", n) }
	checkSegments(t, ffprobe, plain, segment, []float64{0, 6, 12, 18, 24, 30, 36}, 6, 0, 2) // a jump to the end, then the start
	// The app stops: the conversion ends.
	if status, _ := b.do("DELETE", "/Videos/ActiveEncodings?DeviceId=tv1&PlaySessionId="+conv.PlaySessionId, nil, auth); status != 204 || len(s.hls.vods) != 0 {
		t.Errorf("stop: %d, %d conversions left", status, len(s.hls.vods))
	}

	// A video that is re-encoded (MPEG-4 in AVI) gets a key frame every
	// 6 s and is cut there.
	avi := filepath.Join(s.firstRoot(), "Old (1999)", "Old (1999).avi")
	os.MkdirAll(filepath.Dir(avi), 0o755)
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=20:s=320x240:r=25", "-f", "lavfi", "-i", "sine=d=20",
		"-c:v", "mpeg4", "-c:a", "mp3", "-shortest", avi).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	s.refresh()
	cat, _ = s.lib.Catalog()
	var old string
	for _, it := range cat.Movies {
		if it.Title == "Old" {
			old = it.ID
		}
	}
	var a answer
	_, body = b.do("POST", "/Items/"+old+"/PlaybackInfo", map[string]any{"DeviceProfile": native}, auth)
	mustUnmarshal(t, body, &a)
	if a.MediaSources[0].TranscodingUrl == "" {
		t.Fatalf("the AVI is not converted: %+v", a)
	}
	_, master = plain.get(a.MediaSources[0].TranscodingUrl)
	lines = strings.Split(strings.TrimSpace(master), "\n")
	media = "/Videos/" + old + "/" + strings.TrimSpace(lines[len(lines)-1])
	if _, playlist = plain.get(media); strings.Count(playlist, "#EXTINF:6.000") != 3 {
		t.Fatalf("re-encoded playlist:\n%s", playlist)
	}
	checkSegments(t, ffprobe, plain, segment, []float64{0, 6, 12, 18}, 3, 0, 1)
}

// checkSegments fetches segments in the given order and checks that each
// begins at its time in the playlist, whichever run of ffmpeg made it.
func checkSegments(t *testing.T, ffprobe string, b *browser, url func(int) string, cuts []float64, order ...int) {
	t.Helper()
	for _, n := range order {
		status, body := b.get(url(n))
		if status != 200 || len(body) < 1000 {
			t.Errorf("segment %d: %d, %d bytes", n, status, len(body))
			continue
		}
		file := filepath.Join(t.TempDir(), "seg.ts")
		os.WriteFile(file, []byte(body), 0o644)
		out, _ := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time", "-of", "csv=p=0", file).Output()
		first := math.Inf(1)
		for _, line := range strings.Fields(string(out)) {
			if v, err := strconv.ParseFloat(strings.TrimSuffix(line, ","), 64); err == nil {
				first = math.Min(first, v)
			}
		}
		if math.Abs(first-cuts[n]) > 0.25 {
			t.Errorf("segment %d begins at %.3f s, the playlist says %.3f s", n, first, cuts[n])
		}
	}
}
