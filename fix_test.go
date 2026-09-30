package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	dir := filepath.Join(s.root, moviesFolder, "На линии огня (1993)")
	for name, want := range map[string]string{
		"На линии огня (1993).mkv":    "0123456789",
		"На линии огня (1993).en.srt": "-->",
		"На линии огня (1993).nfo":    `source="tmdb" id="10"`,
	} {
		if data, _ := os.ReadFile(filepath.Join(dir, name)); !strings.Contains(string(data), want) {
			t.Errorf("%s: %q\n  %s", name, data, strings.Join(tree(t, s.root), "\n  "))
		}
	}
	for _, gone := range []string{"Iron Man (2008)", moviesFolder + "/На линии огня (1993)/poster.jpg", moviesFolder + "/На линии огня (1993)/Iron Man (2008).nfo"} {
		if exists(filepath.Join(s.root, filepath.FromSlash(gone))) {
			t.Errorf("%s is still there", gone)
		}
	}

	// A loose file, by picking a candidate.
	if status, body := boss.post("/api/fix/"+loose, resolveRequest{Source: "tmdb", ID: "777", Kind: kindMovie}); status != 200 {
		t.Fatalf("fix a loose file: %d %s", status, body)
	}
	if !exists(filepath.Join(s.root, moviesFolder, "Мятеж (2025)", "Мятеж (2025).avi")) {
		t.Errorf("the loose file was not filed:\n  %s", strings.Join(tree(t, s.root), "\n  "))
	}
	// An identity whose name is taken by another file is refused, and
	// nothing moves.
	taken := filepath.Join(s.root, moviesFolder, "Мятеж (2025)", "Мятеж (2025).mkv")
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
	ent := filepath.Join(s.root, showsFolder, "Звёздный путь - Энтерпрайз (2001)")
	for _, want := range []string{
		"tvshow.nfo", "poster.jpg",
		"Season 01/Звёздный путь - Энтерпрайз S01E01-E02 - Разорванный круг (1) + Разорванный круг (2).mkv",
		"Season 01/Звёздный путь - Энтерпрайз S01E03.mkv", // the catalogue has no such episode: no title
		"Season 02/Звёздный путь - Энтерпрайз S02E01 - Ударная волна - Часть 2.mkv",
	} {
		if !exists(filepath.Join(ent, filepath.FromSlash(want))) {
			t.Errorf("missing %s\n  %s", want, strings.Join(tree(t, s.root), "\n  "))
		}
	}
	if exists(filepath.Join(s.root, "Star Trek - Enterprise (2001)")) {
		t.Errorf("the old series folder is still there")
	}
	for _, path := range tree(t, s.root) {
		if strings.Contains(path, "Broken Bow") || strings.Contains(path, "Shockwave") {
			t.Errorf("a file of the old identity is left: %s", path)
		}
	}

	// The catalogue shows the result at once, and the run can be undone.
	boss.json("/api/library", &lib)
	if len(lib.Shows) != 1 || lib.Shows[0].Title != "Звёздный путь: Энтерпрайз" || len(lib.Movies) != 2 {
		t.Errorf("library after fixes: %+v", lib)
	}
	if len(journals(s.root)) != 3 {
		t.Errorf("journals: %d, want one per fix", len(journals(s.root)))
	}
	if status, _ := boss.post("/api/fix/0123456789abcdef0123456789abcdef", resolveRequest{Ref: "tmdb:10"}); status != 404 {
		t.Errorf("unknown title: %d", status)
	}
}

// The library as the web interface shows it carries what titles can be
// grouped by.
func TestLibraryGroups(t *testing.T) {
	s, srv := serverFixture(t)
	nfo := filepath.Join(s.root, "Iron Man (2008)", "Iron Man (2008).nfo")
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
	video := filepath.Join(s.root, "Clip (2020).avi")
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
