package main

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Guess is what could be understood from a file name alone.
type Guess struct {
	Title    string
	Year     int
	IsSeries bool
	Season   int
	Episodes []int
}

var (
	reSxE     = regexp.MustCompile(`(?i)\bs(\d{1,2})[ ._-]{0,3}e(\d{1,3})(?:(?:-e?|e)(\d{1,3}))?\b`)
	reNxN     = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})(?:-(\d{2,3}))?\b`)
	reWords   = regexp.MustCompile(`(?i)(?:season|сезон)[ ._]*(\d{1,2})[ ._,-]*(?:episode|серия|эпизод)[ ._]*(\d{1,3})`)
	reRuNum   = regexp.MustCompile(`(?i)(\d{1,2})[ ._-]*сезон[ ._,-]*(\d{1,3})[ ._-]*сери`)
	reEpOnly  = regexp.MustCompile(`(?i)(?:^|[ ._-])(?:e|ep|episode|серия|эпизод)[ ._]*(\d{1,3})(?:$|[ ._-])`)
	reEpRuNum = regexp.MustCompile(`(?i)(?:^|[ ._-])(\d{1,3})[ ._-]*серия`)
	reEpLead  = regexp.MustCompile(`^(\d{1,3})(?:$|[ ._-])`)

	reSeasonDir = regexp.MustCompile(`(?i)^(?:season|сезон|s)[ ._-]*(\d{1,2})$|^(\d{1,2})[ ._-]*(?:season|сезон)$`)

	reSquare = regexp.MustCompile(`\[[^\]]*\]`)
	reParen  = regexp.MustCompile(`\([^)]*\)`)
	reYear   = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reSpaces = regexp.MustCompile(`\s+`)
	reJunk   = regexp.MustCompile(`(?i)\b(2160p|1080[pi]|720p|480p|4k|uhd|hdr|hdr10|web-?dl(rip)?|webrip|blu-?ray|bdrip|bdremux|remux|hdrip|dvdrip|dvd\d?|hdtv(rip)?|tvrip|satrip|camrip|x26[45]|h\.?26[45]|hevc|avc|xvid|divx|aac|ac3|dts|imax|amzn|extended|unrated|remastered|proper|repack|lostfilm|newstudio)\b`)
)

// ParsePath guesses what a media file is. rel is the path relative to the
// scanned root, so that parent directory names can help.
func ParsePath(rel string) Guess {
	base := filepath.Base(rel)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	var dirs []string
	if d := filepath.Dir(rel); d != "." {
		dirs = strings.Split(d, string(filepath.Separator))
	}

	dirSeason := -1
	for _, d := range dirs {
		if m := reSeasonDir.FindStringSubmatch(strings.TrimSpace(d)); m != nil {
			dirSeason = atoi(m[1] + m[2])
		}
	}

	g := Guess{}
	prefix := ""
	switch {
	case matchSeasonEpisode(stem, &g, &prefix):
	case dirSeason >= 0 && matchEpisodeOnly(stem, &g, &prefix):
		g.Season = dirSeason
	default:
		g.Title, g.Year = cleanTitle(stem)
		return g
	}

	g.IsSeries = true
	g.Title, g.Year = cleanTitle(prefix)
	// The file name has no series title: take it from the nearest parent
	// directory that is not a "Season N" one.
	for i := len(dirs) - 1; i >= 0 && g.Title == ""; i-- {
		if reSeasonDir.MatchString(strings.TrimSpace(dirs[i])) {
			continue
		}
		g.Title, g.Year = cleanTitle(dirs[i])
	}
	return g
}

func matchSeasonEpisode(stem string, g *Guess, prefix *string) bool {
	for _, re := range []*regexp.Regexp{reSxE, reNxN, reWords, reRuNum} {
		m := re.FindStringSubmatchIndex(stem)
		if m == nil {
			continue
		}
		g.Season = atoi(stem[m[2]:m[3]])
		first := atoi(stem[m[4]:m[5]])
		last := first
		if len(m) > 7 && m[6] >= 0 {
			last = atoi(stem[m[6]:m[7]])
		}
		g.Episodes = episodeRange(first, last)
		*prefix = stem[:m[0]]
		return true
	}
	return false
}

func matchEpisodeOnly(stem string, g *Guess, prefix *string) bool {
	for _, re := range []*regexp.Regexp{reEpOnly, reEpRuNum, reEpLead} {
		m := re.FindStringSubmatchIndex(stem)
		if m == nil {
			continue
		}
		g.Episodes = []int{atoi(stem[m[2]:m[3]])}
		*prefix = stem[:m[0]]
		return true
	}
	return false
}

func episodeRange(first, last int) []int {
	if last <= first || last-first > 20 {
		return []int{first}
	}
	eps := make([]int, 0, last-first+1)
	for e := first; e <= last; e++ {
		eps = append(eps, e)
	}
	return eps
}

// cleanTitle strips release junk from a raw name and extracts the year.
func cleanTitle(raw string) (string, int) {
	s := reSquare.ReplaceAllString(raw, " ")
	s = reParen.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.TrimSpace(m[1 : len(m)-1])
		if reYear.MatchString(inner) && len(inner) == 4 {
			return " " + inner + " "
		}
		return " "
	})
	s = strings.ReplaceAll(s, "_", " ")
	// Dots are word separators only in "Scene.Style.Names"; in normal names
	// ("Форсаж. На пределе скорости") they are punctuation.
	if !strings.Contains(strings.TrimSpace(s), " ") {
		s = strings.ReplaceAll(s, ".", " ")
	}

	title, year := s, 0
	maxYear := time.Now().Year() + 1
	ms := reYear.FindAllStringIndex(s, -1)
	for i := len(ms) - 1; i >= 0; i-- {
		y := atoi(s[ms[i][0]:ms[i][1]])
		if y > maxYear || strings.TrimSpace(s[:ms[i][0]]) == "" {
			continue
		}
		title, year = s[:ms[i][0]], y
		break
	}
	if loc := reJunk.FindStringIndex(title); loc != nil && strings.TrimSpace(title[:loc[0]]) != "" {
		title = title[:loc[0]]
	}
	title = reSpaces.ReplaceAllString(title, " ")
	title = strings.Trim(title, " -._,")
	if title == "" && year == 0 {
		title = strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
	}
	return title, year
}

// norm reduces a title to letters and digits for comparison.
func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r == 'ё' {
			r = 'е'
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
