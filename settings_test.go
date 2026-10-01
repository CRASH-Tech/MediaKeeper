package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Settings found in config.yaml go into the database; the file keeps only
// the place of the database, the full one is kept as config.yaml.old.
func TestSettingsFromFile(t *testing.T) {
	setup(t, Config{})
	dir := t.TempDir()
	saveConfig(Config{OMDbKey: "omdb", Language: "ru-RU", Libraries: []Root{{Path: dir, Kind: rootMovies}},
		Server: ServerConfig{Name: "Living room", Port: 8300}})
	store, err := OpenAuth(filepath.Join(t.TempDir(), "mediakeeper.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	file, _, _ := loadConfig()
	cfg, ok, err := takeSettingsFile(store, file, func(string, ...any) {})
	if err != nil || !ok || cfg.OMDbKey != "omdb" || cfg.Server.Name != "Living room" || len(cfg.Libraries) != 1 || !cfg.Libraries[0].HasKey {
		t.Fatalf("taken: %+v %v", cfg, err)
	}
	if !fileExists(configPath()+".old") || fileHasSettings(mustLoad(t)) {
		t.Errorf("config.yaml was not cleared:\n%s", mustRead(t, configPath()))
	}
	// Nothing new in the file: nothing changes. A new value: it is taken.
	cfg, _, _ = takeSettingsFile(store, mustLoad(t), func(string, ...any) {})
	if cfg.OMDbKey != "omdb" {
		t.Errorf("after a second start: %+v", cfg)
	}
	saveConfig(Config{Language: "de-DE"})
	cfg, _, _ = takeSettingsFile(store, mustLoad(t), func(string, ...any) {})
	if cfg.Language != "de-DE" || cfg.OMDbKey != "omdb" || len(cfg.Libraries) != 1 {
		t.Errorf("a setting put into the file later: %+v", cfg)
	}
	// The place of the database stays in the file.
	saveConfig(Config{Server: ServerConfig{Database: "/data/mk.db"}})
	if file := mustLoad(t); file.Server.Database != "/data/mk.db" || fileHasSettings(file) {
		t.Errorf("bootstrap file: %+v", file)
	}
}

func mustLoad(t *testing.T) Config {
	t.Helper()
	c, _, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The settings page reads and changes the settings; library folders take
// effect at once, the port after a restart, and what a flag sets stays.
func TestSettingsAPI(t *testing.T) {
	s, srv := serverFixture(t)
	boss, kid := newBrowser(t, srv, "boss"), newBrowser(t, srv, "kid")
	if status, _ := kid.get("/api/settings"); status != http.StatusForbidden {
		t.Errorf("a viewer reads the settings: %d", status)
	}
	s.auth.SaveSettings(s.config()) // as the server's start would have them saved
	var view struct {
		TMDBKey struct {
			Set bool
			End string
		}
		Name      string
		Libraries []struct{ Path string }
		Locked    map[string]string
		Restart   []string
	}
	boss.json("/api/settings", &view)
	if !view.TMDBKey.Set || view.TMDBKey.End != "bkey" || view.Name != "Test" || len(view.Libraries) != 1 {
		t.Fatalf("settings: %+v", view)
	}

	// Another folder, with a film in it: in the library at once.
	films := t.TempDir()
	os.WriteFile(filepath.Join(films, "Iron.Man.2008.BDRip.mkv"), []byte("x"), 0o644)
	change := map[string]any{"libraries": []map[string]string{{"path": s.firstRoot()}, {"path": films, "kind": "movies"}}}
	if status, body := boss.post("/api/settings", change); status != 200 {
		t.Fatalf("add a folder: %d %s", status, body)
	}
	var lib libraryView
	boss.json("/api/library", &lib)
	if len(lib.Movies) != 3 {
		t.Errorf("the new folder is not in the library: %+v", lib.Movies)
	}
	for name, bad := range map[string]map[string]any{
		"a missing folder":   {"libraries": []map[string]string{{"path": "/no/such/folder"}}},
		"a relative path":    {"libraries": []map[string]string{{"path": "films"}}},
		"folders in folders": {"libraries": []map[string]string{{"path": films}, {"path": filepath.Dir(films)}}},
		"a bad port":         {"port": 70000},
		"a bad language":     {"language": "Russian"},
		"an unknown source":  {"sources": []string{"tmdb", "netflix"}},
	} {
		if status, _ := boss.post("/api/settings", bad); status != 400 {
			t.Errorf("%s accepted", name)
		}
	}
	// The port takes a restart; a key sent empty is removed.
	view.Restart = nil
	_, body := boss.post("/api/settings", map[string]any{"port": 8300, "tmdbKey": ""})
	mustUnmarshal(t, body, &view)
	if len(view.Restart) != 1 || view.TMDBKey.Set || s.config().TMDBKey != "" {
		t.Errorf("after the port and the key: %+v", view)
	}
	if saved, _, _ := s.auth.Settings(); saved.Server.Port != 8300 || len(saved.Libraries) != 2 {
		t.Errorf("saved: %+v", saved)
	}

	// What a flag sets is not changed (but is saved for when it is not set).
	s.live.locked["server.name"] = "-name"
	boss.post("/api/settings", map[string]any{"name": "Kitchen"})
	if s.serverName() != "Test" {
		t.Errorf("a locked setting changed: %s", s.serverName())
	}
}

// Without any account, the web interface sets the server up: the first
// administrator, the library folders, the rest. Afterwards that is closed.
func TestSetup(t *testing.T) {
	setup(t, Config{})
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "Iron.Man.2008.BDRip.mkv"), []byte("x"), 0o644)
	s, err := NewServer(ServerOptions{Name: "New", Port: 8200}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.dl.Close() })
	b := newBrowser(t, srv, "")

	var state struct{ Needed bool }
	b.json("/api/setup", &state)
	if !state.Needed {
		t.Fatalf("setup not needed: %+v", state)
	}
	var folders struct{ Folders []string }
	if status, body := b.get("/api/settings/folders?path=" + filepath.Dir(root)); status != 200 {
		t.Errorf("choosing folders during the setup: %d %s", status, body)
	} else {
		mustUnmarshal(t, body, &folders)
		if !strings.Contains(strings.Join(folders.Folders, "|"), filepath.Base(root)) {
			t.Errorf("folders: %+v", folders)
		}
	}
	if status, _ := b.post("/api/setup", map[string]any{"user": "me", "password": "1"}); status != 400 {
		t.Errorf("a short password accepted")
	}
	if status, _ := b.post("/api/setup", map[string]any{"user": "me", "password": "secret", "libraries": []map[string]string{{"path": "/no/such"}}}); status != 400 || s.auth.HasUsers() {
		t.Errorf("a wrong folder left an account behind")
	}
	dbDir, cache := t.TempDir(), t.TempDir()
	if status, body := b.post("/api/setup", map[string]any{"user": "me", "password": "secret", "name": "Living room", "omdbKey": "key",
		"libraries": []map[string]string{{"path": root, "kind": "movies"}}, "database": dbDir, "cache": cache}); status != 200 {
		t.Fatalf("setup: %d %s", status, body)
	}
	if s.auth.Path() != filepath.Join(dbDir, "mediakeeper.db") || s.cacheRoot() != cache {
		t.Errorf("the database in %s, the cache in %s", s.auth.Path(), s.cacheRoot())
	}
	var me struct {
		Name  string
		Admin bool
	}
	b.json("/api/me", &me) // signed in by the setup
	if me.Name != "me" || !me.Admin {
		t.Errorf("after the setup: %+v", me)
	}
	var lib libraryView
	b.json("/api/library", &lib)
	if len(lib.Movies) != 1 || s.serverName() != "Living room" || s.config().OMDbKey != "key" {
		t.Errorf("the library after the setup: %+v, name %s", lib.Movies, s.serverName())
	}
	if saved, _, _ := s.auth.Settings(); len(saved.Libraries) != 1 || saved.Libraries[0].Key != "" || !saved.Libraries[0].HasKey {
		t.Errorf("saved: %+v", saved.Libraries)
	}
	other := newBrowser(t, srv, "")
	if status, _ := other.post("/api/setup", map[string]any{"user": "intruder", "password": "secret"}); status != http.StatusForbidden {
		t.Errorf("a second setup: %d", status)
	}
	if status, _ := other.get("/api/settings/folders"); status == 200 {
		t.Errorf("the folders of the server are open after the setup")
	}
}

// A folder's key stays with it: removing the first folder changes no
// identifier of the titles in the others.
func TestRootKeysKept(t *testing.T) {
	s, srv := serverFixture(t)
	boss := newBrowser(t, srv, "boss")
	films := t.TempDir()
	os.WriteFile(filepath.Join(films, "Iron.Man.2008.BDRip.mkv"), []byte("x"), 0o644)
	boss.post("/api/settings", map[string]any{"libraries": []map[string]string{{"path": s.firstRoot()}, {"path": films}}})
	id := func() string {
		var lib struct{ Movies []struct{ ID, Title string } }
		boss.json("/api/library", &lib)
		for _, m := range lib.Movies {
			if m.Title == "Iron Man" {
				return m.ID
			}
		}
		return ""
	}
	before := id()
	boss.post("/api/settings", map[string]any{"libraries": []map[string]string{{"path": films}}})
	if after := id(); before == "" || after != before {
		t.Errorf("the id changed when the first folder went: %q -> %q", before, after)
	}
}

// The folders a server first starts with are saved, keeping the identifiers
// they had; once chosen — even as none — they are not replaced.
func TestFirstLibraries(t *testing.T) {
	setup(t, Config{})
	store, err := OpenAuth(filepath.Join(t.TempDir(), "mediakeeper.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, b := t.TempDir(), t.TempDir()
	cli := []Root{{Path: a}, {Path: b, Kind: rootMovies}}
	var cfg Config
	if err := firstLibraries(store, &cfg, cli, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	saved, _, _ := store.Settings()
	if len(saved.Libraries) != 2 || !saved.LibrariesSet || rootKey(saved.Libraries, 0) != rootKey(cli, 0) || rootKey(saved.Libraries, 1) != rootKey(cli, 1) {
		t.Fatalf("saved: %+v", saved.Libraries)
	}
	none, err := settingsChange{Libraries: &[]Root{}}.apply(saved)
	if err != nil || len(none.Libraries) != 0 || !none.LibrariesSet {
		t.Fatalf("no folders: %+v %v", none, err)
	}
	store.SaveSettings(none)
	firstLibraries(store, &none, []Root{{Path: t.TempDir()}}, func(string, ...any) {})
	if saved, _, _ := store.Settings(); len(saved.Libraries) != 0 {
		t.Errorf("chosen folders replaced: %+v", saved.Libraries)
	}
}

// The database moves while the server runs: accounts and sessions go on,
// config.yaml says where it is now, and the old file is kept.
func TestMoveDatabase(t *testing.T) {
	s, srv := serverFixture(t)
	boss := newBrowser(t, srv, "boss")
	s.auth.SaveSettings(s.config())
	old := s.auth.Path()
	occupied := filepath.Join(t.TempDir(), "taken.db")
	os.WriteFile(occupied, []byte("x"), 0o600)
	for name, bad := range map[string]string{"a relative path": "data/mk.db", "a file that is there": occupied} {
		if status, _ := boss.post("/api/settings", map[string]any{"database": bad}); status != 400 {
			t.Errorf("%s accepted", name)
		}
	}
	dir := t.TempDir()
	if status, body := boss.post("/api/settings", map[string]any{"database": dir}); status != 200 {
		t.Fatalf("move: %d %s", status, body)
	}
	moved := filepath.Join(dir, "mediakeeper.db")
	if s.auth.Path() != moved || !fileExists(moved) || !fileExists(old+".old") || fileExists(old) {
		t.Errorf("after the move: %s, old there: %v", s.auth.Path(), fileExists(old))
	}
	if file := mustLoad(t); file.Server.Database != moved {
		t.Errorf("config.yaml: %+v", file.Server)
	}
	var me struct{ Name string }
	if boss.json("/api/me", &me); me.Name != "boss" { // the session was moved too
		t.Errorf("signed out by the move: %+v", me)
	}
	newBrowser(t, srv, "kid") // signing in reads the moved database
	if saved, ok, _ := s.auth.Settings(); !ok || saved.TMDBKey != "tmdbkey" {
		t.Errorf("the settings did not move: %+v", saved)
	}

	// Set by -db: not moved.
	s.live.locked["server.database"] = "-db"
	if status, _ := boss.post("/api/settings", map[string]any{"database": t.TempDir()}); status != 400 {
		t.Errorf("a locked database moved")
	}
}

// On the very first start nothing is written until the setup says where the
// database goes; then it is made there, with what the setup chose.
func TestSetupMakesDatabase(t *testing.T) {
	setup(t, Config{})
	dir := t.TempDir()
	configFile = filepath.Join(dir, "config.yaml")
	defer func() { configFile = "" }()
	def := filepath.Join(dir, "mediakeeper.db")
	if !freshStart(def, filepath.Join(dir, "server.json"), false) || freshStart(def, "", true) {
		t.Fatal("freshStart")
	}
	for _, chosen := range []string{"", filepath.Join(t.TempDir(), "data")} {
		store, err := OpenMemoryAuth(def)
		if err != nil {
			t.Fatal(err)
		}
		s, err := NewServer(ServerOptions{Auth: store, Name: "New", Port: 8200}, func(string, ...any) {})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(s.Handler())
		b := newBrowser(t, srv, "")
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("written before the setup: %v", entries)
		}
		// A folder can be made from the picker, before the setup too.
		var made struct{ Path string }
		if status, body := b.post("/api/settings/folders", map[string]string{"path": t.TempDir(), "name": "Films"}); status != 200 {
			t.Errorf("making a folder: %d %s", status, body)
		} else if mustUnmarshal(t, body, &made); filepath.Base(made.Path) != "Films" || !fileExists(made.Path) {
			t.Errorf("made: %+v", made)
		}
		if status, _ := b.post("/api/settings/folders", map[string]string{"path": t.TempDir(), "name": "../x"}); status != 400 {
			t.Errorf("a name with a slash made a folder")
		}
		req := map[string]any{"user": "me", "password": "secret"}
		want := def
		if chosen != "" {
			req["database"], want = chosen, filepath.Join(chosen, "mediakeeper.db")
		}
		if status, body := b.post("/api/setup", req); status != 200 {
			t.Fatalf("setup: %d %s", status, body)
		}
		if s.auth.InMemory() || s.auth.Path() != want || !fileExists(want) || fileExists(want+".old") {
			t.Errorf("database %s (in memory %v), want %s", s.auth.Path(), s.auth.InMemory(), want)
		}
		if file := mustLoad(t); file.Server.Database != map[bool]string{true: "", false: want}[chosen == ""] {
			t.Errorf("config.yaml says %q", file.Server.Database)
		}
		var users int
		s.auth.conn().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
		if _, ok, _ := s.auth.Settings(); !ok || users != 1 {
			t.Errorf("the account or the settings are not in the new database")
		}
		srv.Close()
		s.dl.Close()
		store.Close()
		os.Remove(def)
		os.Remove(configFile)
	}
}
