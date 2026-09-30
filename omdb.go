package main

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var omdbURL = "https://www.omdbapi.com/"

// OMDb serves IMDb data (English) for movies and series; it needs a key
// and its IDs are IMDb numbers.
type OMDb struct{ key string }

func NewOMDb(key string) *OMDb { return &OMDb{key: key} }

func (c *OMDb) Key() string  { return "omdb" }
func (c *OMDb) Name() string { return "OMDb" }

type omdbTitle struct {
	Response string `json:"Response"`
	Error    string `json:"Error"`

	Title      string `json:"Title"`
	Year       string `json:"Year"`
	Rated      string `json:"Rated"`
	Released   string `json:"Released"`
	Runtime    string `json:"Runtime"`
	Genre      string `json:"Genre"`
	Director   string `json:"Director"`
	Writer     string `json:"Writer"`
	Actors     string `json:"Actors"`
	Plot       string `json:"Plot"`
	Country    string `json:"Country"`
	Poster     string `json:"Poster"`
	Rating     string `json:"imdbRating"`
	IMDbID     string `json:"imdbID"`
	Type       string `json:"Type"`
	Production string `json:"Production"`

	Search   []omdbTitle `json:"Search"`
	Episodes []struct {
		Title    string `json:"Title"`
		Released string `json:"Released"`
		Episode  string `json:"Episode"`
		Rating   string `json:"imdbRating"`
	} `json:"Episodes"`
}

func (c *OMDb) get(q url.Values) (*omdbTitle, error) {
	q.Set("apikey", c.key)
	body, _, err := httpGet(omdbURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var t omdbTitle
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, err
	}
	if t.Response == "False" {
		if strings.Contains(t.Error, "limit") || strings.Contains(t.Error, "API key") {
			return nil, &unavailable{t.Error}
		}
		return nil, errNotFound // "Movie not found!", "Too many results."
	}
	return &t, nil
}

// na drops OMDb's placeholder for a missing value.
func na(s string) string {
	if s == "N/A" {
		return ""
	}
	return s
}

func omdbKind(typ string) string {
	switch typ {
	case "movie":
		return kindMovie
	case "series":
		return kindTV
	}
	return ""
}

func (t *omdbTitle) result() SearchResult {
	return SearchResult{Source: "omdb", Kind: omdbKind(t.Type), ID: t.IMDbID, Title: t.Title, Year: yearOf(t.Year)}
}

func (c *OMDb) Search(kind, query string, year int) ([]SearchResult, error) {
	q := url.Values{"s": {query}, "type": {"movie"}}
	if kind == kindTV {
		q.Set("type", "series")
	}
	if year > 0 {
		q.Set("y", strconv.Itoa(year))
	}
	t, err := c.get(q)
	if err == errNotFound {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, r := range t.Search {
		out = append(out, r.result())
	}
	return out, nil
}

func (c *OMDb) ByIMDb(imdb string) (*SearchResult, error) {
	t, err := c.get(url.Values{"i": {imdb}})
	if err != nil {
		return nil, err
	}
	r := t.result()
	if r.Kind == "" { // an episode or a game
		return nil, errNotFound
	}
	return &r, nil
}

var reAmazonSize = regexp.MustCompile(`\._V1_[^/]*\.jpg$`)

// omdbPoster asks for the full-size image instead of the 300px preview.
func omdbPoster(u string) string {
	return reAmazonSize.ReplaceAllString(na(u), "._V1_.jpg")
}

func omdbDate(s string) string {
	if t, err := time.Parse("02 Jan 2006", s); err == nil {
		return t.Format("2006-01-02")
	}
	return ""
}

func omdbCast(actors string) []Person {
	var out []Person
	for _, name := range splitList(na(actors)) {
		out = append(out, Person{Name: name})
	}
	return out
}

func (c *OMDb) Movie(id string) (*Movie, error) {
	t, err := c.get(url.Values{"i": {id}, "plot": {"full"}})
	if err != nil {
		return nil, err
	}
	rating, _ := strconv.ParseFloat(t.Rating, 64)
	return &Movie{
		Source: "omdb", ID: t.IMDbID, IDs: map[string]string{"imdb": t.IMDbID},
		Title: t.Title, Overview: na(t.Plot), Released: omdbDate(t.Released), Year: yearOf(t.Year),
		Runtime: atoi(strings.TrimSuffix(t.Runtime, " min")), Rating: rating, MPAA: na(t.Rated),
		Genres: splitList(na(t.Genre)), Countries: splitList(na(t.Country)), Studios: splitList(na(t.Production)),
		Directors: splitList(na(t.Director)), Writers: splitList(na(t.Writer)),
		Cast: omdbCast(t.Actors), Poster: omdbPoster(t.Poster),
	}, nil
}

func (c *OMDb) Show(id string) (*Show, error) {
	t, err := c.get(url.Values{"i": {id}, "plot": {"full"}})
	if err != nil {
		return nil, err
	}
	rating, _ := strconv.ParseFloat(t.Rating, 64)
	return &Show{
		Source: "omdb", ID: t.IMDbID, IDs: map[string]string{"imdb": t.IMDbID},
		Title: t.Title, Overview: na(t.Plot), Premiered: omdbDate(t.Released), Year: yearOf(t.Year),
		Rating: rating, MPAA: na(t.Rated), Genres: splitList(na(t.Genre)),
		Cast: omdbCast(t.Actors), Poster: omdbPoster(t.Poster),
	}, nil
}

func (c *OMDb) Season(showID string, n int) (*Season, error) {
	t, err := c.get(url.Values{"i": {showID}, "Season": {strconv.Itoa(n)}})
	if err != nil {
		return nil, err
	}
	s := &Season{Number: n}
	for _, e := range t.Episodes {
		rating, _ := strconv.ParseFloat(e.Rating, 64)
		s.Episodes = append(s.Episodes, Episode{
			Number: atoi(e.Episode), Title: na(e.Title), Aired: na(e.Released), Rating: rating,
		})
	}
	return s, nil
}
