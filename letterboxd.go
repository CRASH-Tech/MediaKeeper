package main

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
	"sync"
)

var letterboxdURL = "https://letterboxd.com"

// Letterboxd has no public API and blocks its search for programs, so this
// source reads film pages: by link, by IMDb number (letterboxd.com/imdb/…)
// and by guessing the page address from the title ("Iron Man", 2008 ->
// /film/iron-man-2008/). Movies only, English, no key.
type Letterboxd struct {
	mu    sync.Mutex
	pages map[string]*lbPage // by requested URL and by slug
}

type lbPage struct {
	movie *Movie
	err   error
}

func NewLetterboxd() *Letterboxd { return &Letterboxd{pages: map[string]*lbPage{}} }

func (c *Letterboxd) Key() string  { return "letterboxd" }
func (c *Letterboxd) Name() string { return "Letterboxd" }

var (
	reLBPath    = regexp.MustCompile(`/film/([^/]+)/?$`)
	reLBTMDB    = regexp.MustCompile(`data-tmdb-id="(\d+)"`)
	reLBIMDb    = regexp.MustCompile(`imdb\.com/title/(tt\d+)`)
	reLBJSON    = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)
	reLBTitle   = regexp.MustCompile(`<meta property="og:title" content="([^"]*)"`)
	reLBImage   = regexp.MustCompile(`<meta property="og:image" content="([^"]*)"`)
	reLBTagline = regexp.MustCompile(`<h4 class="tagline">([^<]*)</h4>`)
	reLBYear    = regexp.MustCompile(`^(.*) \((\d{4})\)$`)
	reDuration  = regexp.MustCompile(`^PT(?:(\d+)H)?(?:(\d+)M)?`)
	reNotSlug   = regexp.MustCompile(`[^a-z0-9]+`)
)

type lbNamed struct {
	Name string `json:"name"`
}

func lbNames(list []lbNamed) []string {
	var out []string
	for _, n := range list {
		out = append(out, n.Name)
	}
	return out
}

// fetch loads a film page; the answer (including "no such page") is cached.
func (c *Letterboxd) fetch(pageURL string) (*Movie, error) {
	c.mu.Lock()
	p := c.pages[pageURL]
	c.mu.Unlock()
	if p != nil {
		return p.movie, p.err
	}
	m, err := c.parse(pageURL)
	if _, down := err.(*unavailable); down {
		return nil, err
	}
	c.mu.Lock()
	c.pages[pageURL] = &lbPage{m, err}
	if m != nil {
		c.pages[letterboxdURL+"/film/"+m.ID+"/"] = &lbPage{m, nil}
	}
	c.mu.Unlock()
	return m, err
}

func (c *Letterboxd) parse(pageURL string) (*Movie, error) {
	body, final, err := httpGet(pageURL, nil)
	if err != nil {
		return nil, err
	}
	path := reLBPath.FindStringSubmatch(final.Path)
	if path == nil {
		return nil, errNotFound // redirected to something that is not a film
	}
	page := string(body)
	first := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(page); m != nil {
			return html.UnescapeString(m[1])
		}
		return ""
	}

	m := &Movie{Source: "letterboxd", ID: path[1], IDs: map[string]string{}}
	if id := first(reLBTMDB); id != "" {
		m.IDs["tmdb"] = id
	}
	if id := first(reLBIMDb); id != "" {
		m.IDs["imdb"] = id
	}
	m.Title = first(reLBTitle)
	if t := reLBYear.FindStringSubmatch(m.Title); t != nil {
		m.Title, m.Year = t[1], atoi(t[2])
	}
	m.Tagline = first(reLBTagline)
	m.Backdrop = first(reLBImage)

	var ld struct {
		Name        string    `json:"name"`
		Image       string    `json:"image"`
		Description string    `json:"description"`
		DateCreated string    `json:"dateCreated"`
		Duration    string    `json:"duration"`
		Genre       []string  `json:"genre"`
		Director    []lbNamed `json:"director"`
		Actor       []lbNamed `json:"actor"`
		Company     []lbNamed `json:"productionCompany"`
		Country     []lbNamed `json:"countryOfOrigin"`
		Rating      struct {
			Value float64 `json:"ratingValue"`
		} `json:"aggregateRating"`
	}
	if raw := reLBJSON.FindStringSubmatch(page); raw != nil {
		// The JSON is wrapped into a CDATA comment.
		s := raw[1]
		if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
			json.Unmarshal([]byte(s[i:j+1]), &ld)
		}
	}
	if ld.Name != "" {
		m.Title = ld.Name
	}
	if m.Title == "" {
		return nil, errNotFound
	}
	m.Overview = ld.Description
	if m.Year == 0 || yearOf(ld.DateCreated) == m.Year {
		m.Released = ld.DateCreated
		m.Year = yearOf(ld.DateCreated)
	}
	if d := reDuration.FindStringSubmatch(ld.Duration); d != nil {
		m.Runtime = atoi(d[1])*60 + atoi(d[2])
	}
	m.Rating = ld.Rating.Value * 2 // Letterboxd rates out of 5
	m.Genres = ld.Genre
	m.Directors = lbNames(ld.Director)
	m.Studios = lbNames(ld.Company)
	m.Countries = lbNames(ld.Country)
	for _, a := range ld.Actor {
		m.Cast = append(m.Cast, Person{Name: a.Name})
	}
	// The page links a 600px poster; the same address serves a larger one.
	m.Poster = strings.Replace(ld.Image, "-0-600-0-900-", "-0-1000-0-1500-", 1)
	return m, nil
}

func lbResult(m *Movie) *SearchResult {
	return &SearchResult{Source: "letterboxd", Kind: kindMovie, ID: m.ID, Title: m.Title, Year: m.Year}
}

func slugify(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "'", "")
	return strings.Trim(reNotSlug.ReplaceAllString(s, "-"), "-")
}

func (c *Letterboxd) Search(kind, query string, year int) ([]SearchResult, error) {
	slug := slugify(query)
	if kind != kindMovie || slug == "" {
		return nil, nil
	}
	slugs := []string{slug}
	if year > 0 {
		slugs = []string{slug + "-" + itoa(year), slug}
	}
	var out []SearchResult
	seen := map[string]bool{}
	for _, s := range slugs {
		m, err := c.fetch(letterboxdURL + "/film/" + s + "/")
		if err == errNotFound {
			continue
		} else if err != nil {
			return nil, err
		}
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, *lbResult(m))
		}
	}
	return out, nil
}

func (c *Letterboxd) ByIMDb(imdb string) (*SearchResult, error) {
	m, err := c.fetch(letterboxdURL + "/imdb/" + imdb + "/")
	if err != nil {
		return nil, err
	}
	return lbResult(m), nil
}

// Lookup accepts a film link (letterboxd.com/film/…, boxd.it/…) or a slug.
func (c *Letterboxd) Lookup(ref string) (*SearchResult, error) {
	switch {
	case strings.Contains(ref, "boxd.it/"):
		ref = "https://" + ref[strings.Index(ref, "boxd.it/"):]
	case strings.Contains(ref, "/film/"):
		slug := strings.Trim(ref[strings.Index(ref, "/film/")+len("/film/"):], "/")
		ref = letterboxdURL + "/film/" + strings.SplitN(slug, "/", 2)[0] + "/"
	default:
		ref = letterboxdURL + "/film/" + ref + "/"
	}
	m, err := c.fetch(ref)
	if err != nil {
		return nil, err
	}
	return lbResult(m), nil
}

func (c *Letterboxd) Movie(id string) (*Movie, error) {
	return c.fetch(letterboxdURL + "/film/" + id + "/")
}

func (c *Letterboxd) Show(string) (*Show, error)          { return nil, errNotFound }
func (c *Letterboxd) Season(string, int) (*Season, error) { return nil, errNotFound }
