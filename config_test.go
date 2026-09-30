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
	old := filepath.Join(configDir(), "config.json")
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
