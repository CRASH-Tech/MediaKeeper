package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Folders of movies only and of series only get no Movies and Shows inside;
// every title stays in its folder, and each folder has its own undo.
func TestSeveralLibraryFolders(t *testing.T) {
	root := setup(t, Config{TMDBKey: "tmdbkey", Language: "ru-RU"},
		"films/Iron.Man.2008.BDRip.mkv",
		"tv/Startrek/Star.Trek.Enterprise.s1e01-02.Broken.Bow.mkv",
		"tv/Startrek/Star.Trek.Enterprise.s2e01.Shockwave,Pt.2.mkv",
		"mixed/Iron Man (2008) IMAX.mkv")
	before := tree(t, root)
	films, tv, mixed := filepath.Join(root, "films"), filepath.Join(root, "tv"), filepath.Join(root, "mixed")
	out := mustRun(t, "", "-yes", mixed, "-movies", films, "-shows", tv) // flags may follow the folders
	// The folders given by their paths come first.
	wantAll(t, "output", out, "[1/3] "+mixed+"\n", "[2/3] "+films+" (movies)", "[3/3] "+tv+" (shows)")
	ent := "tv/Звёздный путь - Энтерпрайз (2001)/"
	for _, want := range []string{
		"films/Железный человек (2008)/Железный человек (2008).mkv",
		ent + "tvshow.nfo",
		ent + "Season 01/Звёздный путь - Энтерпрайз S01E01-E02 - Разорванный круг (1) + Разорванный круг (2).mkv",
		"mixed/Movies/Железный человек (2008)/Железный человек (2008).mkv",
	} {
		if !exists(filepath.Join(root, filepath.FromSlash(want))) {
			t.Errorf("missing %s\n  %s", want, strings.Join(tree(t, root), "\n  "))
		}
	}
	for _, dir := range []string{films, tv, mixed} {
		if len(journals(dir)) != 1 {
			t.Errorf("%s: %d journals, want its own one", dir, len(journals(dir)))
		}
	}
	// Reverted folder by folder, the same way.
	mustRun(t, "", "-undo", "-movies", films, "-shows", tv, mixed)
	if after := tree(t, root); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("after -undo:\n  %s\nwant:\n  %s", strings.Join(after, "\n  "), strings.Join(before, "\n  "))
	}

	// The folders of the settings are used when none is given, and a folder
	// given by its path keeps the kind they give it.
	cfg, _, _ := loadConfig()
	cfg.Libraries = []Root{{Path: films, Kind: rootMovies}, {Path: tv, Kind: rootShows}}
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "", "-yes", films)
	if !exists(filepath.Join(films, "Железный человек (2008)", "Железный человек (2008).mkv")) {
		t.Errorf("the kind of the settings was not used:\n  %s", strings.Join(tree(t, root), "\n  "))
	}
	out = mustRun(t, "", "-yes")
	wantAll(t, "output", out, "[2/2] "+tv+" (shows)")
	if !exists(filepath.Join(root, filepath.FromSlash(ent+"tvshow.nfo"))) || exists(filepath.Join(mixed, "Movies")) {
		t.Errorf("the folders of the settings:\n  %s", strings.Join(tree(t, root), "\n  "))
	}
}

// In a folder of movies, a name that looks like an episode is a movie.
func TestMoviesFolderHasNoEpisodes(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "Doctor.Who.S01E01.mkv"), []byte("x"), 0o644)
	files, _ := scanRoot(Root{Path: root, Kind: rootMovies})
	if len(files) != 1 || files[0].Guess.IsSeries {
		t.Errorf("in a folder of movies: %+v", files[0].Guess)
	}
	files, _ = scanRoot(Root{Path: root})
	if !files[0].Guess.IsSeries {
		t.Errorf("in a folder of both: %+v", files[0].Guess)
	}
}

func TestLibrariesInSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	write := func(text string) error {
		os.MkdirAll(filepath.Dir(configPath()), 0o700)
		os.WriteFile(configPath(), []byte(text), 0o600)
		_, _, err := loadConfig()
		return err
	}
	if err := write("libraries:\n  - /srv/media\n  - path: /srv/films\n    kind: Films\n  - {path: /srv/tv, kind: series}\n"); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := loadConfig()
	want := []Root{{Path: "/srv/media", Kind: rootMixed}, {Path: "/srv/films", Kind: rootMovies}, {Path: "/srv/tv", Kind: rootShows}}
	if len(cfg.Libraries) != 3 || cfg.Libraries[0] != want[0] || cfg.Libraries[1] != want[1] || cfg.Libraries[2] != want[2] {
		t.Fatalf("libraries: %+v", cfg.Libraries)
	}
	// Written back as they were, with the rest of the settings.
	cfg.OMDbKey = "key"
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	again, _, err := loadConfig()
	if err != nil || len(again.Libraries) != 3 || again.Libraries[2] != want[2] || again.OMDbKey != "key" {
		t.Errorf("after saving: %+v %v\n%s", again.Libraries, err, mustRead(t, configPath()))
	}
	for text, problem := range map[string]string{
		"libraries:\n  - path: /a\n    kind: music\n": "unknown kind",
		"libraries:\n  - path: /a\n    knd: movies\n": `unknown setting "knd"`,
		"libraries:\n  - kind: movies\n":              "without a path",
	} {
		if err := write(text); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%q: %v, want %q", text, err, problem)
		}
	}
}

func TestCheckRoots(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	if _, err := checkRoots([]Root{{Path: filepath.Join(dir, "a")}, {Path: filepath.Join(dir, "a", "b")}}); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Errorf("nested folders: %v", err)
	}
	if _, err := checkRoots([]Root{{Path: filepath.Join(dir, "missing")}}); err == nil {
		t.Errorf("a missing folder accepted")
	}
}

// The server shows every folder; a title is changed within its own folder,
// and new titles go to the folder of their kind.
func TestServerWithSeveralFolders(t *testing.T) {
	setup(t, Config{TMDBKey: "tmdbkey", Language: "ru-RU"})
	first := dlnaLibraryFiles(t)
	films, tv := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(films, "Iron.Man.2008.BDRip.mkv"), []byte("film"), 0o644)
	os.MkdirAll(filepath.Join(tv, "Doctor Who"), 0o755)
	os.WriteFile(filepath.Join(tv, "Doctor Who", "Doctor.Who.S01E01.mkv"), []byte("ep"), 0o644)
	cfg, _, _ := loadConfig()
	roots := []Root{{Path: first}, {Path: films, Kind: rootMovies}, {Path: tv, Kind: rootShows}}
	s, err := NewServer(ServerOptions{Roots: roots, Name: "Test", Port: 8200, DLNA: true, NoTags: true, Config: cfg}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.auth.SetUser("boss", "boss-password", true); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.dl.Close() })
	boss := newBrowser(t, srv, "boss")

	var lib libraryView
	boss.json("/api/library", &lib)
	var iron, other string
	for _, m := range lib.Movies {
		switch m.Title {
		case "Iron Man & Co":
			iron = m.ID
		case "Iron Man":
			other = m.ID
		}
	}
	if len(lib.Movies) != 3 || len(lib.Shows) != 2 || other == "" {
		t.Fatalf("library: %+v", lib)
	}
	// The first folder keeps the identifiers it had alone.
	if iron != catalogID(kindMovie, filepath.Join("Iron Man (2008)", "Iron Man (2008).mkv")) {
		t.Errorf("the first folder's ids changed")
	}
	var item struct{ File string }
	boss.json("/api/item/"+other, &item)
	if item.File != filepath.Join(filepath.Base(films), "Iron.Man.2008.BDRip.mkv") {
		t.Errorf("file: %q", item.File)
	}

	// Fixed in its own folder: no Movies inside a folder of movies, and the
	// undo journal is that folder's.
	if status, body := boss.post("/api/fix/"+other, resolveRequest{Ref: "tmdb:1726"}); status != 200 {
		t.Fatalf("fix: %d %s", status, body)
	}
	if !exists(filepath.Join(films, "Железный человек (2008)", "Железный человек (2008).mkv")) || len(journals(films)) != 1 || len(journals(first)) != 0 {
		t.Errorf("fixed in place:\n  %s", strings.Join(tree(t, films), "\n  "))
	}

	// New titles go to the folder of their kind.
	a := &App{outSet: true, outRoots: s.libRoots()}
	if got := a.category("", moviesFolder); got != films {
		t.Errorf("a new movie goes to %s", got)
	}
	if got := a.category("", showsFolder); got != tv {
		t.Errorf("a new series goes to %s", got)
	}
	a.outRoots = roots[:2]
	if got := a.category("", showsFolder); got != filepath.Join(first, showsFolder) {
		t.Errorf("with no folder of series, a new series goes to %s", got)
	}

	// DLNA lists the files of each folder under its name.
	dl, err := buildLibrary(roots)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range dl.nodes[dlnaFoldersID].Children {
		names = append(names, n.Title)
	}
	if len(names) != 3 || len(dl.nodes[dlnaMoviesID].Children) != 3 {
		t.Errorf("DLNA folders: %v", names)
	}
}
