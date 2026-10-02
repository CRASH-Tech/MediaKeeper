package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The settings live in the database (one JSON document of Config in the
// meta table) and are changed in the web interface. Only where the database
// is cannot be there: -db, MEDIAKEEPER_DB or server.database in config.yaml.
//
// config.yaml, where earlier versions kept everything, is an inbox now:
// settings found in it are taken into the database at the start (over what
// is there), and the file is written again with only the database's place,
// the full one kept as config.yaml.old.
//
// Command-line flags and environment variables still win: the settings page
// shows such a setting as locked, saying what sets it.

const settingsKey = "settings"

// Settings reads the saved settings; ok is false when nothing was saved yet.
func (a *Auth) Settings() (c Config, ok bool, err error) {
	var text string
	switch err = a.conn().QueryRow(`SELECT value FROM meta WHERE key = ?`, settingsKey).Scan(&text); {
	case errors.Is(err, sql.ErrNoRows):
		return c, false, nil
	case err != nil:
		return c, false, err
	}
	return c, true, json.Unmarshal([]byte(text), &c)
}

func (a *Auth) SaveSettings(c Config) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = a.conn().Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, settingsKey, string(data))
	return err
}

// mergeConfig lays the settings set in over on base: what over leaves empty
// stays as in base. Library folders keep the keys they have in base.
func mergeConfig(base, over Config) Config {
	str := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	str(&base.TMDBKey, over.TMDBKey)
	str(&base.OMDbKey, over.OMDbKey)
	str(&base.KinopoiskKey, over.KinopoiskKey)
	str(&base.Language, over.Language)
	str(&base.TMDBURL, over.TMDBURL)
	str(&base.TMDBImageURL, over.TMDBImageURL)
	if len(over.Sources) > 0 {
		base.Sources = over.Sources
	}
	if len(over.Libraries) > 0 {
		base.Libraries, base.LibrariesSet = withKeys(base.Libraries, over.Libraries), true
	}
	o, b := over.Server, &base.Server
	str(&b.Name, o.Name)
	str(&b.Database, o.Database)
	str(&b.Cache, o.Cache)
	str(&b.HWAccel, o.HWAccel)
	if o.Port != 0 {
		b.Port = o.Port
	}
	if o.DLNA != nil {
		b.DLNA = o.DLNA
	}
	if o.Guests != nil {
		b.Guests = o.Guests
	}
	if o.NoTags {
		b.NoTags = true
	}
	return base
}

// withKeys gives new library folders their keys: a folder already in the
// library keeps its own; the first folders of a new library get theirs by
// place, as before keys were kept; any other one its path.
func withKeys(old, roots []Root) []Root {
	keys := map[string]Root{}
	for _, r := range old {
		keys[filepath.Clean(r.Path)] = r
	}
	fresh := len(old) == 0
	out := make([]Root, len(roots))
	for i, r := range roots {
		if known, ok := keys[filepath.Clean(r.Path)]; ok && known.HasKey {
			r.Key, r.HasKey = known.Key, true
		} else if !r.HasKey {
			if fresh {
				r.Key = rootKey(roots, i)
			} else {
				r.Key = r.Path + "\x00"
			}
			r.HasKey = true
		}
		out[i] = r
	}
	return out
}

// firstLibraries saves the library folders a server starts with while none
// were ever chosen: those of the command line or, in Docker, the mounted
// /media. They can be changed under Settings and left out of the command
// then, and the titles keep their identifiers.
func firstLibraries(store *Auth, cfg *Config, cli []Root, log func(string, ...any)) error {
	if cfg.LibrariesSet || len(cfg.Libraries) > 0 {
		return nil
	}
	roots := cli
	if roots == nil && inContainer() {
		roots = []Root{{Path: "/media"}}
	}
	if roots == nil {
		return nil
	}
	cfg.Libraries, cfg.LibrariesSet = withKeys(nil, roots), true
	if err := store.SaveSettings(*cfg); err != nil {
		return err
	}
	var paths []string
	for _, r := range roots {
		paths = append(paths, r.Path)
	}
	log("Library folders saved in the settings: %s. They are changed in the web interface, under Settings.", strings.Join(paths, ", "))
	return nil
}

// fileHasSettings tells whether config.yaml holds more than the place of
// the database.
func fileHasSettings(c Config) bool {
	c.Server.Database = ""
	empty, _ := json.Marshal(Config{})
	got, _ := json.Marshal(c)
	return string(got) != string(empty)
}

// takeSettingsFile moves the settings of config.yaml into the database (see
// the top of this file) and returns the settings now in force there.
func takeSettingsFile(store *Auth, file Config, log func(string, ...any)) (Config, bool, error) {
	saved, ok, err := store.Settings()
	if err != nil {
		return saved, ok, err
	}
	if !fileHasSettings(file) {
		return saved, ok, nil
	}
	merged := mergeConfig(saved, file)
	merged.Server.Database = ""
	if err := store.SaveSettings(merged); err != nil {
		return saved, ok, err
	}
	path := configPath()
	if data, err := os.ReadFile(path); err == nil {
		os.WriteFile(path+".old", data, 0o600)
	}
	if err := writeBootstrapConfig(path, file.Server.Database); err != nil {
		return merged, true, err
	}
	log("The settings of %s are in the database now (the file is kept as config.yaml.old); they are changed in the web interface, under Settings.", path)
	return merged, true, nil
}

// writeBootstrapConfig writes config.yaml with nothing but the place of the
// database, and what the file is for now.
func writeBootstrapConfig(path, database string) error {
	var b strings.Builder
	b.WriteString("# MediaKeeper keeps its settings in its database (mediakeeper.db) and changes\n" +
		"# them in the web interface, under Settings, or with mediakeeper -setup.\n" +
		"#\n" +
		"# Settings put into this file (as in config.yaml.old, if there is one) are\n" +
		"# taken into the database at the next start, and the file is cleared again.\n" +
		"# Only where the database is stays here; -db and MEDIAKEEPER_DB do the same.\n")
	if database != "" {
		fmt.Fprintf(&b, "server:\n  database: %s\n", yamlValue(database))
	} else {
		b.WriteString("# server:\n#   database: /var/lib/mediakeeper/mediakeeper.db\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// withDefaults fills in what is not set.
func withDefaults(c Config) Config {
	if c.Language == "" {
		c.Language = "en-US"
	}
	if c.Server.Name == "" {
		c.Server.Name = hostName()
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8200
	}
	yes := true
	if c.Server.DLNA == nil {
		c.Server.DLNA = &yes
	}
	if c.Server.Guests == nil {
		c.Server.Guests = &yes
	}
	return c
}

// ---------------------------------------------------------------- server

// liveSettings are the settings the server works by; they change while it
// runs, when an administrator saves new ones.
type liveSettings struct {
	mu       sync.RWMutex
	cfg      Config            // in force: saved settings, defaults, flags
	locked   map[string]string // setting -> what sets it (a flag, a variable)
	roots    []Root            // the library folders that exist
	cacheDir string
	hw       *hwAccel
	dataDir  string // where the database is: the base of relative paths
}

func (s *Server) config() Config {
	s.live.mu.RLock()
	defer s.live.mu.RUnlock()
	return s.live.cfg
}

func (s *Server) libRoots() []Root {
	s.live.mu.RLock()
	defer s.live.mu.RUnlock()
	return append([]Root(nil), s.live.roots...)
}

// firstRoot is the first library folder: downloads in progress are kept in
// it. "" while there is none.
func (s *Server) firstRoot() string {
	if roots := s.libRoots(); len(roots) > 0 {
		return roots[0].Path
	}
	return ""
}

func (s *Server) serverName() string { return s.config().Server.Name }

func (s *Server) guestsOn() bool { c := s.config(); return c.Server.Guests == nil || *c.Server.Guests }

func (s *Server) tagsOff() bool { return s.config().Server.NoTags }

func (s *Server) cacheRoot() string {
	s.live.mu.RLock()
	defer s.live.mu.RUnlock()
	return s.live.cacheDir
}

func (s *Server) card() *hwAccel {
	s.live.mu.RLock()
	defer s.live.mu.RUnlock()
	return s.live.hw
}

// existingRoots drops library folders that are not there (a disk not
// mounted), saying so: the server works with the others.
func existingRoots(roots []Root, log func(string, ...any)) []Root {
	var out []Root
	for _, r := range roots {
		if st, err := os.Stat(r.Path); err != nil || !st.IsDir() {
			log("library folder %s is not there: left out until it is", r.Path)
			continue
		}
		out = append(out, r)
	}
	return out
}

// resolveCache is the folder of generated images for a setting: a path
// (relative to the database's folder), else .cache in the first library
// folder, else "cache" next to the database.
func (s *Server) resolveCache(setting string, roots []Root) string {
	switch {
	case setting != "" && filepath.IsAbs(expandHome(setting)):
		return expandHome(setting)
	case setting != "":
		return filepath.Join(s.live.dataDir, setting)
	case len(roots) > 0:
		return filepath.Join(roots[0].Path, ".cache")
	}
	return filepath.Join(s.live.dataDir, "cache")
}

// applySettings puts saved settings into force, except those a flag or a
// variable sets. It says what takes a restart.
func (s *Server) applySettings(saved Config) (restart []string) {
	saved = withDefaults(saved)
	s.live.mu.Lock()
	cfg, locked := s.live.cfg, s.live.locked
	take := func(key string, apply func()) {
		if locked[key] == "" {
			apply()
		}
	}
	take("tmdb_api_key", func() { cfg.TMDBKey = saved.TMDBKey })
	take("omdb_api_key", func() { cfg.OMDbKey = saved.OMDbKey })
	take("kinopoisk_api_key", func() { cfg.KinopoiskKey = saved.KinopoiskKey })
	take("language", func() { cfg.Language = saved.Language })
	take("sources", func() { cfg.Sources = saved.Sources })
	cfg.TMDBURL, cfg.TMDBImageURL = saved.TMDBURL, saved.TMDBImageURL
	take("libraries", func() { cfg.Libraries = saved.Libraries })
	take("server.name", func() { cfg.Server.Name = saved.Server.Name })
	take("server.port", func() { cfg.Server.Port = saved.Server.Port })
	take("server.dlna", func() { cfg.Server.DLNA = saved.Server.DLNA })
	take("server.guests", func() { cfg.Server.Guests = saved.Server.Guests })
	take("server.no_tags", func() { cfg.Server.NoTags = saved.Server.NoTags })
	take("server.cache", func() { cfg.Server.Cache = saved.Server.Cache })
	oldCard := cfg.Server.HWAccel
	take("server.hwaccel", func() { cfg.Server.HWAccel = saved.Server.HWAccel })
	s.live.cfg = cfg
	s.live.mu.Unlock()

	roots := existingRoots(cfg.Libraries, s.log)
	if locked["libraries"] != "" {
		roots = s.libRoots()
	}
	s.setRoots(roots, cfg.Server.Cache)
	if s.dlna != nil {
		s.dlna.setName(cfg.Server.Name)
	}
	if cfg.Server.HWAccel != oldCard {
		card := detectHW(s.ffmpeg, cfg.Server.HWAccel, s.log)
		s.live.mu.Lock()
		s.live.hw = card
		s.live.mu.Unlock()
	}
	if cfg.Server.Port != s.port {
		restart = append(restart, "the port")
	}
	if dlna := cfg.Server.DLNA == nil || *cfg.Server.DLNA; dlna != (s.dlna != nil) {
		restart = append(restart, "DLNA")
	}
	return restart
}

// setRoots changes the library folders the server shows.
func (s *Server) setRoots(roots []Root, cache string) {
	s.live.mu.Lock()
	s.live.roots = roots
	s.live.cacheDir = s.resolveCache(cache, roots)
	s.live.mu.Unlock()
	s.lib.setRoots(roots)
	if s.dlna != nil {
		s.dlna.setRoots(roots)
	}
	s.refresh()
}

// ------------------------------------------------------------------- API

// settingsView is what the settings page gets: the settings in force, API
// keys only as whether they are set (and their end), what is locked and why.
func (s *Server) settingsView() map[string]any {
	c := s.config()
	key := func(k string) map[string]any {
		if k == "" {
			return map[string]any{"set": false}
		}
		end := k
		if len(end) > 4 {
			end = end[len(end)-4:]
		}
		return map[string]any{"set": true, "end": end}
	}
	s.live.mu.RLock()
	locked := s.live.locked
	s.live.mu.RUnlock()
	var sources []map[string]any
	for _, name := range defaultSources {
		need := map[string]bool{"tmdb": true, "omdb": true, "kinopoisk": true}[name]
		sources = append(sources, map[string]any{"key": name, "name": sourceTitles[name], "needsKey": need})
	}
	order := c.Sources
	if len(order) == 0 {
		order = defaultSources
	}
	roots := []map[string]any{}
	for _, r := range c.Libraries {
		_, err := os.Stat(r.Path)
		roots = append(roots, map[string]any{"path": r.Path, "kind": r.Kind, "missing": err != nil})
	}
	hw := "the processor"
	if card := s.card(); card != nil {
		hw = card.method
		if card.device != "" {
			hw += " on " + card.device
		}
	}
	return map[string]any{
		"tmdbKey": key(c.TMDBKey), "omdbKey": key(c.OMDbKey), "kinopoiskKey": key(c.KinopoiskKey),
		"language": c.Language, "sources": order, "allSources": sources,
		"tmdbUrl": c.TMDBURL, "tmdbImageUrl": c.TMDBImageURL,
		"libraries": roots,
		"name":      c.Server.Name, "port": c.Server.Port, "dlna": c.Server.DLNA == nil || *c.Server.DLNA,
		"guests": c.Server.Guests == nil || *c.Server.Guests, "tags": !c.Server.NoTags,
		"cache": c.Server.Cache, "cacheDir": s.cacheRoot(), "hwaccel": c.Server.HWAccel, "hwInUse": hw,
		"locked": locked, "database": s.auth.Path(), "container": inContainer(), "configDir": filepath.Dir(configPath()),
	}
}

var sourceTitles = map[string]string{"tmdb": "TMDB", "tvmaze": "TVMaze", "omdb": "OMDb", "kinopoisk": "Kinopoisk",
	"wikidata": "Wikidata", "imdb": "IMDb", "letterboxd": "Letterboxd"}

// settingsChange is a change sent by the settings page; what is missing
// stays as it is. An API key sent as "" is removed.
type settingsChange struct {
	TMDBKey      *string  `json:"tmdbKey"`
	OMDbKey      *string  `json:"omdbKey"`
	KinopoiskKey *string  `json:"kinopoiskKey"`
	Language     *string  `json:"language"`
	Sources      []string `json:"sources"`
	TMDBURL      *string  `json:"tmdbUrl"`
	TMDBImageURL *string  `json:"tmdbImageUrl"`
	Libraries    *[]Root  `json:"libraries"`
	Name         *string  `json:"name"`
	Port         *int     `json:"port"`
	DLNA         *bool    `json:"dlna"`
	Guests       *bool    `json:"guests"`
	Tags         *bool    `json:"tags"`
	Cache        *string  `json:"cache"`
	HWAccel      *string  `json:"hwaccel"`
	Database     *string  `json:"database"` // where to move the database: kept in config.yaml, not in it
}

// fields names what a change sets, for the log (API keys without their
// values).
func (ch settingsChange) fields() []string {
	var out []string
	add := func(set bool, name string) {
		if set {
			out = append(out, name)
		}
	}
	add(ch.TMDBKey != nil, "TMDB key")
	add(ch.OMDbKey != nil, "OMDb key")
	add(ch.KinopoiskKey != nil, "Kinopoisk key")
	if ch.Language != nil {
		out = append(out, "language "+*ch.Language)
	}
	add(ch.Sources != nil, "sources "+strings.Join(ch.Sources, ","))
	add(ch.TMDBURL != nil || ch.TMDBImageURL != nil, "TMDB addresses")
	if ch.Libraries != nil {
		var paths []string
		for _, r := range *ch.Libraries {
			paths = append(paths, r.Path)
		}
		out = append(out, "library folders "+strings.Join(paths, ", "))
	}
	if ch.Name != nil {
		out = append(out, "name "+*ch.Name)
	}
	if ch.Port != nil {
		out = append(out, fmt.Sprintf("port %d", *ch.Port))
	}
	if ch.DLNA != nil {
		out = append(out, fmt.Sprintf("DLNA %v", *ch.DLNA))
	}
	if ch.Guests != nil {
		out = append(out, fmt.Sprintf("guests %v", *ch.Guests))
	}
	if ch.Tags != nil {
		out = append(out, fmt.Sprintf("tags %v", *ch.Tags))
	}
	if ch.Cache != nil {
		out = append(out, "cache "+*ch.Cache)
	}
	if ch.HWAccel != nil {
		out = append(out, "hardware conversion "+*ch.HWAccel)
	}
	if ch.Database != nil {
		out = append(out, "database "+*ch.Database)
	}
	if len(out) == 0 {
		out = append(out, "nothing")
	}
	return out
}

var reLanguage = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)

// apply checks a change and lays it over the saved settings.
func (ch settingsChange) apply(c Config) (Config, error) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&c.TMDBKey, ch.TMDBKey)
	set(&c.OMDbKey, ch.OMDbKey)
	set(&c.KinopoiskKey, ch.KinopoiskKey)
	set(&c.TMDBURL, ch.TMDBURL)
	set(&c.TMDBImageURL, ch.TMDBImageURL)
	if ch.Language != nil {
		if l := strings.TrimSpace(*ch.Language); l != "" && !reLanguage.MatchString(l) {
			return c, errors.New("a language looks like en-US or ru-RU")
		}
		c.Language = strings.TrimSpace(*ch.Language)
	}
	if ch.Sources != nil {
		seen := map[string]bool{}
		for _, name := range ch.Sources {
			if sourceTitles[name] == "" || seen[name] {
				return c, fmt.Errorf("unknown or repeated source %q", name)
			}
			seen[name] = true
		}
		if len(ch.Sources) == 0 {
			return c, errors.New("at least one source is needed")
		}
		c.Sources = ch.Sources
	}
	if ch.Libraries != nil {
		roots := *ch.Libraries
		for i := range roots {
			kind, err := parseRootKind(roots[i].Kind)
			if err != nil {
				return c, err
			}
			roots[i].Kind = kind
			roots[i].Path = filepath.Clean(expandHome(strings.TrimSpace(roots[i].Path)))
			if !filepath.IsAbs(roots[i].Path) {
				return c, fmt.Errorf("%s: a library folder is given by its full path", roots[i].Path)
			}
			roots[i].HasKey = false // keys are the server's business
		}
		checked, err := checkRoots(roots)
		if len(roots) > 0 && err != nil {
			return c, err
		}
		if len(roots) == 0 {
			checked = nil
		}
		c.Libraries, c.LibrariesSet = withKeys(c.Libraries, checked), true
	}
	set(&c.Server.Name, ch.Name)
	if ch.Port != nil {
		if *ch.Port < 1 || *ch.Port > 65535 {
			return c, errors.New("a port goes from 1 to 65535")
		}
		c.Server.Port = *ch.Port
	}
	if ch.DLNA != nil {
		c.Server.DLNA = ch.DLNA
	}
	if ch.Guests != nil {
		c.Server.Guests = ch.Guests
	}
	if ch.Tags != nil {
		c.Server.NoTags = !*ch.Tags
	}
	set(&c.Server.Cache, ch.Cache)
	if ch.HWAccel != nil {
		v := strings.ToLower(strings.TrimSpace(*ch.HWAccel))
		method, _, _ := strings.Cut(v, ":")
		if v != "" && method != "none" && method != "auto" && method != "vaapi" && method != "qsv" && method != "nvenc" {
			return c, errors.New("hardware conversion is auto, vaapi, qsv, nvenc or none")
		}
		c.Server.HWAccel = v
	}
	return c, nil
}

// settingsAPI serves /api/settings for administrators: GET the settings,
// POST a change (settingsChange).
func (s *Server) settingsAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.settingsView())
	case http.MethodPost:
		var ch settingsChange
		if err := readJSON(r, &ch); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		restart, err := s.changeSettings(ch)
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		view := s.settingsView()
		view["restart"] = restart
		writeJSON(w, http.StatusOK, view)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// changeSettings saves a change and puts it into force.
func (s *Server) changeSettings(ch settingsChange) ([]string, error) {
	s.changing.Lock() // one change at a time
	defer s.changing.Unlock()
	saved, _, err := s.auth.Settings()
	if err != nil {
		return nil, err
	}
	changed, err := ch.apply(saved)
	if err != nil {
		return nil, err
	}
	// A new place for the database is checked before anything is saved.
	database := ""
	if ch.Database != nil {
		if database, err = s.newDatabasePath(*ch.Database); err != nil {
			return nil, err
		}
	}
	if err := s.auth.SaveSettings(changed); err != nil {
		return nil, err
	}
	restart := s.applySettings(changed)
	s.log("settings changed: %s", strings.Join(ch.fields(), ", "))
	if database != "" {
		if err := s.moveDatabase(database); err != nil {
			return restart, err
		}
	}
	return restart, nil
}

// newDatabasePath checks a place to move the database to: "" when it is
// where it is already. A folder means mediakeeper.db in it.
func (s *Server) newDatabasePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	s.live.mu.RLock()
	locked := s.live.locked["server.database"]
	s.live.mu.RUnlock()
	path = filepath.Clean(expandHome(path))
	if !filepath.IsAbs(path) {
		return "", errors.New("the database is given by its full path")
	}
	if st, err := os.Stat(path); (err == nil && st.IsDir()) || filepath.Ext(path) == "" {
		path = filepath.Join(path, "mediakeeper.db")
	}
	if path == s.auth.Path() && !s.auth.InMemory() {
		return "", nil
	}
	if locked != "" {
		return "", fmt.Errorf("the place of the database is set by %s: change it there", locked)
	}
	return path, canMoveTo(path)
}

// moveDatabase moves the database while the server runs and writes its new
// place into config.yaml, where the next start looks for it.
// (The first time, from memory, it is created there.)
func (s *Server) moveDatabase(path string) error {
	old, created := s.auth.Path(), s.auth.InMemory()
	if err := s.auth.MoveTo(path); err != nil {
		return err
	}
	// Where it is by default, config.yaml need not say anything, unless it
	// says something else.
	file, _, _ := loadConfig()
	if path != filepath.Join(filepath.Dir(configPath()), "mediakeeper.db") || file.Server.Database != "" {
		if err := writeBootstrapConfig(configPath(), path); err != nil {
			return fmt.Errorf("the database is in %s now, but config.yaml could not say so (%v): start the server with -db %s", path, err, path)
		}
	}
	if created {
		s.log("the database is created: %s", path)
	} else {
		s.log("the database moved from %s to %s (the old file is kept as %s.old)", old, path, filepath.Base(old))
	}
	return nil
}

// foldersAPI serves GET /api/settings/folders?path=…: the folders in a
// folder of the server, to choose a library folder from; POST {path, name}
// makes a new folder there and answers with what is in it.
func (s *Server) foldersAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if r.Method == http.MethodPost {
		var req struct{ Path, Name string }
		if err := readJSON(r, &req); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			apiError(w, http.StatusBadRequest, errors.New("a folder name cannot be empty or hold a slash"))
			return
		}
		path = filepath.Join(filepath.Clean(expandHome(req.Path)), name)
		if !filepath.IsAbs(path) {
			apiError(w, http.StatusBadRequest, errors.New("the folder is given by its full path"))
			return
		}
		if err := os.Mkdir(path, 0o755); err != nil {
			apiError(w, http.StatusBadRequest, fmt.Errorf("cannot make the folder: %v", err))
			return
		}
		s.log("folder made: %s", path)
	}
	if path == "" {
		path = "/"
		if home, err := os.UserHomeDir(); err == nil && os.Getenv("MEDIAKEEPER_CONFIG") == "" {
			path = home
		}
		if inContainer() {
			path = "/media" // where the compose file mounts the library
		}
	}
	path = filepath.Clean(expandHome(path))
	// A place not made yet (the suggested folder of the database): the
	// nearest folder above it that is there.
	for st, err := os.Stat(path); err != nil || !st.IsDir(); st, err = os.Stat(path) {
		if filepath.Dir(path) == path {
			break
		}
		path = filepath.Dir(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		apiError(w, http.StatusBadRequest, fmt.Errorf("cannot read %s: %v", path, err))
		return
	}
	folders := []string{}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			folders = append(folders, e.Name())
		} else if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(path, e.Name())); err == nil && st.IsDir() {
				folders = append(folders, e.Name())
			}
		}
	}
	sort.Slice(folders, func(i, j int) bool { return strings.ToLower(folders[i]) < strings.ToLower(folders[j]) })
	writable := true
	if f, err := os.CreateTemp(path, ".mediakeeper-write-test-*"); err != nil {
		writable = false
	} else {
		f.Close()
		os.Remove(f.Name())
	}
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "parent": parent, "folders": folders, "writable": writable})
}

// freshStart tells whether a server starts for the very first time: no
// database, nothing to import, no place for it chosen (chosen: by -db or
// config.yaml) and no administrator given in the environment. Then nothing
// is written before the setup in the browser.
func freshStart(dbPath, legacy string, chosen bool) bool {
	return !chosen && os.Getenv("MEDIAKEEPER_DB") == "" && os.Getenv("MEDIAKEEPER_ADMIN_PASSWORD") == "" &&
		!exists(dbPath) && !exists(legacy)
}

// inContainer: in Docker, /media is the library the compose file mounts
// (elsewhere it is where removable disks appear).
func inContainer() bool {
	_, docker := os.Stat("/.dockerenv")
	st, err := os.Stat("/media")
	return docker == nil && err == nil && st.IsDir()
}

// setupAPI serves the first start, when there is no account yet:
//
//	GET  /api/setup   {needed, name, suggest}
//	POST /api/setup   the administrator, the library folders, the rest
//
// The first one to finish it becomes the administrator; afterwards it is
// closed. (MEDIAKEEPER_ADMIN_PASSWORD makes the account without it.)
func (s *Server) setupAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// The folders to start from: those the server has (from the command
		// line, or saved by firstLibraries), else Docker's /media.
		libraries := []map[string]string{}
		for _, r := range s.config().Libraries {
			libraries = append(libraries, map[string]string{"path": r.Path, "kind": r.Kind})
		}
		if len(libraries) == 0 && inContainer() {
			libraries = append(libraries, map[string]string{"path": "/media", "kind": ""})
		}
		s.live.mu.RLock()
		locked := s.live.locked
		s.live.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{"needed": !s.auth.HasUsers(), "name": s.serverName(),
			"libraries": libraries, "language": s.config().Language,
			"database": s.auth.Path(), "cache": s.config().Server.Cache, "locked": locked, "container": inContainer(),
			"configDir": filepath.Dir(configPath())})
	case http.MethodPost:
		var req struct {
			User     string `json:"user"`
			Password string `json:"password"`
			settingsChange
		}
		if err := readJSON(r, &req); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		s.setupMu.Lock()
		defer s.setupMu.Unlock()
		if s.auth.HasUsers() {
			apiError(w, http.StatusForbidden, errors.New("the server is set up already: sign in"))
			return
		}
		if strings.TrimSpace(req.User) == "" {
			apiError(w, http.StatusBadRequest, errors.New("the administrator needs a name"))
			return
		}
		if len(req.Password) < 4 {
			apiError(w, http.StatusBadRequest, errors.New("the password must be at least 4 characters long"))
			return
		}
		// The settings first: a mistake in them leaves no account behind.
		if _, err := s.changeSettings(req.settingsChange); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		if s.auth.InMemory() { // the place was not changed: the suggested one
			if err := s.moveDatabase(s.auth.Path()); err != nil {
				apiError(w, http.StatusBadRequest, err)
				return
			}
		}
		if _, err := s.auth.SetUser(req.User, req.Password, true); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		u, token, err := s.login(r, req.User, req.Password, "web")
		if err != nil {
			apiError(w, http.StatusInternalServerError, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "mk_token", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 3600})
		s.log("the server is set up; administrator: %s", u.Name)
		writeJSON(w, http.StatusOK, userJSON(u))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
