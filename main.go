// MediaKeeper turns a folder of movies and series into a library Jellyfin
// understands: it finds each title in online catalogues (TMDB, Kinopoisk,
// TVMaze, OMDb, Wikidata, IMDb, Letterboxd), renames the files, sorts
// episodes into season folders and writes .nfo files, artwork and tags.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The default priority. The first source with a confident match names the
// file; Wikidata and IMDb only find a title and pass it on to the others.
var defaultSources = []string{"tmdb", "tvmaze", "omdb", "kinopoisk", "wikidata", "imdb", "letterboxd"}

var sourceAliases = map[string]string{"kp": "kinopoisk", "lb": "letterboxd"}

// buildProviders creates the configured sources; the ones that need a key
// and have none are returned by name instead.
func buildProviders(cfg Config) (providers []Provider, noKey []string, err error) {
	order := cfg.Sources
	if len(order) == 0 {
		order = defaultSources
	}
	seen := map[string]bool{}
	for _, name := range order {
		name = strings.ToLower(strings.TrimSpace(name))
		if alias, ok := sourceAliases[name]; ok {
			name = alias
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		switch {
		case name == "tmdb" && cfg.TMDBKey != "":
			providers = append(providers, NewTMDB(cfg.TMDBKey, cfg.Language, cfg.TMDBURL, cfg.TMDBImageURL))
		case name == "tmdb":
			noKey = append(noKey, "TMDB")
		case name == "kinopoisk" && cfg.KinopoiskKey != "":
			providers = append(providers, NewKinopoisk(cfg.KinopoiskKey))
		case name == "kinopoisk":
			noKey = append(noKey, "Kinopoisk")
		case name == "omdb" && cfg.OMDbKey != "":
			providers = append(providers, NewOMDb(cfg.OMDbKey))
		case name == "omdb":
			noKey = append(noKey, "OMDb")
		case name == "tvmaze":
			providers = append(providers, NewTVMaze())
		case name == "wikidata":
			providers = append(providers, NewWikidata())
		case name == "imdb":
			providers = append(providers, NewIMDb())
		case name == "letterboxd":
			providers = append(providers, NewLetterboxd())
		default:
			return nil, nil, fmt.Errorf("unknown source %q (known: %s)", name, strings.Join(defaultSources, ", "))
		}
	}
	return providers, noKey, nil
}

type App struct {
	ui       *UI
	hub      *Hub
	root     string // scanned directory
	kind     string // what it holds: rootMovies, rootShows or rootMixed (both)
	out      string // -out, or the scanned directory
	outSet   bool   // -out was given: everything is gathered there
	outRoots []Root // with outSet: the library folders new titles go to, by kind
	files    []*MediaFile

	yes, dryRun, noTags, refresh bool
	keepDescribed                bool // leave videos that have an .nfo alone: not renamed, not looked up, not tagged
	noJournal                    bool // the source folder is temporary: there is nothing to undo into
}

// version is set by the release build (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "mediakeeper:", err)
		os.Exit(1)
	}
}

func run(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("mediakeeper", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprintf(out, "Usage: mediakeeper [options] [directory ...]\n\n"+
			"Finds movies and series in the directories (those of \"libraries\" in the\n"+
			"settings, or the current one, by default; -movies and -shows add folders of\n"+
			"a single kind),\n"+
			"identifies them in online catalogues, renames them the Jellyfin way, writes\n"+
			".nfo files and downloads artwork. With -serve it becomes a media server: a\n"+
			"web interface to browse, watch and download, a Jellyfin-compatible API for\n"+
			"Jellyfin apps, and DLNA for TVs.\n\n"+
			"Sources: tmdb, omdb, kinopoisk (need a key), tvmaze, wikidata, imdb,\n"+
			"letterboxd (no key). Keys: mediakeeper -setup, or the variables TMDB_API_KEY,\n"+
			"OMDB_API_KEY, KINOPOISK_API_KEY. Settings: %s\n\n", configLocation())
		fs.PrintDefaults()
	}
	outDir := fs.String("out", "", "build the library in this directory (default: every title stays in the folder it is in)")
	lang := fs.String("lang", "", "language of TMDB titles and descriptions, e.g. ru-RU (default en-US)")
	sources := fs.String("sources", "", "comma-separated sources in priority order, e.g. omdb,tvmaze,wikidata")
	dryRun := fs.Bool("dry-run", false, "show the plan and change nothing")
	yes := fs.Bool("yes", false, "ask nothing: skip unclear files and apply the plan")
	noTags := fs.Bool("no-tags", false, "do not write tags into the files")
	refresh := fs.Bool("refresh", false, "also redo videos that already have an .nfo, identifying them from scratch (they are left alone otherwise)")
	undo := fs.Bool("undo", false, "revert the last run in this directory (repeat to go further back)")
	setup := fs.Bool("setup", false, "enter API keys and exit")
	serve := fs.Bool("serve", false, "run the server for the directory: web interface, Jellyfin API and DLNA")
	port := fs.Int("port", 8200, "with -serve: HTTP port of the server")
	name := fs.String("name", "", "with -serve: the name clients show (default: the host name)")
	dlna := fs.Bool("dlna", true, "with -serve: also be a DLNA media server (no login, the whole local network can watch)")
	guests := fs.Bool("guests", true, "with -serve: the web interface can be browsed and watched without signing in")
	showVersion := fs.Bool("version", false, "print the version and exit")
	configFlag := fs.String("config", "", "the settings file, or a folder for config.yaml in it (default: next to the program; also MEDIAKEEPER_CONFIG)")
	var given []Root
	fs.Var(rootList{rootMovies, &given}, "movies", "a library folder of movies only; may be repeated")
	fs.Var(rootList{rootShows, &given}, "shows", "a library folder of series only; may be repeated")
	// Flags may also follow the folders: -serve /media -movies /films.
	var folders []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		folders = append(folders, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if *configFlag != "" {
		if err := useConfig(*configFlag); err != nil {
			return err
		}
	}
	cfg, hasConfig, err := loadConfig()
	if err != nil {
		return err
	}
	set := map[string]bool{} // flags given on the command line win over the settings file
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if *showVersion {
		fmt.Fprintln(out, "mediakeeper", version)
		return nil
	}

	ui := NewUI(in, out)
	if *setup {
		return askKeys(ui, &cfg)
	}

	roots, err := libraryRoots(cfg, folders, given)
	if err != nil {
		return err
	}
	if *undo {
		for _, r := range roots {
			if err := Undo(ui, r.Path); err != nil {
				return err
			}
		}
		return nil
	}
	dest := ""
	if *outDir != "" {
		if dest, err = filepath.Abs(*outDir); err != nil {
			return err
		}
	}

	for env, field := range map[string]*string{
		"TMDB_API_KEY": &cfg.TMDBKey, "KINOPOISK_API_KEY": &cfg.KinopoiskKey, "OMDB_API_KEY": &cfg.OMDbKey,
	} {
		if v := os.Getenv(env); v != "" {
			*field = v
		}
	}
	if !hasConfig && !*yes && !*serve && cfg.TMDBKey+cfg.KinopoiskKey+cfg.OMDbKey == "" {
		if err := askKeys(ui, &cfg); err != nil {
			return err
		}
	}
	if *lang != "" {
		cfg.Language = *lang
	}
	if cfg.Language == "" {
		cfg.Language = "en-US"
	}
	if *sources != "" {
		cfg.Sources = strings.Split(*sources, ",")
	}
	providers, noKey, err := buildProviders(cfg)
	if err != nil {
		return err
	}
	if *serve {
		o := ServerOptions{Roots: roots, Name: *name, Port: *port, DLNA: *dlna, Guests: *guests, NoTags: *noTags, Config: cfg}
		sc := cfg.Server
		if !set["name"] && sc.Name != "" {
			o.Name = sc.Name
		}
		if !set["port"] && sc.Port != 0 {
			o.Port = sc.Port
		}
		if !set["dlna"] && sc.DLNA != nil {
			o.DLNA = *sc.DLNA
		}
		if !set["guests"] && sc.Guests != nil {
			o.Guests = *sc.Guests
		}
		if !set["no-tags"] && sc.NoTags {
			o.NoTags = true
		}
		if o.Name == "" {
			o.Name = hostName()
		}
		return Serve(ui, o)
	}
	if len(providers) == 0 {
		return errors.New("no sources at all: check -sources and the keys (mediakeeper -setup)")
	}

	hub := NewHub(providers, func(name, reason string) {
		ui.Printf("%s\n", ui.Dim(fmt.Sprintf("  (source %s is switched off for this run: %s)", name, reason)))
	})

	var names []string
	for _, p := range providers {
		names = append(names, p.Name())
	}
	ui.Printf("Sources: %s\n", strings.Join(names, ", "))
	if len(noKey) > 0 {
		ui.Printf("%s\n", ui.Dim("Not used, no key: "+strings.Join(noKey, ", ")+" (mediakeeper -setup)"))
	}
	for i, r := range roots {
		if len(roots) > 1 {
			ui.Printf("\n%s\n", ui.Bold(fmt.Sprintf("[%d/%d] %s", i+1, len(roots), r)))
		}
		a := &App{ui: ui, hub: hub, root: r.Path, kind: r.Kind, out: r.Path,
			yes: *yes, dryRun: *dryRun, noTags: *noTags, refresh: *refresh, keepDescribed: !*refresh}
		if dest != "" {
			a.out, a.outSet, a.outRoots = dest, true, []Root{{Path: dest}}
		}
		if err := a.Run(); err != nil {
			return err
		}
	}
	return nil
}

// libraryRoots are the folders to work on: those given on the command
// line, else the libraries of the settings, else the current directory. A
// folder given by its path alone keeps the kind the settings give it.
// Folders given by their paths come first, then -movies and -shows: adding
// "-movies /disk2" to "-serve /media" keeps /media the first folder, which
// holds the downloads and keeps the identifiers its titles had.
func libraryRoots(cfg Config, args []string, given []Root) ([]Root, error) {
	var roots []Root
	for _, arg := range args {
		r := Root{Path: arg}
		if abs, err := filepath.Abs(arg); err == nil {
			for _, known := range cfg.Libraries {
				if p, err := filepath.Abs(expandHome(known.Path)); err == nil && p == abs {
					r.Kind = known.Kind
				}
			}
		}
		roots = append(roots, r)
	}
	roots = append(roots, given...)
	if len(roots) == 0 {
		roots = cfg.Libraries
	}
	if len(roots) == 0 {
		roots = []Root{{Path: "."}}
	}
	return checkRoots(roots)
}

// askKeys is the first-run dialog. Every key is optional: three sources
// work without any.
func askKeys(ui *UI, cfg *Config) error {
	ui.Box("API keys", []string{
		"TVMaze, Wikidata, IMDb and Letterboxd need no key. Keys add:",
		"",
		"TMDB      — the richest data, titles in any language (-lang)",
		"            https://www.themoviedb.org/settings/api",
		"OMDb      — IMDb data, in English",
		"            https://www.omdbapi.com/apikey.aspx",
		"Kinopoisk — titles and descriptions in Russian",
		"            https://kinopoiskapiunofficial.tech",
	}, []string{
		"Enter keeps the current value, \"-\" erases the key.",
		"Keys are saved to " + configPath(),
	})
	for _, k := range []struct {
		name  string
		field *string
	}{{"TMDB", &cfg.TMDBKey}, {"OMDb", &cfg.OMDbKey}, {"Kinopoisk", &cfg.KinopoiskKey}} {
		state := "none"
		if *k.field != "" {
			state = "set"
		}
		line, err := ui.ReadLine(fmt.Sprintf("%s key [%s]: ", k.name, state))
		if err != nil {
			break
		}
		switch line {
		case "":
		case "-":
			*k.field = ""
		default:
			*k.field = line
		}
	}
	if err := saveConfig(*cfg); err != nil {
		ui.Printf("%s\n", ui.Yellow("Cannot save the settings: "+err.Error()))
	}
	ui.Printf("\n")
	return nil
}

func (a *App) sourceName(key string) string {
	for _, p := range a.hub.all {
		if p.Key() == key {
			return p.Name()
		}
	}
	return key
}

func (a *App) Run() error {
	ui := a.ui
	files, err := Scan(a.root)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		ui.Printf("No video files in %s.\n", a.root)
		return nil
	}
	a.files = files // all of them: where titles belong is judged against everything on disk
	todo := files
	if a.keepDescribed {
		var described int
		todo, described = withoutNFO(files)
		if described > 0 {
			ui.Printf("%s\n", ui.Dim(fmt.Sprintf("Left alone: %d file(s) that already have an .nfo (-refresh redoes them).", described)))
		}
		if len(todo) == 0 {
			ui.Printf("Every video in %s already has an .nfo: nothing to do.\n", a.root)
			return nil
		}
	}
	units := Group(todo)
	ui.Printf("Files found: %d (movies and series: %d)\n\n", len(todo), len(units))

	plan := &Plan{}
	skipped := 0
	for _, u := range units {
		label := u.Files[0].Rel
		if u.Kind == kindTV {
			label = fmt.Sprintf("%s — %d episode(s)", u.Title, len(u.Files))
		}
		m, err := a.Identify(u)
		if errors.Is(err, errQuit) {
			ui.Printf("%s\n", ui.Yellow("Stopped: the remaining files are left untouched."))
			break
		}
		if err == nil && m != nil {
			a.localize(m)
		}
		if err == nil && m != nil && m.Show != nil {
			err = a.AddShow(plan, u, m.Show)
		}
		switch {
		case err != nil:
			skipped++
			ui.Printf("%s %s: %v\n", ui.Red("✗"), label, err)
		case m == nil:
			skipped++
			ui.Printf("%s %s: skipped\n", ui.Yellow("–"), label)
		case m.Show != nil:
			ui.Printf("%s %s → %s %s\n", ui.Green("✓"), label,
				withYear(m.Show.Title, m.Show.Year), ui.Dim("["+a.sourceName(m.Show.Source)+"]"))
		default:
			a.AddMovie(plan, u.Files[0], m.Movie)
			ui.Printf("%s %s → %s %s\n", ui.Green("✓"), label,
				withYear(m.Movie.Title, m.Movie.Year), ui.Dim("["+a.sourceName(m.Movie.Source)+"]"))
		}
	}
	if len(plan.Items) == 0 {
		ui.Printf("\nNothing to do.\n")
		return nil
	}

	a.PrintPlan(plan)
	if a.dryRun {
		ui.Printf("Dry run: nothing was changed.\n")
		return nil
	}
	if !a.yes && !ui.Confirm("\nApply?") {
		ui.Printf("Cancelled, nothing was changed.\n")
		return nil
	}
	ui.Printf("\n")
	a.Apply(plan)
	ui.Printf("\nDone.")
	if skipped > 0 {
		ui.Printf(" Skipped: %d.", skipped)
	}
	ui.Printf("\n")
	return nil
}
