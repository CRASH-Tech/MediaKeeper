package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

var (
	kinopoiskURL = "https://kinopoiskapiunofficial.tech"
	wikidataURL  = "https://www.wikidata.org/w/api.php"
)

// Kinopoisk works through kinopoiskapiunofficial.tech (free key): Russian
// titles and descriptions for movies and series.
type Kinopoisk struct {
	key   string
	mu    sync.Mutex
	films map[string]*kpFilm
}

func NewKinopoisk(key string) *Kinopoisk { return &Kinopoisk{key: key, films: map[string]*kpFilm{}} }

func (c *Kinopoisk) Key() string  { return "kinopoisk" }
func (c *Kinopoisk) Name() string { return "Kinopoisk" }

// flexInt reads a number that the API sends sometimes as 2008 and
// sometimes as "2008".
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	*f = flexInt(atoi(strings.Trim(string(b), `"`)))
	return nil
}

type kpFilm struct {
	KinopoiskID  int     `json:"kinopoiskId"`
	FilmID       int     `json:"filmId"` // the search calls it this way
	IMDbID       string  `json:"imdbId"`
	NameRu       string  `json:"nameRu"`
	NameEn       string  `json:"nameEn"`
	NameOriginal string  `json:"nameOriginal"`
	PosterURL    string  `json:"posterUrl"`
	CoverURL     string  `json:"coverUrl"`
	Rating       float64 `json:"ratingKinopoisk"`
	Year         flexInt `json:"year"`
	FilmLength   flexInt `json:"filmLength"`
	Slogan       string  `json:"slogan"`
	Description  string  `json:"description"`
	Type         string  `json:"type"`
	RatingMPAA   string  `json:"ratingMpaa"`
	Countries    []struct {
		Country string `json:"country"`
	} `json:"countries"`
	Genres []struct {
		Genre string `json:"genre"`
	} `json:"genres"`
}

func (f *kpFilm) id() string {
	if f.KinopoiskID > 0 {
		return itoa(f.KinopoiskID)
	}
	return itoa(f.FilmID)
}

func (f *kpFilm) kind() string {
	switch f.Type {
	case "TV_SERIES", "MINI_SERIES", "TV_SHOW":
		return kindTV
	}
	return kindMovie
}

func (f *kpFilm) title() string {
	for _, t := range []string{f.NameRu, f.NameOriginal, f.NameEn} {
		if t != "" {
			return t
		}
	}
	return ""
}

func (f *kpFilm) original() string {
	for _, t := range []string{f.NameOriginal, f.NameEn} {
		if t != "" && t != f.title() {
			return t
		}
	}
	return ""
}

func (f *kpFilm) result() SearchResult {
	return SearchResult{"kinopoisk", f.kind(), f.id(), f.title(), f.original(), int(f.Year)}
}

func (c *Kinopoisk) get(path string, v any) error {
	body, _, err := httpGet(kinopoiskURL+path, map[string]string{"X-API-KEY": c.key})
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func (c *Kinopoisk) Search(kind, query string, year int) ([]SearchResult, error) {
	var resp struct {
		Films []kpFilm `json:"films"`
	}
	if err := c.get("/api/v2.1/films/search-by-keyword?keyword="+url.QueryEscape(query), &resp); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, f := range resp.Films {
		if f.kind() == kind && f.title() != "" {
			out = append(out, f.result())
		}
	}
	return out, nil
}

func (c *Kinopoisk) ByIMDb(imdb string) (*SearchResult, error) {
	var resp struct {
		Items []kpFilm `json:"items"`
	}
	if err := c.get("/api/v2.2/films?imdbId="+url.QueryEscape(imdb), &resp); err != nil {
		return nil, err
	}
	if len(resp.Items) == 0 {
		return nil, errNotFound
	}
	r := resp.Items[0].result()
	return &r, nil
}

func (c *Kinopoisk) film(id string) (*kpFilm, error) {
	c.mu.Lock()
	f := c.films[id]
	c.mu.Unlock()
	if f != nil {
		return f, nil
	}
	f = &kpFilm{}
	if err := c.get("/api/v2.2/films/"+id, f); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.films[id] = f
	c.mu.Unlock()
	return f, nil
}

// Lookup tells whether a Kinopoisk number is a movie or a series.
func (c *Kinopoisk) Lookup(id string) (*SearchResult, error) {
	f, err := c.film(id)
	if err != nil {
		return nil, err
	}
	r := f.result()
	return &r, nil
}

var kpMPAA = map[string]string{"g": "G", "pg": "PG", "pg13": "PG-13", "r": "R", "nc17": "NC-17"}

func (f *kpFilm) ids() map[string]string {
	ids := map[string]string{"kinopoisk": f.id()}
	if f.IMDbID != "" {
		ids["imdb"] = f.IMDbID
	}
	return ids
}

func (f *kpFilm) genres() []string {
	var out []string
	for _, g := range f.Genres {
		out = append(out, g.Genre)
	}
	return out
}

// staff returns directors, writers and actors; a film without this list is
// still usable, so errors are ignored.
func (c *Kinopoisk) staff(id string) (directors, writers []string, cast []Person) {
	var list []struct {
		NameRu      string `json:"nameRu"`
		NameEn      string `json:"nameEn"`
		Description string `json:"description"`
		PosterURL   string `json:"posterUrl"`
		Profession  string `json:"professionKey"`
	}
	if c.get("/api/v1/staff?filmId="+id, &list) != nil {
		return
	}
	for _, p := range list {
		name := p.NameRu
		if name == "" {
			name = p.NameEn
		}
		switch p.Profession {
		case "DIRECTOR":
			directors = append(directors, name)
		case "WRITER":
			writers = append(writers, name)
		case "ACTOR":
			cast = append(cast, Person{name, p.Description, p.PosterURL})
		}
	}
	return
}

func (c *Kinopoisk) Movie(id string) (*Movie, error) {
	f, err := c.film(id)
	if err != nil {
		return nil, err
	}
	m := &Movie{
		Source: "kinopoisk", ID: f.id(), IDs: f.ids(),
		Title: f.title(), OriginalTitle: f.original(), Overview: f.Description, Tagline: f.Slogan,
		Year: int(f.Year), Runtime: int(f.FilmLength), Rating: f.Rating, MPAA: kpMPAA[f.RatingMPAA],
		Genres: f.genres(), Poster: f.PosterURL, Backdrop: f.CoverURL,
	}
	for _, co := range f.Countries {
		m.Countries = append(m.Countries, co.Country)
	}
	m.Directors, m.Writers, m.Cast = c.staff(id)
	return m, nil
}

func (c *Kinopoisk) Show(id string) (*Show, error) {
	f, err := c.film(id)
	if err != nil {
		return nil, err
	}
	s := &Show{
		Source: "kinopoisk", ID: f.id(), IDs: f.ids(),
		Title: f.title(), OriginalTitle: f.original(), Overview: f.Description,
		Year: int(f.Year), Rating: f.Rating, MPAA: kpMPAA[f.RatingMPAA],
		Genres: f.genres(), Poster: f.PosterURL, Backdrop: f.CoverURL,
	}
	_, _, s.Cast = c.staff(id)
	return s, nil
}

func (c *Kinopoisk) Season(showID string, n int) (*Season, error) {
	var resp struct {
		Items []struct {
			Number   int `json:"number"`
			Episodes []struct {
				Number   int    `json:"episodeNumber"`
				NameRu   string `json:"nameRu"`
				NameEn   string `json:"nameEn"`
				Synopsis string `json:"synopsis"`
				Released string `json:"releaseDate"`
			} `json:"episodes"`
		} `json:"items"`
	}
	if err := c.get("/api/v2.2/films/"+showID+"/seasons", &resp); err != nil {
		return nil, err
	}
	for _, item := range resp.Items {
		if item.Number != n {
			continue
		}
		s := &Season{Number: n}
		for _, e := range item.Episodes {
			title := e.NameRu
			if title == "" {
				title = e.NameEn
			}
			s.Episodes = append(s.Episodes, Episode{Number: e.Number, Title: title, Overview: e.Synopsis, Aired: e.Released})
		}
		return s, nil
	}
	return nil, errNotFound
}

// kinopoiskToIMDb finds the IMDb number for a Kinopoisk one through
// Wikidata; used when there is no Kinopoisk key.
func kinopoiskToIMDb(kpID string) (string, error) {
	var search struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	q := url.Values{"action": {"query"}, "list": {"search"}, "format": {"json"},
		"srsearch": {"haswbstatement:P2603=" + kpID}}
	body, _, err := httpGet(wikidataURL+"?"+q.Encode(), nil)
	if err == nil {
		err = json.Unmarshal(body, &search)
	}
	if err != nil {
		return "", fmt.Errorf("Wikidata: %w", err)
	}
	if len(search.Query.Search) == 0 {
		return "", fmt.Errorf("Kinopoisk %s is not in Wikidata — enter the IMDb number instead", kpID)
	}
	entity := search.Query.Search[0].Title

	var ents struct {
		Entities map[string]struct {
			Claims map[string][]struct {
				Mainsnak struct {
					Datavalue struct {
						Value json.RawMessage `json:"value"`
					} `json:"datavalue"`
				} `json:"mainsnak"`
			} `json:"claims"`
		} `json:"entities"`
	}
	q = url.Values{"action": {"wbgetentities"}, "ids": {entity}, "props": {"claims"}, "format": {"json"}}
	body, _, err = httpGet(wikidataURL+"?"+q.Encode(), nil)
	if err == nil {
		err = json.Unmarshal(body, &ents)
	}
	if err != nil {
		return "", fmt.Errorf("Wikidata: %w", err)
	}
	if cl := ents.Entities[entity].Claims["P345"]; len(cl) > 0 {
		var imdb string
		json.Unmarshal(cl[0].Mainsnak.Datavalue.Value, &imdb)
		if imdb != "" {
			return imdb, nil
		}
	}
	return "", fmt.Errorf("Wikidata has no IMDb number for Kinopoisk %s", kpID)
}
