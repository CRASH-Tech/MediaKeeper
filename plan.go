package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

type Move struct{ Src, Dst string }

type Doc struct {
	Path string
	Data []byte
}

type Image struct{ URL, Dst string }

// Item is everything to be done for one media file, or (with an empty
// Move) for a series folder as a whole.
type Item struct {
	Move     Move
	From     string // where the file is before the run, if not Move.Src
	IsDir    bool   // Move relocates the title's whole folder
	Sidecars []Move
	Docs     []Doc
	Images   []Image
	Tags     *TagInfo
	Conflict string
}

func (it *Item) addImage(url, dst string) {
	if url != "" {
		it.Images = append(it.Images, Image{url, dst})
	}
}

type Plan struct {
	Items []*Item
	taken map[string]string // destination -> source
}

var reBadChars = regexp.MustCompile(`[*?"<>|]`)

// sanitize makes a title safe as a file name on any file system.
func sanitize(s string) string {
	s = strings.ReplaceAll(s, ": ", " - ")
	s = strings.NewReplacer(":", "-", "/", "-", `\`, "-").Replace(s)
	s = reBadChars.ReplaceAllString(s, "")
	s = reSpaces.ReplaceAllString(s, " ")
	return strings.Trim(s, " .")
}

func withYear(title string, year int) string {
	name := sanitize(title)
	if year > 0 {
		name += fmt.Sprintf(" (%d)", year)
	}
	return name
}

func (p *Plan) add(it *Item) {
	if p.taken == nil {
		p.taken = map[string]string{}
	}
	if it.Move.Src != "" && !it.IsDir {
		if other, ok := p.taken[it.Move.Dst]; ok {
			it.Conflict = "the same name is already taken by " + filepath.Base(other)
		} else {
			p.taken[it.Move.Dst] = it.Move.Src
		}
	}
	p.Items = append(p.Items, it)
}

func sidecarMoves(f *MediaFile, dstDir, newStem string) []Move {
	oldStem := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
	var out []Move
	for _, sc := range f.Sidecars {
		suffix := strings.TrimPrefix(filepath.Base(sc), oldStem)
		out = append(out, Move{sc, filepath.Join(dstDir, newStem+suffix)})
	}
	return out
}

// Movies and series live in separate folders, as media centers want them
// in separate libraries.
const (
	moviesFolder = "Movies"
	showsFolder  = "Shows"
)

// library returns the directory the title belongs to and, if the title
// already has a folder holding nothing else, that folder. Without -out a
// title stays where it was found: next to its file, or in place of its own
// folder. So "media/Startrek/*.mkv" ends up under "media" whether "media"
// or its parent is scanned, and nothing is pulled up to the scanned
// directory.
func (a *App) library(files []*MediaFile) (base, own string) {
	if a.outSet {
		return a.out, ""
	}
	mine := map[string]bool{}
	dir := filepath.Dir(files[0].Path)
	for _, f := range files {
		mine[f.Path] = true
		for d := filepath.Dir(f.Path); !within(dir, d); {
			dir = filepath.Dir(dir) // up to the common parent of all episodes
		}
	}
	if dir != a.root && reSeasonDir.MatchString(filepath.Base(dir)) {
		dir = filepath.Dir(dir)
	}
	if dir == a.root {
		return dir, ""
	}
	// A folder holding nothing but this title is the title's own folder.
	for _, f := range a.files {
		if !mine[f.Path] && within(dir, filepath.Dir(f.Path)) {
			return dir, ""
		}
	}
	return filepath.Dir(dir), dir
}

// category returns the "Movies" or "Shows" folder for a title found in
// base. A title already inside one of them is not nested deeper: it stays,
// or goes to the sibling folder if it was filed under the wrong one. A
// library folder of a single kind has no such folders: the title stays in
// base. New titles (-out, downloads) go to the library folder of their kind.
func (a *App) category(base, name string) string {
	if a.outSet { // the library folder for its kind
		r := rootFor(a.outRoots, name)
		if r.Kind != rootMixed {
			return r.Path
		}
		return filepath.Join(r.Path, name)
	}
	if a.kind != rootMixed { // a folder of movies only, or series only, needs no Movies and Shows
		return base
	}
	switch current := filepath.Base(base); {
	case strings.EqualFold(current, name):
		return base
	case base != a.root && (strings.EqualFold(current, moviesFolder) || strings.EqualFold(current, showsFolder)):
		return filepath.Join(filepath.Dir(base), name)
	}
	return filepath.Join(base, name)
}

// relocate plans moving the title's own folder to dir as a whole, so that
// everything in it (artwork, extras) comes along, and returns the files as
// they will be found there. If the folder is already in place, or dir is
// occupied, the files are returned as they are and move one by one.
func (p *Plan) relocate(own, dir string, files []*MediaFile) []*MediaFile {
	if p.taken == nil {
		p.taken = map[string]string{}
	}
	if _, planned := p.taken[dir]; own == "" || own == dir || planned || exists(dir) {
		return files
	}
	p.taken[dir] = own
	p.Items = append(p.Items, &Item{IsDir: true, Move: Move{own, dir}})
	rebase := func(path string) string {
		rel, _ := filepath.Rel(own, path)
		return filepath.Join(dir, rel)
	}
	moved := make([]*MediaFile, len(files))
	for i, f := range files {
		c := *f
		c.Path = rebase(f.Path)
		c.Sidecars = nil
		for _, sc := range f.Sidecars {
			c.Sidecars = append(c.Sidecars, rebase(sc))
		}
		moved[i] = &c
	}
	return moved
}

// within reports whether path is dir or lies inside it.
func within(dir, path string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// AddMovie plans "Movies/Title (Year)/Title (Year).ext" with its .nfo and
// artwork.
func (a *App) AddMovie(p *Plan, orig *MediaFile, m *Movie) {
	name := withYear(m.Title, m.Year)
	base, own := a.library([]*MediaFile{orig})
	dir := filepath.Join(a.category(base, moviesFolder), name)
	f := p.relocate(own, dir, []*MediaFile{orig})[0]
	it := &Item{
		From:     orig.Path,
		Move:     Move{f.Path, filepath.Join(dir, name+strings.ToLower(filepath.Ext(f.Path)))},
		Sidecars: sidecarMoves(f, dir, name),
		Docs:     []Doc{{filepath.Join(dir, name+".nfo"), MovieNFO(m)}},
		Tags: &TagInfo{Title: m.Title, Date: m.Released, Description: m.Overview,
			Genres: m.Genres, IMDb: m.IDs["imdb"], TMDB: m.IDs["tmdb"]},
	}
	if it.Tags.Date == "" && m.Year > 0 {
		it.Tags.Date = itoa(m.Year)
	}
	it.addImage(m.Poster, filepath.Join(dir, "poster.jpg"))
	it.addImage(m.Backdrop, filepath.Join(dir, "backdrop.jpg"))
	p.add(it)
}

// AddShow plans "Shows/Show (Year)/Season NN/Show SNNENN - Title.ext" for
// every file of the unit, plus tvshow.nfo and artwork for the series itself.
func (a *App) AddShow(p *Plan, u *Unit, s *Show) error {
	planned := len(p.Items)
	showName := sanitize(s.Title)
	showDir, own := a.showPlace(u.Files, s)
	files := p.relocate(own, showDir, u.Files)

	root := &Item{}
	// A new episode of a series that is already described does not rewrite
	// the series' description.
	if tvshow := filepath.Join(showDir, "tvshow.nfo"); !a.keepDescribed || !exists(tvshow) {
		root.Docs = []Doc{{tvshow, ShowNFO(s)}}
	}
	root.addImage(s.Poster, filepath.Join(showDir, "poster.jpg"))
	root.addImage(s.Backdrop, filepath.Join(showDir, "backdrop.jpg"))
	p.add(root)

	seasons := map[int]*Season{}
	for i, f := range files {
		g := f.Guess
		season, known := seasons[g.Season]
		if !known {
			var err error
			season, err = a.season(s, g.Season)
			if err != nil {
				p.Items = p.Items[:planned]
				delete(p.taken, showDir)
				return err
			}
			seasons[g.Season] = season
			poster := s.SeasonPosters[g.Season]
			if season != nil && season.Poster != "" {
				poster = season.Poster
			}
			root.addImage(poster, filepath.Join(showDir, fmt.Sprintf("season%02d-poster.jpg", g.Season)))
		}

		eps := make([]*Episode, len(g.Episodes))
		var titles []string
		for i, n := range g.Episodes {
			eps[i] = season.Episode(n)
			if eps[i] != nil && eps[i].Title != "" && (len(titles) == 0 || titles[len(titles)-1] != eps[i].Title) {
				titles = append(titles, eps[i].Title)
			}
		}

		stem := fmt.Sprintf("%s S%02dE%02d", showName, g.Season, g.Episodes[0])
		if n := len(g.Episodes); n > 1 {
			stem += fmt.Sprintf("-E%02d", g.Episodes[n-1])
		}
		title := strings.Join(titles, " + ")
		if title != "" {
			stem += " - " + sanitize(title)
		}
		dir := filepath.Join(showDir, fmt.Sprintf("Season %02d", g.Season))

		it := &Item{
			From:     u.Files[i].Path,
			Move:     Move{f.Path, filepath.Join(dir, stem+strings.ToLower(filepath.Ext(f.Path)))},
			Sidecars: sidecarMoves(f, dir, stem),
			Docs:     []Doc{{filepath.Join(dir, stem+".nfo"), EpisodeNFO(s, g.Season, g.Episodes, eps)}},
			Tags: &TagInfo{Title: title, Genres: s.Genres, IMDb: s.IDs["imdb"], TMDB: s.IDs["tmdb"],
				Show: s.Title, Season: g.Season, Episode: g.Episodes[0]},
		}
		if it.Tags.Title == "" {
			it.Tags.Title = stem
		}
		if e := eps[0]; e != nil {
			it.Tags.Date, it.Tags.Description = e.Aired, e.Overview
			it.addImage(e.Thumb, filepath.Join(dir, stem+"-thumb.jpg"))
		}
		p.add(it)
	}
	return nil
}

// showPlace is the folder a series is filed in, and the folder it has now
// if that one holds nothing else.
func (a *App) showPlace(files []*MediaFile, s *Show) (dir, own string) {
	base, own := a.library(files)
	return filepath.Join(a.category(base, showsFolder), withYear(s.Title, s.Year)), own
}

// season returns nil without an error when the source simply has no such
// season: the files are still renamed, only without episode titles.
func (a *App) season(s *Show, n int) (*Season, error) {
	p := a.hub.Get(s.Source)
	if p == nil {
		return nil, fmt.Errorf("source %s became unavailable", s.Source)
	}
	season, err := p.Season(s.ID, n)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	return season, a.hub.check(p, err)
}

func (a *App) rel(base, path string) string {
	if r, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

func (a *App) PrintPlan(p *Plan) (moves int) {
	ui := a.ui
	ui.Printf("\n%s\n", ui.Bold("Plan:"))
	docs, images := 0, 0
	for _, it := range p.Items {
		docs += len(it.Docs)
		images += len(it.Images)
		if it.Move.Src == "" || it.IsDir {
			continue
		}
		ui.Printf("  %s\n", a.rel(a.root, it.From))
		switch {
		case it.Conflict != "":
			ui.Printf("    %s\n", ui.Red("✗ "+it.Conflict))
		case it.From == it.Move.Dst:
			ui.Printf("    %s\n", ui.Dim("= already in place"))
		default:
			moves++
			ui.Printf("    %s %s\n", ui.Green("→"), a.rel(a.out, it.Move.Dst))
		}
	}
	ui.Printf("\nRenames: %d, .nfo files: %d, images: %d\n", moves, docs, images)
	return moves
}

// Apply executes the plan. Problems with a single file are reported and do
// not stop the rest. Every change goes into the journal for -undo.
func (a *App) Apply(p *Plan) {
	ui := a.ui
	j := NewJournal(a.root)
	if a.noJournal {
		j.path = ""
	}
	srcDirs := map[string]bool{}
	noTool := false
	warn := func(err error) {
		if err != nil {
			ui.Printf("  %s\n", ui.Yellow("! "+err.Error()))
		}
	}
	for _, it := range p.Items {
		if it.Conflict != "" {
			continue
		}
		if it.IsDir {
			if err := moveFile(j, it.Move); err != nil {
				ui.Printf("%s\n  %s\n", a.rel(a.out, it.Move.Dst), ui.Red("✗ "+err.Error()))
			}
			continue
		}
		if it.Move.Src != "" {
			ui.Printf("%s\n", a.rel(a.out, it.Move.Dst))
			if err := moveFile(j, it.Move); err != nil {
				ui.Printf("  %s\n", ui.Red("✗ "+err.Error()))
				continue
			}
			srcDirs[filepath.Dir(it.Move.Src)] = true
			for _, sc := range it.Sidecars {
				warn(moveFile(j, sc))
			}
		}
		for _, d := range it.Docs {
			warn(j.WriteFile(d.Path, d.Data))
		}
		for _, img := range it.Images {
			if _, err := os.Stat(img.Dst); err == nil {
				continue
			}
			if err := DownloadFile(img.URL, img.Dst); err != nil {
				warn(fmt.Errorf("%s: %v", filepath.Base(img.Dst), err))
				continue
			}
			j.Created = append(j.Created, img.Dst)
		}
		if it.Tags != nil && !a.noTags && !noTool {
			switch err := WriteTags(it.Move.Dst, it.Tags); {
			case errors.Is(err, errNoTagTool):
				noTool = true
			case err != nil:
				warn(fmt.Errorf("tags: %v", err))
			default:
				j.Tagged = append(j.Tagged, it.Move.Dst)
			}
		}
		warn(j.Save())
	}
	j.RemovedDirs = removeEmptyDirs(a.root, srcDirs)
	warn(j.Save())
	if noTool {
		ui.Printf("\n%s\n", ui.Yellow("Tags were not written into the files: mkvpropedit and ffmpeg are not installed.\n"+
			"Install them (sudo apt install mkvtoolnix ffmpeg) and run the program again."))
	}
	if !j.empty() && !a.noJournal {
		ui.Printf("\n%s\n", ui.Dim("To revert everything: mediakeeper -undo "+a.root))
	}
}

// moveFile renames without ever overwriting an existing file. With a
// journal, the move and the directories it needed are recorded.
func moveFile(j *Journal, m Move) error {
	if m.Src == m.Dst {
		return nil
	}
	var err error
	if j != nil {
		err = j.MkdirAll(filepath.Dir(m.Dst))
	} else {
		err = os.MkdirAll(filepath.Dir(m.Dst), 0o755)
	}
	if err != nil {
		return err
	}
	if _, err := os.Lstat(m.Dst); err == nil {
		return fmt.Errorf("already exists: %s", m.Dst)
	}
	err = os.Rename(m.Src, m.Dst)
	if errors.Is(err, syscall.EXDEV) {
		err = copyAcross(m)
	}
	if err == nil && j != nil {
		j.Moves = append(j.Moves, m)
	}
	return err
}

// copyAcross moves a file to another file system.
func copyAcross(m Move) error {
	in, err := os.Open(m.Src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(m.Dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(m.Dst)
		return err
	}
	return os.Remove(m.Src)
}

// removeEmptyDirs deletes directories left empty after the files moved out,
// never touching the root itself, and returns what it deleted.
func removeEmptyDirs(root string, dirs map[string]bool) []string {
	var list, removed []string
	for d := range dirs {
		list = append(list, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(list)))
	for _, d := range list {
		for d != root && strings.HasPrefix(d, root+string(filepath.Separator)) {
			if os.Remove(d) != nil { // not empty
				break
			}
			removed = append(removed, d)
			d = filepath.Dir(d)
		}
	}
	return removed
}

func itoa(n int) string { return strconv.Itoa(n) }
