package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"sync"
)

var imdbURL = "https://v3.sg.media-imdb.com/suggestion/x/"

// IMDb uses the keyless search-suggestion service of imdb.com. It finds
// titles in any language but knows only the name, year, poster and two
// actors, so the Hub takes the details from another source by IMDb number
// whenever one is available.
type IMDb struct {
	mu    sync.Mutex
	cache map[string][]imdbItem
}

func NewIMDb() *IMDb { return &IMDb{cache: map[string][]imdbItem{}} }

func (*IMDb) Key() string  { return "imdb" }
func (*IMDb) Name() string { return "IMDb" }

type imdbItem struct {
	ID    string `json:"id"`
	Title string `json:"l"`
	Type  string `json:"qid"`
	Stars string `json:"s"`
	Year  int    `json:"y"`
	Image struct {
		URL string `json:"imageUrl"`
	} `json:"i"`
}

func (i imdbItem) kind() string {
	switch i.Type {
	case "movie", "tvMovie", "video", "short":
		return kindMovie
	case "tvSeries", "tvMiniSeries":
		return kindTV
	}
	return ""
}

// suggest is cached: every search asks for movies and for series, and both
// come in the same answer.
func (c *IMDb) suggest(query string) ([]imdbItem, error) {
	query = strings.ToLower(query)
	c.mu.Lock()
	defer c.mu.Unlock()
	if items, ok := c.cache[query]; ok {
		return items, nil
	}
	body, _, err := httpGet(imdbURL+url.PathEscape(query)+".json", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		D []imdbItem `json:"d"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	var out []imdbItem
	for _, it := range resp.D {
		if strings.HasPrefix(it.ID, "tt") && it.kind() != "" {
			out = append(out, it)
		}
	}
	c.cache[query] = out
	return out, nil
}

func (c *IMDb) Search(kind, query string, year int) ([]SearchResult, error) {
	items, err := c.suggest(query)
	if err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, it := range items {
		if it.kind() == kind {
			out = append(out, SearchResult{Source: "imdb", Kind: kind, ID: it.ID, Title: it.Title, Year: it.Year})
		}
	}
	return out, nil
}

func (c *IMDb) byID(id string) (*imdbItem, error) {
	items, err := c.suggest(id)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.ID == id {
			return &it, nil
		}
	}
	return nil, errNotFound
}

func (c *IMDb) ByIMDb(imdb string) (*SearchResult, error) {
	it, err := c.byID(imdb)
	if err != nil {
		return nil, err
	}
	return &SearchResult{Source: "imdb", Kind: it.kind(), ID: it.ID, Title: it.Title, Year: it.Year}, nil
}

func (i imdbItem) cast() []Person {
	var out []Person
	for _, name := range splitList(i.Stars) {
		out = append(out, Person{Name: name})
	}
	return out
}

func (c *IMDb) Movie(id string) (*Movie, error) {
	it, err := c.byID(id)
	if err != nil {
		return nil, err
	}
	return &Movie{Source: "imdb", ID: id, IDs: map[string]string{"imdb": id},
		Title: it.Title, Year: it.Year, Cast: it.cast(), Poster: it.Image.URL}, nil
}

func (c *IMDb) Show(id string) (*Show, error) {
	it, err := c.byID(id)
	if err != nil {
		return nil, err
	}
	return &Show{Source: "imdb", ID: id, IDs: map[string]string{"imdb": id},
		Title: it.Title, Year: it.Year, Cast: it.cast(), Poster: it.Image.URL}, nil
}

func (*IMDb) Season(string, int) (*Season, error) { return nil, errNotFound }
