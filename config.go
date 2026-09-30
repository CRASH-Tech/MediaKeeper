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

	"gopkg.in/yaml.v3"
)

// Config is the settings file, config.yaml in the user's configuration
// directory (~/.config/mediakeeper, /config/mediakeeper in Docker). Every
// field is optional; command-line flags win over it.
type Config struct {
	TMDBKey      string       `yaml:"tmdb_api_key,omitempty" json:"tmdb_api_key,omitempty"`
	KinopoiskKey string       `yaml:"kinopoisk_api_key,omitempty" json:"kinopoisk_api_key,omitempty"`
	OMDbKey      string       `yaml:"omdb_api_key,omitempty" json:"omdb_api_key,omitempty"`
	Language     string       `yaml:"language,omitempty" json:"language,omitempty"`
	Sources      []string     `yaml:"sources,omitempty" json:"sources,omitempty"` // order of priority
	TMDBURL      string       `yaml:"tmdb_api_url,omitempty" json:"tmdb_api_url,omitempty"`
	TMDBImageURL string       `yaml:"tmdb_image_url,omitempty" json:"tmdb_image_url,omitempty"`
	Server       ServerConfig `yaml:"server,omitempty" json:"-"`
}

// ServerConfig are the settings of -serve.
type ServerConfig struct {
	Name   string `yaml:"name,omitempty"`
	Port   int    `yaml:"port,omitempty"`
	DLNA   *bool  `yaml:"dlna,omitempty"`
	Guests *bool  `yaml:"guests,omitempty"`
	NoTags bool   `yaml:"no_tags,omitempty"`
}

func configDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "mediakeeper")
}

func configPath() string {
	if dir := configDir(); dir != "" {
		return filepath.Join(dir, "config.yaml")
	}
	return ""
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
	b.WriteString("# MediaKeeper settings. Every setting is optional; command-line flags win.\n")
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
