package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The accounts and watch states of an earlier version's server.json move
// into the database once; the Jellyfin apps keep knowing the server.
func TestImportServerJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "server.json")
	pbkdf2Rounds = 1
	hash := hashPassword("secret")
	os.WriteFile(legacy, []byte(`{"server_id": "0123456789abcdef0123456789abcdef",
		"users": [{"id": "u1", "name": "Boss", "hash": "`+hash+`", "admin": true}, {"id": "u2", "name": "kid", "hash": "x"}],
		"sessions": {"tok": {"user": "u1", "device": "iPad", "created": "2026-09-01T10:00:00Z"}, "orphan": {"user": "nobody"}},
		"watched": {"u1": {"movie": {"position": 1500, "played": false, "favorite": true, "last_played": "2026-09-30T20:00:00Z"}}}}`), 0o600)
	db := filepath.Join(dir, "mediakeeper.db")
	a, err := OpenAuth(db, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if a.ServerID != "0123456789abcdef0123456789abcdef" || len(a.List()) != 2 || a.ByToken("tok") == nil || a.ByToken("orphan") != nil {
		t.Fatalf("imported: id %s, users %+v", a.ServerID, a.List())
	}
	if p := a.Progress("u1", "movie"); p.Position != 1500 || !p.Favorite || p.LastPlayed.IsZero() {
		t.Errorf("progress: %+v", p)
	}
	if _, _, err := a.Login("boss", "secret", "web"); err != nil {
		t.Errorf("the imported password: %v", err)
	}
	a.Close()
	if exists(legacy) || !exists(legacy+".old") {
		t.Errorf("server.json was not kept aside")
	}
	if st, _ := os.Stat(db); st.Mode().Perm() != 0o600 {
		t.Errorf("the database is readable by others: %v", st.Mode())
	}

	// Opened again, everything is still there, and nothing is imported twice.
	os.WriteFile(legacy, []byte(`{"users": [{"id": "u9", "name": "intruder"}]}`), 0o600)
	a, err = OpenAuth(db, legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.List()) != 2 || a.Progress("u1", "movie").Position != 1500 {
		t.Errorf("after reopening: %+v", a.List())
	}
}

// Every sitting in front of a video goes into the history, counting what
// was really played; ratings, the watchlist and notes are kept per user.
func TestHistoryAndRatings(t *testing.T) {
	pbkdf2Rounds = 1
	a, err := OpenAuth(filepath.Join(t.TempDir(), "mediakeeper.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	u, _ := a.SetUser("viewer", "secret", false)
	film := Viewing{ItemID: "film", Kind: kindMovie, Title: "Film", Year: 2010}

	a.Update(u.ID, "film", func(p *Progress) { p.Planned, p.Rating, p.Note = time.Now(), 8, "with popcorn" })
	a.Watch(u.ID, film, 10, 6000)
	time.Sleep(10 * time.Millisecond)
	a.Watch(u.ID, film, 20, 6000)   // 10 s played
	a.Watch(u.ID, film, 3000, 6000) // a jump ahead: not watched
	a.Watch(u.ID, film, 3040, 6000) // more than the time since the last report: not counted either
	a.Watch(u.ID, film, 5800, 6000) // the end
	if p := a.Progress(u.ID, "film"); !p.Played || p.PlayCount != 1 || !p.Planned.IsZero() || p.Rating != 8 || p.Note != "with popcorn" {
		t.Errorf("after watching to the end: %+v", p)
	}
	// Finished, so listed; only the 10 s really played count, not the jumps.
	all, _ := a.History(u.ID, HistoryCursor{}, 10)
	if len(all) != 1 || !all[0].Finished || all[0].Watched != 10 || all[0].Title != "Film" || all[0].Year != 2010 {
		t.Fatalf("history: %+v", all)
	}
	// A sitting opened and closed again is not listed.
	a.Watch(u.ID, Viewing{ItemID: "other", Kind: kindMovie, Title: "Other"}, 40, 6000)
	if all, _ = a.History(u.ID, HistoryCursor{}, 10); len(all) != 1 {
		t.Errorf("a glance is listed: %+v", all)
	}
	a.db.Exec(`DELETE FROM history WHERE item_id = 'other'`)
	a.db.Exec(`UPDATE history SET watched = 3600`)
	// A later sitting is a new entry.
	a.db.Exec(`UPDATE history SET ended = ended - 7200, started = started - 7200`)
	a.Watch(u.ID, film, 100, 6000)
	a.db.Exec(`UPDATE history SET watched = 120 WHERE watched = 0`)
	if all, _ = a.History(u.ID, HistoryCursor{}, 10); len(all) != 2 {
		t.Fatalf("two sittings: %+v", all)
	}
	stats := a.Stats(u.ID, time.Now().Add(-time.Hour))
	if stats.Total != 3720 || stats.Since != 120 || stats.Titles != 1 || stats.Finished != 1 {
		t.Errorf("stats: %+v", stats)
	}
	if err := a.ForgetHistory(u.ID, all[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := a.ForgetHistory("someone else", all[1].ID); err == nil {
		t.Errorf("another user's entry was removed")
	}

	// The file is renamed: everything follows the new identifier.
	a.Moved(map[string]string{"film": "film2"})
	if p := a.Progress(u.ID, "film2"); p.Rating != 8 || !p.Played {
		t.Errorf("after the rename: %+v", p)
	}
	if all, _ = a.History(u.ID, HistoryCursor{}, 10); len(all) != 1 || all[0].ItemID != "film2" {
		t.Errorf("history after the rename: %+v", all)
	}

	// A guest is not remembered; a deleted user takes everything along.
	a.Watch("guest-1", film, 500, 6000)
	if a.Progress("guest-1", "film").Position != 0 {
		t.Errorf("a guest was remembered")
	}
	boss, _ := a.SetUser("boss", "secret", true)
	_ = boss
	if err := a.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	a.db.QueryRow(`SELECT (SELECT COUNT(*) FROM watch) + (SELECT COUNT(*) FROM history)`).Scan(&left)
	if left != 0 {
		t.Errorf("%d rows of a deleted user are left", left)
	}
}

// The database is next to the settings unless given with -db, in
// MEDIAKEEPER_DB or in the settings; a folder means mediakeeper.db in it.
func TestDatabasePath(t *testing.T) {
	setup(t, Config{})
	defer func() { configFile = "" }()
	if got := databasePath("", ""); got != filepath.Join(filepath.Dir(configPath()), "mediakeeper.db") {
		t.Errorf("default: %s", got)
	}
	dir := t.TempDir()
	if got := databasePath("", dir); got != filepath.Join(dir, "mediakeeper.db") {
		t.Errorf("a folder in the settings: %s", got)
	}
	t.Setenv("MEDIAKEEPER_DB", filepath.Join(dir, "env.db"))
	if got := databasePath("", dir); got != filepath.Join(dir, "env.db") {
		t.Errorf("the variable over the settings: %s", got)
	}
	if got := databasePath(filepath.Join(dir, "flag.db"), dir); got != filepath.Join(dir, "flag.db") {
		t.Errorf("the flag over everything: %s", got)
	}
	// A relative path in the settings is relative to the settings file.
	t.Setenv("MEDIAKEEPER_DB", "")
	if got := databasePath("", "data/mk.db"); got != filepath.Join(filepath.Dir(configPath()), "data", "mk.db") {
		t.Errorf("a relative path in the settings: %s", got)
	}
	// Written into the settings file with the rest, and documented there.
	cfg, _, _ := loadConfig()
	cfg.Server.Database, cfg.Server.Cache = "/data/mk.db", "/data/cache"
	saveConfig(cfg)
	text := mustRead(t, configPath())
	wantAll(t, "settings file", text, "database: /data/mk.db", "cache: /data/cache", "MEDIAKEEPER_CONFIG", "MEDIAKEEPER_DB", "MEDIAKEEPER_CACHE")
	if again, _, err := loadConfig(); err != nil || again.Server.Cache != "/data/cache" {
		t.Errorf("read back: %+v %v", again.Server, err)
	}
}

// The cache of generated images is .cache in the first library folder
// unless the settings, MEDIAKEEPER_CACHE or -cache put it elsewhere.
func TestCachePath(t *testing.T) {
	setup(t, Config{})
	t.Setenv("MEDIAKEEPER_CACHE", "")
	if got := cachePath("", ""); got != "" {
		t.Errorf("default: %q", got)
	}
	if got := cachePath("", "thumbs"); got != filepath.Join(filepath.Dir(configPath()), "thumbs") {
		t.Errorf("relative in the settings: %s", got)
	}
	dir := t.TempDir()
	t.Setenv("MEDIAKEEPER_CACHE", dir)
	if got := cachePath("", "thumbs"); got != dir {
		t.Errorf("the variable over the settings: %s", got)
	}
	if got := cachePath(filepath.Join(dir, "x"), "thumbs"); got != filepath.Join(dir, "x") {
		t.Errorf("the flag over everything: %s", got)
	}

	root := t.TempDir()
	cfg, _, _ := loadConfig()
	for _, cache := range []string{"", dir} {
		s, err := NewServer(ServerOptions{Roots: []Root{{Path: root}}, Cache: cache, Name: "x", Port: 8200, Config: cfg}, func(string, ...any) {})
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, ".cache")
		if cache != "" {
			want = cache
		}
		if s.screens.dir != filepath.Join(want, "screenshots") || s.screens.stillsDir != filepath.Join(want, "stills") {
			t.Errorf("cache %q: screenshots in %s, stills in %s", cache, s.screens.dir, s.screens.stillsDir)
		}
		s.dl.Close()
		s.auth.Close()
	}
}

// A user rates titles, keeps a watchlist and notes, and sees their history;
// all of it survives the files being renamed by a fix.
func TestMineAPI(t *testing.T) {
	s, srv := serverFixture(t)
	kid := newBrowser(t, srv, "kid")
	var lib libraryView
	kid.json("/api/library", &lib)
	iron, show := lib.Movies[0].ID, lib.Shows[0].ID

	for _, bad := range []map[string]any{{"rating": 11}, {"rating": -1}} {
		if status, _ := kid.post("/api/mine/"+iron, bad); status != 400 {
			t.Errorf("%v accepted", bad)
		}
	}
	if status, _ := kid.post("/api/mine/nothing", map[string]any{"rating": 5}); status != 404 {
		t.Errorf("an unknown title: %d", status)
	}
	if status, body := kid.post("/api/mine/"+iron, map[string]any{"rating": 9, "note": " rewatch with Ann ", "favorite": true}); status != 200 || !strings.Contains(body, `"myRating":9`) {
		t.Fatalf("rate: %d %s", status, body)
	}
	kid.post("/api/mine/"+show, map[string]any{"planned": true})
	type mineView struct {
		Movies []struct {
			ID, Note string
			MyRating int
			Favorite bool
		}
		Shows []struct {
			ID      string
			Planned int64
		}
	}
	var mine mineView
	kid.json("/api/library", &mine)
	if m := mine.Movies[0]; m.MyRating != 9 || m.Note != "rewatch with Ann" || !m.Favorite {
		t.Errorf("movie: %+v", m)
	}
	if mine.Shows[0].Planned == 0 {
		t.Errorf("the series is not on the watchlist: %+v", mine.Shows[0])
	}
	// Another user sees none of it.
	boss := newBrowser(t, srv, "boss")
	mine = mineView{} // fields an answer leaves out would stay from the last one
	boss.json("/api/library", &mine)
	if mine.Movies[0].MyRating != 0 || mine.Shows[0].Planned != 0 {
		t.Errorf("another user sees the marks: %+v", mine)
	}
	// Guests keep nothing.
	s.guests = true
	guest := newBrowser(t, srv, "")
	if status, _ := guest.post("/api/mine/"+iron, map[string]any{"rating": 3}); status != http.StatusForbidden {
		t.Errorf("a guest rated: %d", status)
	}
	if status, _ := guest.get("/api/history"); status != http.StatusForbidden {
		t.Errorf("a guest has a history: %d", status)
	}

	// Watching fills the history.
	kid.post("/api/progress/"+iron, map[string]any{"position": 300})
	s.auth.db.Exec(`UPDATE history SET watched = 600`)
	var hist struct {
		Entries []struct {
			ID                  int64
			Title, Link, ItemID string
			Watched             float64
		}
		Stats HistoryStats
	}
	kid.json("/api/history", &hist)
	if len(hist.Entries) != 1 || hist.Entries[0].Title != "Iron Man & Co" || hist.Entries[0].Link != "#movie/"+iron || hist.Stats.Total != 600 {
		t.Fatalf("history: %+v", hist)
	}

	// The fix renames the files; the rating, the note and the history follow.
	if status, body := boss.post("/api/fix/"+iron, resolveRequest{Ref: "tmdb:10"}); status != 200 {
		t.Fatalf("fix: %d %s", status, body)
	}
	mine = mineView{}
	kid.json("/api/library", &mine)
	found := false
	for _, m := range mine.Movies {
		if m.MyRating == 9 && m.Note == "rewatch with Ann" {
			found = m.ID != iron
		}
	}
	if !found {
		t.Errorf("the rating did not follow the renamed file: %+v", mine.Movies)
	}
	hist.Entries = nil
	kid.json("/api/history", &hist)
	if len(hist.Entries) != 1 || hist.Entries[0].Link == "#movie/"+iron || hist.Entries[0].Link == "" {
		t.Errorf("the history does not lead to the renamed title: %+v", hist.Entries)
	}
	if status, _ := kid.do("DELETE", "/api/history/"+strconv.FormatInt(hist.Entries[0].ID, 10), nil, nil); status != 200 {
		t.Errorf("forget an entry: %d", status)
	}
	hist.Entries = nil
	kid.json("/api/history", &hist)
	if len(hist.Entries) != 0 {
		t.Errorf("after forgetting: %+v", hist.Entries)
	}
}

// The history is read page by page, latest begun first, whatever order the
// entries were written in.
func TestHistoryPages(t *testing.T) {
	pbkdf2Rounds = 1
	a, err := OpenAuth(filepath.Join(t.TempDir(), "mediakeeper.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	u, _ := a.SetUser("viewer", "secret", false)
	now := time.Now().Unix()
	for _, e := range []struct {
		title string
		ago   int64
	}{{"third", 3600}, {"first", 3 * 86400}, {"second", 86400}, {"fourth", 60}, {"also fourth", 60}} {
		a.db.Exec(`INSERT INTO history (user_id, item_id, kind, title, started, ended, watched) VALUES (?, ?, 'movie', ?, ?, ?, 600)`,
			u.ID, e.title, e.title, now-e.ago, now-e.ago+600)
	}
	var titles []string
	cursor := HistoryCursor{}
	for page := 0; page < 5; page++ {
		entries, _ := a.History(u.ID, cursor, 2)
		for _, e := range entries {
			titles = append(titles, e.Title)
		}
		if len(entries) < 2 {
			break
		}
		last := entries[len(entries)-1]
		cursor = HistoryCursor{last.Started.Unix(), last.ID}
	}
	if got := strings.Join(titles, ", "); got != "also fourth, fourth, third, second, first" {
		t.Errorf("pages: %s", got)
	}
	if c := parseHistoryCursor(HistoryCursor{1790000000, 42}.String()); c != (HistoryCursor{1790000000, 42}) {
		t.Errorf("cursor: %+v", c)
	}
}
