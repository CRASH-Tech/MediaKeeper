package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Config is the settings file: config.yaml in the program's own folder, or
// the file given with -config or MEDIAKEEPER_CONFIG (see configPath). Every
// field is optional; command-line flags win over it.
type Config struct {
	TMDBKey      string       `yaml:"tmdb_api_key,omitempty" json:"tmdb_api_key,omitempty"`
	KinopoiskKey string       `yaml:"kinopoisk_api_key,omitempty" json:"kinopoisk_api_key,omitempty"`
	OMDbKey      string       `yaml:"omdb_api_key,omitempty" json:"omdb_api_key,omitempty"`
	Language     string       `yaml:"language,omitempty" json:"language,omitempty"`
	Sources      []string     `yaml:"sources,omitempty" json:"sources,omitempty"` // order of priority
	TMDBURL      string       `yaml:"tmdb_api_url,omitempty" json:"tmdb_api_url,omitempty"`
	TMDBImageURL string       `yaml:"tmdb_image_url,omitempty" json:"tmdb_image_url,omitempty"`
	Libraries    []Root       `yaml:"libraries,omitempty" json:"-"` // used when no folder is given on the command line
	Server       ServerConfig `yaml:"server,omitempty" json:"-"`
}

// ServerConfig are the settings of -serve.
type ServerConfig struct {
	Name     string `yaml:"name,omitempty"`
	Database string `yaml:"database,omitempty"` // the SQLite file of accounts and watch states
	Cache    string `yaml:"cache,omitempty"`    // the folder of screenshots and episode stills
	Port     int    `yaml:"port,omitempty"`
	DLNA     *bool  `yaml:"dlna,omitempty"`
	Guests   *bool  `yaml:"guests,omitempty"`
	NoTags   bool   `yaml:"no_tags,omitempty"`
}

// Where the settings live, in this order:
//
//  1. the file given with -config, or in MEDIAKEEPER_CONFIG (a folder means
//     config.yaml in it);
//  2. config.yaml in the folder of the program;
//  3. where the program cannot keep it — a folder it may not write to, like
//     /usr/local/bin, or the temporary build of "go run" — config.yaml in
//     the user's configuration folder (~/.config/mediakeeper), where every
//     version before kept it.
//
// The accounts and the watch progress of the server (server.json) are kept
// next to it. Settings found in the old place are moved to the program's
// folder the first time.

var configFile string // set by useConfig

// useConfig makes path the settings file: -config.
func useConfig(path string) error {
	abs, err := filepath.Abs(expandHome(path))
	if err != nil {
		return err
	}
	if st, err := os.Stat(abs); err == nil && st.IsDir() {
		abs = filepath.Join(abs, "config.yaml")
	}
	configFile = abs
	return nil
}

func configPath() string {
	return findConfig(true)
}

// configLocation is where the settings are or will be, without moving
// anything: for messages.
func configLocation() string {
	return findConfig(false)
}

func findConfig(move bool) string {
	if configFile != "" {
		return configFile
	}
	if env := os.Getenv("MEDIAKEEPER_CONFIG"); env != "" {
		if err := useConfig(env); err == nil {
			return configFile
		}
	}
	legacy := filepath.Join(legacyConfigDir(), "config.yaml")
	app := appDir()
	if app == "" {
		return legacy
	}
	own := filepath.Join(app, "config.yaml")
	if move && !exists(own) && legacyConfigDir() != "" {
		if moved, err := moveConfig(legacyConfigDir(), app); err != nil {
			fmt.Fprintf(os.Stderr, "mediakeeper: the settings stay in %s: %v\n", legacyConfigDir(), err)
			return legacy
		} else if len(moved) > 0 {
			fmt.Fprintf(os.Stderr, "mediakeeper: moved %s from %s to %s\n", strings.Join(moved, ", "), legacyConfigDir(), app)
		}
	}
	return own
}

// chosenPath is a path the user chose: on the command line, else in the
// environment variable — both relative to the working directory — else in
// the settings file, relative to the file's own folder. "" when none chose.
func chosenPath(flag, env, setting string) string {
	base := ""
	path := firstNonEmpty(flag, os.Getenv(env))
	if path == "" && setting != "" {
		path, base = setting, filepath.Dir(configLocation())
	}
	if path == "" {
		return ""
	}
	path = expandHome(path)
	if !filepath.IsAbs(path) && base != "" {
		path = filepath.Join(base, path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path
}

// databasePath is the SQLite file of the server: given with -db, else in
// MEDIAKEEPER_DB, else in the settings, else mediakeeper.db next to the
// settings file. A folder means mediakeeper.db in it.
func databasePath(flag, setting string) string {
	path := chosenPath(flag, "MEDIAKEEPER_DB", setting)
	if path == "" {
		return filepath.Join(filepath.Dir(configPath()), "mediakeeper.db")
	}
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		path = filepath.Join(path, "mediakeeper.db")
	}
	return path
}

// cachePath is the folder of generated images (screenshots, episode
// stills): given with -cache, else in MEDIAKEEPER_CACHE, else in the
// settings, else "" — .cache in the first library folder.
func cachePath(flag, setting string) string {
	return chosenPath(flag, "MEDIAKEEPER_CACHE", setting)
}

// legacyConfigDir is the user's configuration folder for MediaKeeper.
func legacyConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "mediakeeper")
}

var (
	appDirOnce sync.Once
	appDirPath string
)

// appDir is the folder of the program, if the settings can be kept there.
func appDir() string {
	appDirOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		tmp, _ := filepath.EvalSymlinks(os.TempDir())
		if strings.Contains(dir, "go-build") || within(os.TempDir(), dir) || (tmp != "" && within(tmp, dir)) {
			return // a build of "go run" or "go test", gone after the run
		}
		if f, err := os.CreateTemp(dir, ".mediakeeper-*"); err == nil {
			f.Close()
			os.Remove(f.Name())
			appDirPath = dir
		}
	})
	return appDirPath
}

// moveConfig moves the settings and the server's accounts from one folder
// to another: all of them or, if anything fails, none.
func moveConfig(from, to string) (moved []string, err error) {
	var names []string
	for _, name := range []string{"config.yaml", "config.json", "server.json", "mediakeeper.db", "mediakeeper.db-wal", "mediakeeper.db-shm"} {
		if exists(filepath.Join(from, name)) && !exists(filepath.Join(to, name)) {
			names = append(names, name)
		}
	}
	if len(names) == 0 || !exists(filepath.Join(from, "config.yaml")) && !exists(filepath.Join(from, "config.json")) {
		return nil, nil // nothing to move, or only an orphaned server.json
	}
	for i, name := range names { // copied first: a disk of its own is fine too
		if err := copyPrivate(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
			for _, done := range names[:i] {
				os.Remove(filepath.Join(to, done))
			}
			return nil, err
		}
	}
	for _, name := range names {
		os.Remove(filepath.Join(from, name))
	}
	os.Remove(from) // only if it is empty now
	return names, nil
}

// copyPrivate copies a file readable only by its owner: it holds keys.
func copyPrivate(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// loadConfig reads config.yaml. A config.json of an earlier version is
// converted once and kept as config.json.old.
func loadConfig() (cfg Config, exists bool, err error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		old := filepath.Join(filepath.Dir(path), "config.json")
		if data, err := os.ReadFile(old); err == nil {
			if err := json.Unmarshal(data, &cfg); err != nil {
				return cfg, false, fmt.Errorf("%s: %w", old, err)
			}
			if err := saveConfig(cfg); err != nil {
				return cfg, true, err
			}
			return cfg, true, os.Rename(old, old+".old")
		}
		return cfg, false, nil
	}
	if err != nil {
		return cfg, false, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)                                               // a misspelt key is an error, not a silently ignored line
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) { // an empty file is fine
		return cfg, true, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, true, nil
}

// yamlValue writes one value as YAML, quoted where it has to be.
func yamlValue(v any) string {
	if list, ok := v.([]string); ok { // on the key's own line: [a, b]
		items := make([]string, len(list))
		for i, item := range list {
			items[i] = yamlValue(item)
		}
		return "[" + strings.Join(items, ", ") + "]"
	}
	out, _ := yaml.Marshal(v)
	return strings.TrimSpace(string(out))
}

// saveConfig writes the whole file with a comment for every setting, so
// that it can be edited by hand. Settings that are not set are left as
// commented-out examples.
func saveConfig(c Config) error {
	path := configPath()
	if path == "" {
		return errors.New("cannot determine the configuration directory")
	}
	var b strings.Builder
	line := func(comment, key string, value any, set bool, example string) {
		fmt.Fprintf(&b, "\n# %s\n", comment)
		if set {
			fmt.Fprintf(&b, "%s: %s\n", key, yamlValue(value))
		} else {
			fmt.Fprintf(&b, "# %s: %s\n", key, example)
		}
	}
	b.WriteString("# MediaKeeper settings. Every setting is optional; command-line flags win.\n" +
		"#\n# This file is found next to the program unless another one is given with\n" +
		"# -config or the environment variable MEDIAKEEPER_CONFIG (a file, or a folder\n" +
		"# for config.yaml in it). Paths in it may be relative to this file's folder.\n")
	line("TMDB: the richest data, titles in any language. https://www.themoviedb.org/settings/api",
		"tmdb_api_key", c.TMDBKey, c.TMDBKey != "", `""`)
	line("OMDb: IMDb data, in English. https://www.omdbapi.com/apikey.aspx",
		"omdb_api_key", c.OMDbKey, c.OMDbKey != "", `""`)
	line("Kinopoisk: titles and descriptions in Russian. https://kinopoiskapiunofficial.tech",
		"kinopoisk_api_key", c.KinopoiskKey, c.KinopoiskKey != "", `""`)
	line("Language of TMDB titles and descriptions.",
		"language", c.Language, c.Language != "", "ru-RU")
	line("Sources in priority order: tmdb, tvmaze, omdb, kinopoisk, wikidata, imdb, letterboxd.",
		"sources", c.Sources, len(c.Sources) > 0, "[tmdb, tvmaze, omdb, kinopoisk, wikidata, imdb, letterboxd]")
	line("A TMDB mirror, where TMDB itself is not reachable.",
		"tmdb_api_url", c.TMDBURL, c.TMDBURL != "", "https://api.themoviedb.org/3")
	line("Where TMDB images are downloaded from.",
		"tmdb_image_url", c.TMDBImageURL, c.TMDBImageURL != "", "https://image.tmdb.org/t/p/original")

	b.WriteString("\n# The folders of the library, used when none is given on the command line.\n" +
		"# kind: movies or shows; left out, a folder holds both, sorted into Movies\n" +
		"# and Shows inside. A plain path is a folder of both.\n")
	if len(c.Libraries) > 0 {
		b.WriteString("libraries:\n")
		for _, r := range c.Libraries {
			fmt.Fprintf(&b, "  - path: %s\n", yamlValue(r.Path))
			if r.Kind != rootMixed {
				fmt.Fprintf(&b, "    kind: %s\n", r.Kind)
			}
		}
	} else {
		b.WriteString("# libraries:\n#   - path: /srv/movies\n#     kind: movies\n#   - path: /mnt/disk2/films\n#     kind: movies\n" +
			"#   - path: /srv/series\n#     kind: shows\n#   - /srv/media\n")
	}

	b.WriteString("\n# The media server (mediakeeper -serve).\nserver:\n")
	sub := func(comment, key string, value any, set bool, example string) {
		fmt.Fprintf(&b, "  # %s\n", comment)
		if set {
			fmt.Fprintf(&b, "  %s: %s\n", key, yamlValue(value))
		} else {
			fmt.Fprintf(&b, "  # %s: %s\n", key, example)
		}
	}
	s := c.Server
	sub("The name clients show; the host name by default.", "name", s.Name, s.Name != "", "Living room")
	sub("HTTP port for the web interface, the Jellyfin API and DLNA.", "port", s.Port, s.Port != 0, "8200")
	sub("Also be a DLNA server (it has no login: the whole local network can watch).", "dlna", s.DLNA, s.DLNA != nil, "true")
	sub("Let the web interface be watched without signing in.", "guests", s.Guests, s.Guests != nil, "true")
	sub("Do not write tags into the files of downloads.", "no_tags", s.NoTags, s.NoTags, "false")
	sub("The database of accounts, ratings, watchlists and history (a file, or a folder for mediakeeper.db);\n"+
		"  # mediakeeper.db next to this file by default. Also -db, MEDIAKEEPER_DB.",
		"database", s.Database, s.Database != "", "/var/lib/mediakeeper/mediakeeper.db")
	sub("The folder of screenshots and episode stills; .cache in the first library folder by default.\n"+
		"  # Also -cache, MEDIAKEEPER_CACHE. Moving it is harmless: the images are taken again.",
		"cache", s.Cache, s.Cache != "", "/var/cache/mediakeeper")

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Written whole or not at all: the file holds API keys.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// hostName is the default name of the server: what this machine is called.
func hostName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "MediaKeeper"
	}
	// "nas.local", "nas.lan": the first label is the name people use.
	if i := strings.IndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	return name
}
