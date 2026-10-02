package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var errQuit = errors.New("interrupted by the user")

// autoPick accepts a search result without asking only when the title is
// the same and there is no second candidate that fits equally well.
func autoPick(title string, year int, rs []SearchResult) *SearchResult {
	q := norm(title)
	if q == "" {
		return nil
	}
	var exact []*SearchResult
	for i := range rs {
		if norm(rs[i].Title) == q || norm(rs[i].OriginalTitle) == q {
			exact = append(exact, &rs[i])
		}
	}
	if len(exact) == 0 && year > 0 { // "House M D" is "House"; but only of the same year
		for i := range rs {
			if r := &rs[i]; r.Year == year && (sameButShort(title, r.Title) || sameButShort(title, r.OriginalTitle)) {
				exact = append(exact, r)
			}
		}
	}
	if year == 0 {
		if len(exact) == 1 {
			return exact[0]
		}
		return nil
	}
	var same, near []*SearchResult
	for _, r := range exact {
		switch r.Year - year {
		case 0:
			same = append(same, r)
		case 1, -1:
			near = append(near, r)
		}
	}
	switch {
	case len(same) == 1:
		return same[0]
	case len(same) > 1 && same[0] == &rs[0]:
		// Namesakes of the same year (fan films, featurettes): catalogues
		// put the well-known title first, so the top result is trusted.
		return same[0]
	case len(same) == 0 && len(near) == 1:
		return near[0]
	}
	return nil
}

// sameButShort tells whether a title is the catalogue's one with a few short
// words added — "House M D" for "House", "The Office US", "Shameless UK".
func sameButShort(title, catalogue string) bool {
	words := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	}
	t, c := words(title), words(catalogue)
	if len(c) == 0 || len(t) <= len(c) {
		return false
	}
	for i := range c {
		if t[i] != c[i] {
			return false
		}
	}
	for _, w := range t[len(c):] {
		if utf8.RuneCountInString(w) > 2 {
			return false
		}
	}
	return true
}

var (
	reTitleSplit = regexp.MustCompile(`[.:;!?–—]\s+|\s+[-–—]\s+`)
	rePunct      = regexp.MustCompile(`[.,:;!?–—«»"]+`)
)

// queryVariants returns the title and its simplifications, to be tried in
// turn until something is found: catalogues rarely know a localized
// subtitle, so "Форсаж. На пределе скорости" also becomes "Форсаж" and
// "На пределе скорости".
func queryVariants(title string) []string {
	out := []string{title}
	add := func(s string) {
		s = strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
		if utf8.RuneCountInString(s) < 3 {
			return
		}
		for _, have := range out {
			if strings.EqualFold(have, s) {
				return
			}
		}
		out = append(out, s)
	}
	add(rePunct.ReplaceAllString(title, " "))
	if parts := reTitleSplit.Split(title, -1); len(parts) > 1 {
		add(parts[0])
		add(parts[len(parts)-1])
	}
	return out
}

// search looks in all sources for the title, then for its simplifications.
// A single file may well be a miniseries or a TV movie, so a "movie" is
// searched among series too; a file with a season and an episode number is
// searched among movies only when no series is found.
func (a *App) search(kind, title string, year int) []Found {
	if title == "" {
		return nil
	}
	kinds := []string{kind}
	if kind == kindMovie {
		kinds = []string{kindMovie, kindTV}
	}
	variants := queryVariants(title)
	for _, q := range variants {
		if found := a.hub.SearchAll(kinds, q, year); len(found) > 0 {
			return found
		}
	}
	if kind == kindTV {
		for _, q := range variants {
			if found := a.hub.SearchAll([]string{kindMovie}, q, year); len(found) > 0 {
				return found
			}
		}
	}
	return nil
}

// mergeFound appends the extra results to the groups of the same sources,
// dropping entries that are already listed.
func mergeFound(found, extra []Found) []Found {
	for _, e := range extra {
		target := -1
		for i := range found {
			if found[i].Provider.Key() == e.Provider.Key() {
				target = i
			}
		}
		if target < 0 {
			found = append(found, e)
			continue
		}
		have := map[string]bool{}
		for _, r := range found[target].Results {
			have[r.Kind+r.ID] = true
		}
		// The transliteration is a better guess than fuzzy look-alikes of
		// the Latin spelling, so its results go first.
		var fresh []SearchResult
		for _, r := range e.Results {
			if !have[r.Kind+r.ID] {
				fresh = append(fresh, r)
			}
		}
		found[target].Results = append(fresh, found[target].Results...)
	}
	return found
}

var translit = []struct{ lat, cyr string }{
	{"shch", "щ"}, {"zh", "ж"}, {"kh", "х"}, {"ts", "ц"}, {"ch", "ч"}, {"sh", "ш"},
	{"yu", "ю"}, {"ya", "я"}, {"yo", "ё"}, {"ye", "е"}, {"yy", "ый"}, {"iy", "ий"},
	{"a", "а"}, {"b", "б"}, {"v", "в"}, {"g", "г"}, {"d", "д"}, {"e", "е"}, {"z", "з"},
	{"i", "и"}, {"j", "й"}, {"k", "к"}, {"l", "л"}, {"m", "м"}, {"n", "н"}, {"o", "о"},
	{"p", "п"}, {"r", "р"}, {"s", "с"}, {"t", "т"}, {"u", "у"}, {"f", "ф"}, {"h", "х"},
	{"c", "к"}, {"w", "в"}, {"x", "кс"}, {"q", "к"}, {"'", "ь"},
}

// toCyrillic spells a Latin title with Russian letters, as if it were a
// transliterated Russian name: "Myatezh" -> "мятеж". Returns "" for a
// title that is not purely Latin.
func toCyrillic(title string) string {
	s := strings.ToLower(title)
	var b strings.Builder
	letters := false
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c >= 0x80:
			return ""
		case c == 'y': // "й" after a vowel, "ы" otherwise; digraphs are handled below
			if pair := s[i:min(i+2, len(s))]; pair != "yu" && pair != "ya" && pair != "yo" && pair != "ye" && pair != "yy" {
				if i > 0 && strings.IndexByte("aeiou", s[i-1]) >= 0 {
					b.WriteString("й")
				} else {
					b.WriteString("ы")
				}
				i++
				letters = true
				continue
			}
		}
		matched := false
		for _, t := range translit {
			if strings.HasPrefix(s[i:], t.lat) {
				b.WriteString(t.cyr)
				i += len(t.lat)
				matched, letters = true, true
				break
			}
		}
		if !matched {
			b.WriteByte(c)
			i++
		}
	}
	if !letters {
		return ""
	}
	return b.String()
}

// Identify finds the catalogue entry for a unit, asking the user when the
// file name is not enough. A nil match means the unit was skipped.
func (a *App) Identify(u *Unit) (*Match, error) {
	if !a.refresh {
		if m := a.fromNFO(u); m != nil {
			return m, nil
		}
	}

	// The name guessed from the files first, then the other names it goes
	// by; sources are tried in priority order: the first confident match wins.
	var found []Found
	for _, title := range append([]string{u.Title}, u.Alt...) {
		more := a.search(u.Kind, title, u.Year)
		for _, f := range more {
			var sameKind []SearchResult
			for _, r := range f.Results {
				if r.Kind == u.Kind {
					sameKind = append(sameKind, r)
				}
			}
			if r := autoPick(title, u.Year, sameKind); r != nil {
				// A result of the wrong kind is left for the dialog to sort out.
				if m, err := a.hub.Load(*r); err == nil && (m.Show != nil) == (u.Kind == kindTV) {
					return m, nil
				}
			}
		}
		found = mergeFound(found, more)
	}
	// Nothing certain, and the name may be a transliteration ("Myatezh"):
	// add what the Cyrillic spelling finds.
	if cyr := toCyrillic(u.Title); cyr != "" {
		found = mergeFound(found, a.search(u.Kind, cyr, u.Year))
	}
	if len(a.hub.Active()) == 0 {
		return nil, errors.New("no sources are left available")
	}
	if a.yes {
		return nil, nil
	}
	return a.dialog(u, found)
}

// fromNFO reloads the entry recorded by a previous run in the .nfo file.
func (a *App) fromNFO(u *Unit) *Match {
	source, id, kind, local := existingID(a.root, u)
	if id == "" {
		return nil
	}
	m, err := a.hub.Load(SearchResult{Source: source, Kind: kind, ID: id})
	switch {
	case err != nil:
		return nil
	case m.Show != nil && u.Kind == kindMovie:
		m = &Match{Movie: m.Show.AsMovie()} // a series kept as one file
	case m.Movie != nil && u.Kind == kindTV:
		return nil
	}
	if m.Movie != nil {
		m.Movie.Local = local
	} else {
		m.Show.Local = local
	}
	return m
}

// localize finds the Russian title of a match whose data is in another
// language. It needs the IMDb number and the Wikidata source; a failed
// lookup is simply tried again on the next run.
func (a *App) localize(m *Match) {
	title, ids, local := m.title()
	if local.Known || hasCyrillic(title) || ids["imdb"] == "" {
		return
	}
	w, ok := a.hub.Get("wikidata").(*Wikidata)
	if !ok {
		return
	}
	ru, err := w.RussianTitle(ids["imdb"])
	if a.hub.check(w, err) != nil {
		return
	}
	*local = LocalTitle{Title: ru, Known: true}
}

func (m *Match) title() (string, map[string]string, *LocalTitle) {
	if m.Movie != nil {
		return m.Movie.Title, m.Movie.IDs, &m.Movie.Local
	}
	return m.Show.Title, m.Show.IDs, &m.Show.Local
}

func (a *App) dialog(u *Unit, found []Found) (*Match, error) {
	var lastErr error
	for {
		choices := a.drawDialog(u, found, lastErr)
		lastErr = nil

		line, err := a.ui.ReadLine("> ")
		if err == io.EOF {
			return nil, errQuit
		}
		var m *Match
		switch low := strings.ToLower(line); {
		case low == "" || low == "s" || low == "ы" || low == "-":
			return nil, nil
		case low == "q" || low == "й":
			return nil, errQuit
		case isNumber(line) && len(line) <= 2:
			n := atoi(line)
			if n < 1 || n > len(choices) {
				lastErr = fmt.Errorf("there is no entry %d in the list", n)
				continue
			}
			m, err = a.hub.Load(choices[n-1])
		default:
			ref, ok := parseRef(line)
			if !ok {
				// "Форсаж 2026": the year is a hint, not a part of the title.
				title, year := cleanTitle(line)
				if title == "" {
					title = line
				}
				found = a.search(u.Kind, title, year)
				continue
			}
			m, err = a.lookupRef(ref, u.Kind)
		}
		if err == nil {
			err = a.reconcileKind(u, m)
		}
		if err == errQuit {
			return nil, err
		} else if err != nil {
			lastErr = err
			continue
		}
		return m, nil
	}
}

// drawDialog shows the window and returns the numbered candidates.
func (a *App) drawDialog(u *Unit, found []Found, lastErr error) []SearchResult {
	kind := "movie"
	if u.Kind == kindTV {
		kind = "series"
	}
	guess := fmt.Sprintf("Guessed: %s \"%s\"", kind, u.Title)
	if u.Year > 0 {
		guess += fmt.Sprintf(" (%d)", u.Year)
	}
	head := []string{"File: " + u.Files[0].Rel}
	if len(u.Files) > 1 {
		head = append(head, fmt.Sprintf("      … and %d more", len(u.Files)-1))
	}
	head = append(head, guess)

	// With many sources each gets fewer lines, so the window stays readable.
	perSource := 6
	if len(found) > 2 {
		perSource = 4
	}
	var choices []SearchResult
	var list []string
	for _, f := range found {
		list = append(list, f.Provider.Name()+":")
		for i, r := range f.Results {
			if i == perSource {
				break
			}
			choices = append(choices, r)
			list = append(list, fmt.Sprintf("  %2d) %s", len(choices), describe(r)))
		}
	}
	if len(list) == 0 {
		list = []string{"Nothing found in any source."}
	}
	if lastErr != nil {
		list = append(list, "", "! "+lastErr.Error())
	}
	help := []string{
		"a number     pick an entry from the list",
		"a title      search all sources; any language, a year helps: Форсаж 2026",
		"tt0371746    IMDb number;  also tmdb:1726, kp:61237, tvmaze:714",
		"a link       to IMDb, TMDB, Kinopoisk, Letterboxd or TVMaze",
		"s  skip      q  quit",
	}
	a.ui.Box("What is this?", head, list, help)
	return choices
}

func describe(r SearchResult) string {
	s := r.Title
	if r.Year > 0 {
		s += fmt.Sprintf(" (%d)", r.Year)
	}
	if r.OriginalTitle != "" && r.OriginalTitle != r.Title {
		s += " — " + r.OriginalTitle
	}
	if r.Kind == kindTV {
		s += " [series]"
	}
	return s
}

// Ref is an identifier typed by the user.
type Ref struct {
	Source string // imdb, tmdb, kinopoisk, letterboxd, tvmaze
	ID     string
	Kind   string // for tmdb: movie, tv or "" (same as the guess)
}

var (
	reIMDb      = regexp.MustCompile(`\btt\d{5,}\b`)
	reTMDBURL   = regexp.MustCompile(`themoviedb\.org/(movie|tv)/(\d+)`)
	reTMDB      = regexp.MustCompile(`(?i)^(tmdb|movie|tv):\s*(\d+)$`)
	reKPURL     = regexp.MustCompile(`kinopoisk\.ru/(?:film|series)/(\d+)`)
	reKP        = regexp.MustCompile(`(?i)^(?:kp|кп|kinopoisk):\s*(\d+)$`)
	reLBURL     = regexp.MustCompile(`(?:letterboxd\.com/film/|boxd\.it/)[\w-]+`)
	reLB        = regexp.MustCompile(`(?i)^(?:lb|letterboxd):\s*([\w-]+)$`)
	reTVMazeURL = regexp.MustCompile(`tvmaze\.com/shows/(\d+)`)
	reTVMaze    = regexp.MustCompile(`(?i)^tvmaze:\s*(\d+)$`)
)

func parseRef(s string) (Ref, bool) {
	if m := reTMDBURL.FindStringSubmatch(s); m != nil {
		return Ref{Source: "tmdb", ID: m[2], Kind: m[1]}, true
	}
	if m := reKPURL.FindStringSubmatch(s); m != nil {
		return Ref{Source: "kinopoisk", ID: m[1]}, true
	}
	if m := reLBURL.FindString(s); m != "" {
		return Ref{Source: "letterboxd", ID: m}, true
	}
	if m := reTVMazeURL.FindStringSubmatch(s); m != nil {
		return Ref{Source: "tvmaze", ID: m[1], Kind: kindTV}, true
	}
	if m := reIMDb.FindString(s); m != "" {
		return Ref{Source: "imdb", ID: m}, true
	}
	if m := reTMDB.FindStringSubmatch(s); m != nil {
		kind := strings.ToLower(m[1])
		if kind == "tmdb" {
			kind = ""
		}
		return Ref{Source: "tmdb", ID: m[2], Kind: kind}, true
	}
	if m := reKP.FindStringSubmatch(s); m != nil {
		return Ref{Source: "kinopoisk", ID: m[1]}, true
	}
	if m := reLB.FindStringSubmatch(s); m != nil {
		return Ref{Source: "letterboxd", ID: m[1]}, true
	}
	if m := reTVMaze.FindStringSubmatch(s); m != nil {
		return Ref{Source: "tvmaze", ID: m[1], Kind: kindTV}, true
	}
	return Ref{}, false
}

func (a *App) lookupRef(ref Ref, guessKind string) (*Match, error) {
	if ref.Source == "imdb" {
		return a.hub.LoadByIMDb(ref.ID)
	}
	p := a.hub.Get(ref.Source)
	if p == nil {
		if ref.Source == "kinopoisk" {
			// No Kinopoisk key: Wikidata knows the IMDb number of most films.
			imdb, err := kinopoiskToIMDb(ref.ID)
			if err != nil {
				return nil, err
			}
			return a.hub.LoadByIMDb(imdb)
		}
		return nil, fmt.Errorf("source %s is not connected or not available", ref.Source)
	}
	if l, ok := p.(Lookuper); ok {
		r, err := l.Lookup(ref.ID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ref.ID, a.hub.check(p, err))
		}
		return a.hub.Load(*r)
	}
	kind := ref.Kind
	if kind == "" {
		kind = guessKind
	}
	m, err := a.hub.Load(SearchResult{Source: ref.Source, Kind: kind, ID: ref.ID})
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", p.Name(), ref.ID, err)
	}
	return m, nil
}

// reconcileKind handles the case when the chosen entry is a series while
// the file looked like a movie, or the other way round.
func (a *App) reconcileKind(u *Unit, m *Match) error {
	switch {
	case m.Show != nil && u.Kind == kindMovie:
		f := u.Files[0]
		a.ui.Printf("\"%s\" is a series, and %s has no episode number.\n", m.Show.Title, filepath.Base(f.Path))
		season, whole, err := a.askNumber("Season (Enter if the file is the whole series, to keep it as a movie): ", true)
		if err != nil {
			return err
		}
		if whole {
			m.Movie, m.Show = m.Show.AsMovie(), nil
			return nil
		}
		episode, _, err := a.askNumber("Episode: ", false)
		if err != nil {
			return err
		}
		f.Guess.IsSeries, f.Guess.Season, f.Guess.Episodes = true, season, []int{episode}
		u.Kind = kindTV
	case m.Movie != nil && u.Kind == kindTV:
		if len(u.Files) > 1 {
			return fmt.Errorf("\"%s\" is a movie, but the group has %d files", m.Movie.Title, len(u.Files))
		}
		u.Files[0].Guess.IsSeries = false
		u.Kind = kindMovie
	}
	return nil
}

// askNumber reads a number; with allowEmpty an empty line is an answer too.
func (a *App) askNumber(prompt string, allowEmpty bool) (n int, empty bool, err error) {
	for {
		line, err := a.ui.ReadLine(prompt)
		switch {
		case err != nil:
			return 0, false, errQuit
		case line == "" && allowEmpty:
			return 0, true, nil
		case isNumber(line):
			return atoi(line), false, nil
		}
		a.ui.Printf("A number is expected.\n")
	}
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
