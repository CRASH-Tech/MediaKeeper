package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigYAML(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	yes := false
	want := Config{TMDBKey: "k: with a colon", OMDbKey: "abc123", Sources: []string{"omdb", "tvmaze"},
		Server: ServerConfig{Name: "Living room", Port: 8300, DLNA: &yes}}
	if err := saveConfig(want); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(configPath())
	text := string(data)
	for _, line := range []string{"omdb_api_key: abc123", "# kinopoisk_api_key:", "sources:", "  name: Living room", "  port: 8300", "  dlna: false", "  # guests: true"} {
		if !strings.Contains(text, line) {
			t.Errorf("config.yaml lacks %q:\n%s", line, text)
		}
	}
	if st, _ := os.Stat(configPath()); st.Mode().Perm() != 0o600 {
		t.Errorf("the file with API keys is readable by others: %v", st.Mode())
	}
	got, exists, err := loadConfig()
	if err != nil || !exists || got.TMDBKey != want.TMDBKey || got.OMDbKey != "abc123" || strings.Join(got.Sources, ",") != "omdb,tvmaze" ||
		got.Server.Name != "Living room" || got.Server.Port != 8300 || got.Server.DLNA == nil || *got.Server.DLNA || got.Server.Guests != nil {
		t.Errorf("round trip: %+v %v %v", got, exists, err)
	}

	// Hand-written files: comments, flow lists; a typo is reported.
	os.WriteFile(configPath(), []byte("# mine\nlanguage: ru-RU\nsources: [imdb]\nserver:\n  guests: false\n"), 0o600)
	if got, _, err = loadConfig(); err != nil || got.Language != "ru-RU" || got.Sources[0] != "imdb" || got.Server.Guests == nil || *got.Server.Guests {
		t.Errorf("hand-written: %+v %v", got, err)
	}
	os.WriteFile(configPath(), []byte("omdb_apikey: x\n"), 0o600)
	if _, _, err = loadConfig(); err == nil || !strings.Contains(err.Error(), "omdb_apikey") {
		t.Errorf("a misspelt key must be an error, got %v", err)
	}
	os.WriteFile(configPath(), nil, 0o600)
	if _, exists, err = loadConfig(); err != nil || !exists {
		t.Errorf("an empty file: %v %v", exists, err)
	}
}

// The config.json of earlier versions is converted once.
func TestConfigFromJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := filepath.Join(legacyConfigDir(), "config.json")
	os.MkdirAll(filepath.Dir(old), 0o700)
	os.WriteFile(old, []byte(`{"omdb_api_key": "fromjson", "language": "ru-RU"}`), 0o600)
	got, exists, err := loadConfig()
	if err != nil || !exists || got.OMDbKey != "fromjson" || got.Language != "ru-RU" {
		t.Fatalf("migration: %+v %v %v", got, exists, err)
	}
	if !exists || !fileExists(old+".old") || fileExists(old) || !fileExists(configPath()) {
		t.Errorf("files after migration: json %v, old %v, yaml %v", fileExists(old), fileExists(old+".old"), fileExists(configPath()))
	}
	if got, _, _ := loadConfig(); got.OMDbKey != "fromjson" {
		t.Errorf("second load: %+v", got)
	}
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestHostName(t *testing.T) {
	if name := hostName(); name == "" || strings.Contains(name, ".") {
		t.Errorf("host name: %q", name)
	}
}

// The settings file can be given on the command line or in a variable; a
// folder means config.yaml in it, and the server's accounts go next to it.
func TestConfigLocation(t *testing.T) {
	setup(t, Config{})
	dir := t.TempDir()
	defer func() { configFile = "" }()

	mustRun(t, "omdbkey\n\n\n", "-config", dir, "-setup")
	// The settings go into the database next to where config.yaml would be.
	if configPath() != filepath.Join(dir, "config.yaml") || !fileExists(filepath.Join(dir, "mediakeeper.db")) {
		t.Fatalf("-config with a folder: %s", configPath())
	}
	store, err := OpenAuth(filepath.Join(dir, "mediakeeper.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	if saved, ok, _ := store.Settings(); !ok || saved.TMDBKey != "omdbkey" { // the first question is TMDB's
		t.Errorf("-setup saved %+v", saved)
	}
	store.Close()
	s, err := NewServer(ServerOptions{Roots: []Root{{Path: t.TempDir()}}, Name: "x", Port: 8200, Config: Config{}}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	s.auth.SetUser("boss", "boss-password", true)
	if !fileExists(filepath.Join(dir, "mediakeeper.db")) {
		t.Errorf("the database is not next to the settings")
	}
	s.dl.Close()

	configFile = ""
	file := filepath.Join(t.TempDir(), "other.yaml")
	t.Setenv("MEDIAKEEPER_CONFIG", file)
	if configPath() != file {
		t.Errorf("MEDIAKEEPER_CONFIG: %s", configPath())
	}
	os.WriteFile(file, []byte("language: de-DE\n"), 0o600)
	if cfg, _, err := loadConfig(); err != nil || cfg.Language != "de-DE" {
		t.Errorf("read from MEDIAKEEPER_CONFIG: %+v %v", cfg, err)
	}
}

// Settings kept in the user's configuration folder by earlier versions move
// to the program's folder, together with the accounts.
func TestConfigMovesToProgramFolder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MEDIAKEEPER_CONFIG", "")
	old := legacyConfigDir()
	os.MkdirAll(old, 0o700)
	os.WriteFile(filepath.Join(old, "config.yaml"), []byte("language: ru-RU\n"), 0o600)
	os.WriteFile(filepath.Join(old, "server.json"), []byte(`{"users":{}}`), 0o600)

	app := t.TempDir()
	appDirOnce.Do(func() {}) // a test binary has no folder to keep settings in: give it one
	appDirPath = app
	defer func() { appDirPath = "" }()

	if got := configLocation(); got != filepath.Join(app, "config.yaml") || !fileExists(filepath.Join(old, "config.yaml")) {
		t.Errorf("only telling where the settings are moved them: %s", got)
	}
	cfg, _, err := loadConfig()
	if err != nil || cfg.Language != "ru-RU" {
		t.Fatalf("after moving: %+v %v", cfg, err)
	}
	for _, name := range []string{"config.yaml", "server.json"} {
		if !fileExists(filepath.Join(app, name)) || fileExists(filepath.Join(old, name)) {
			t.Errorf("%s was not moved", name)
		}
		if st, _ := os.Stat(filepath.Join(app, name)); st != nil && st.Mode().Perm() != 0o600 {
			t.Errorf("%s is readable by others: %v", name, st.Mode())
		}
	}
	if fileExists(old) {
		t.Errorf("the emptied old folder is still there")
	}

	// Settings already in the program's folder are never replaced.
	os.MkdirAll(old, 0o700)
	os.WriteFile(filepath.Join(old, "config.yaml"), []byte("language: en-US\n"), 0o600)
	if cfg, _, _ := loadConfig(); cfg.Language != "ru-RU" || !fileExists(filepath.Join(old, "config.yaml")) {
		t.Errorf("the program's own settings were replaced: %+v", cfg)
	}

	// Without a writable program folder, the old place is used.
	appDirPath = ""
	if got := configPath(); got != filepath.Join(old, "config.yaml") {
		t.Errorf("fallback: %s", got)
	}
}

// -config with a folder that is not there yet: config.yaml and the database
// go into it, and it is made only when something is kept — organizing
// without new keys keeps nothing.
func TestConfigFolderMadeLate(t *testing.T) {
	setup(t, Config{})
	defer func() { configFile = "" }()
	dir := filepath.Join(t.TempDir(), "data", "mediakeeper")
	lib := t.TempDir()
	mustRun(t, "", "-config", dir, "-yes", "-dry-run", lib)
	if configPath() != filepath.Join(dir, "config.yaml") || fileExists(filepath.Dir(dir)) {
		t.Fatalf("config %s; made early: %v", configPath(), fileExists(filepath.Dir(dir)))
	}
	configFile = ""
	mustRun(t, "omdbkey\n\n\n", "-config", dir, "-setup")
	if !fileExists(filepath.Join(dir, "mediakeeper.db")) || fileExists(filepath.Join(dir, "config.yaml")) {
		t.Errorf("after -setup: database there %v, config.yaml there %v", fileExists(filepath.Join(dir, "mediakeeper.db")), fileExists(filepath.Join(dir, "config.yaml")))
	}

	// The Docker image's earlier layout: /config/mediakeeper while it is there.
	configFile = ""
	docker := t.TempDir()
	os.MkdirAll(filepath.Join(docker, "mediakeeper"), 0o755)
	os.WriteFile(filepath.Join(docker, "mediakeeper", "mediakeeper.db"), nil, 0o600)
	if useConfig(docker); configFile != filepath.Join(docker, "mediakeeper", "config.yaml") {
		t.Errorf("earlier layout: %s", configFile)
	}
	os.WriteFile(filepath.Join(docker, "mediakeeper.db"), nil, 0o600)
	if useConfig(docker); configFile != filepath.Join(docker, "config.yaml") {
		t.Errorf("new layout: %s", configFile)
	}
}
