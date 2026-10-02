package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Artwork missing since the titles were described comes again from the
// catalogue entries their .nfo files name: for one title, or for all.
func TestArtwork(t *testing.T) {
	s, srv := serverFixture(t)
	boss := newBrowser(t, srv, "boss")
	root := s.firstRoot()
	iron := filepath.Join(root, "Iron Man (2008)")
	ent := filepath.Join(root, "Star Trek - Enterprise (2001)")
	mark := func(path, source, id, kind string) {
		data, _ := os.ReadFile(path)
		os.WriteFile(path, []byte(strings.Replace(string(data), "?>", `?>`+"\n"+`<!-- mediakeeper source="`+source+`" id="`+id+`" kind="`+kind+`" -->`, 1)), 0o644)
	}
	mark(filepath.Join(iron, "Iron Man (2008).nfo"), "tmdb", "1726", kindMovie)
	mark(filepath.Join(ent, "tvshow.nfo"), "tmdb", "314", kindTV)
	os.Remove(filepath.Join(iron, "poster.jpg"))
	s.refresh()

	var lib struct {
		Movies []struct{ ID, Title string }
		Shows  []struct{ ID, Title string }
	}
	boss.json("/api/library", &lib)
	ids := map[string]string{}
	for _, m := range lib.Movies {
		ids[m.Title] = m.ID
	}
	for _, sh := range lib.Shows {
		ids[sh.Title] = sh.ID
	}

	type job struct {
		Running       bool
		Done, Fetched int
		Error         string
		Result        *struct{ Fetched int }
	}
	wait := func() job {
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			var j job
			boss.json("/api/artwork", &j)
			if !j.Running {
				return j
			}
			if time.Now().After(deadline) {
				t.Fatal("the job did not finish")
			}
		}
	}

	// One movie: what is missing.
	if status, body := boss.post("/api/meta/"+ids["Iron Man & Co"]+"/artwork", map[string]any{}); status != 200 {
		t.Fatalf("movie artwork: %d %s", status, body)
	}
	if j := wait(); j.Result == nil || j.Result.Fetched == 0 {
		t.Errorf("movie job: %+v", j)
	}
	if data, _ := os.ReadFile(filepath.Join(iron, "poster.jpg")); !strings.HasPrefix(string(data), "JPEG") {
		t.Errorf("poster: %q", data)
	}
	// A title whose description names no entry.
	boss.post("/api/meta/"+ids["Some Unknown Movie"]+"/artwork", map[string]any{})
	if j := wait(); !strings.Contains(j.Error, "fill it in") {
		t.Errorf("without an entry: %+v", j)
	}
	// A series, replaced: poster and season poster anew, a still kept.
	if status, body := boss.post("/api/meta/"+ids["Star Trek: Enterprise"]+"/artwork", map[string]any{"replace": true}); status != 200 {
		t.Fatalf("series artwork: %d %s", status, body)
	}
	wait()
	for file, want := range map[string]string{"poster.jpg": "JPEG", "season01-poster.jpg": "JPEG",
		"Season 01/Star Trek - Enterprise S01E01-E02 - Broken Bow-thumb.jpg": "THUMB"} {
		if data, _ := os.ReadFile(filepath.Join(ent, file)); !strings.HasPrefix(string(data), want) {
			t.Errorf("%s: %q", file, data)
		}
	}
	if status, _ := newBrowser(t, srv, "kid").post("/api/artwork", nil); status != 403 {
		t.Errorf("a viewer started the job: %d", status)
	}

	// The whole library: started, watched, done.
	os.Remove(filepath.Join(iron, "poster.jpg"))
	s.refresh()
	if status, body := boss.post("/api/artwork", nil); status != 200 {
		t.Fatalf("job: %d %s", status, body)
	}
	if j := wait(); j.Done == 0 || j.Fetched == 0 || !exists(filepath.Join(iron, "poster.jpg")) {
		t.Errorf("job: %+v", j)
	}
}

// An image server out of reach is given up after two tries.
func TestArtworkUnreachable(t *testing.T) {
	var logged []string
	a := newArtwork("X", func(f string, args ...any) { logged = append(logged, f) }, nil)
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		a.get("http://127.0.0.1:1/p"+itoa(i)+".jpg", filepath.Join(dir, itoa(i)+".jpg"), false)
	}
	a.done()
	if len(a.Problems) != 3 || !strings.Contains(a.Problems[2], "3 more picture(s) not tried") {
		t.Errorf("problems: %q", a.Problems)
	}
}
