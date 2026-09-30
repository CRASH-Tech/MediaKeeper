package main

import (
	"encoding/json"
	"net/url"
	"sync"
)

// Base URLs are variables so that tests can point them at a local server.
var tvmazeURL = "https://api.tvmaze.com"

// TVMaze knows series only, in English, and needs no key.
type TVMaze struct {
	mu       sync.Mutex
	episodes map[string][]tvmEpisode
}

func NewTVMaze() *TVMaze { return &TVMaze{episodes: map[string][]tvmEpisode{}} }

func (c *TVMaze) Key() string  { return "tvmaze" }
func (c *TVMaze) Name() string { return "TVMaze" }

type tvmImage struct {
	Original string `json:"original"`
}

func (i *tvmImage) url() string {
	if i == nil {
		return ""
	}
	return i.Original
}

type tvmNamed struct {
	Name string `json:"name"`
}

type tvmShow struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	Premiered string   `json:"premiered"`
	Status    string   `json:"status"`
	Genres    []string `json:"genres"`
	Summary   string   `json:"summary"`
	Rating    struct {
		Average float64 `json:"average"`
	} `json:"rating"`
	Network    *tvmNamed `json:"network"`
	WebChannel *tvmNamed `json:"webChannel"`
	Externals  struct {
		TVDB int    `json:"thetvdb"`
		IMDb string `json:"imdb"`
	} `json:"externals"`
	Image    *tvmImage `json:"image"`
	Embedded struct {
		Cast []struct {
			Person struct {
				Name  string    `json:"name"`
				Image *tvmImage `json:"image"`
			} `json:"person"`
			Character tvmNamed `json:"character"`
		} `json:"cast"`
		Seasons []struct {
			Number int       `json:"number"`
			Image  *tvmImage `json:"image"`
		} `json:"seasons"`
	} `json:"_embedded"`
}

type tvmEpisode struct {
	Name    string `json:"name"`
	Season  int    `json:"season"`
	Number  int    `json:"number"`
	Airdate string `json:"airdate"`
	Runtime int    `json:"runtime"`
	Summary string `json:"summary"`
	Rating  struct {
		Average float64 `json:"average"`
	} `json:"rating"`
	Image *tvmImage `json:"image"`
}

func (s *tvmShow) result() SearchResult {
	return SearchResult{Source: "tvmaze", Kind: kindTV, ID: itoa(s.ID), Title: s.Name, Year: yearOf(s.Premiered)}
}

func (c *TVMaze) get(path string, v any) error {
	body, _, err := httpGet(tvmazeURL+path, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func (c *TVMaze) Search(kind, query string, year int) ([]SearchResult, error) {
	if kind != kindTV {
		return nil, nil
	}
	var resp []struct {
		Show tvmShow `json:"show"`
	}
	if err := c.get("/search/shows?q="+url.QueryEscape(query), &resp); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, r := range resp {
		out = append(out, r.Show.result())
	}
	return out, nil
}

func (c *TVMaze) ByIMDb(imdb string) (*SearchResult, error) {
	var s tvmShow
	if err := c.get("/lookup/shows?imdb="+url.QueryEscape(imdb), &s); err != nil {
		return nil, err
	}
	r := s.result()
	return &r, nil
}

func (c *TVMaze) Movie(string) (*Movie, error) { return nil, errNotFound }

func (c *TVMaze) Show(id string) (*Show, error) {
	var r tvmShow
	if err := c.get("/shows/"+id+"?embed[]=cast&embed[]=seasons", &r); err != nil {
		return nil, err
	}
	s := &Show{
		Source: "tvmaze", ID: itoa(r.ID), IDs: map[string]string{"tvmaze": itoa(r.ID)},
		Title: r.Name, Overview: plainText(r.Summary), Premiered: r.Premiered, Year: yearOf(r.Premiered),
		Status: r.Status, Rating: r.Rating.Average, Genres: r.Genres,
		Poster: r.Image.url(), SeasonPosters: map[int]string{},
	}
	if r.Externals.IMDb != "" {
		s.IDs["imdb"] = r.Externals.IMDb
	}
	if r.Externals.TVDB > 0 {
		s.IDs["tvdb"] = itoa(r.Externals.TVDB)
	}
	for _, n := range []*tvmNamed{r.Network, r.WebChannel} {
		if n != nil {
			s.Studios = append(s.Studios, n.Name)
		}
	}
	for _, a := range r.Embedded.Cast {
		s.Cast = append(s.Cast, Person{a.Person.Name, a.Character.Name, a.Person.Image.url()})
	}
	for _, sn := range r.Embedded.Seasons {
		if u := sn.Image.url(); u != "" {
			s.SeasonPosters[sn.Number] = u
		}
	}

	// The backdrop lives in a separate list; a show without one is fine.
	var images []struct {
		Type        string `json:"type"`
		Resolutions struct {
			Original struct {
				URL string `json:"url"`
			} `json:"original"`
		} `json:"resolutions"`
	}
	if c.get("/shows/"+id+"/images", &images) == nil {
		for _, img := range images {
			if img.Type == "background" {
				s.Backdrop = img.Resolutions.Original.URL
				break
			}
		}
	}
	return s, nil
}

func (c *TVMaze) Season(showID string, n int) (*Season, error) {
	c.mu.Lock()
	all, ok := c.episodes[showID]
	c.mu.Unlock()
	if !ok {
		if err := c.get("/shows/"+showID+"/episodes", &all); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.episodes[showID] = all
		c.mu.Unlock()
	}
	s := &Season{Number: n}
	for _, e := range all {
		if e.Season != n {
			continue
		}
		s.Episodes = append(s.Episodes, Episode{
			Number: e.Number, Title: e.Name, Overview: plainText(e.Summary), Aired: e.Airdate,
			Thumb: e.Image.url(), Rating: e.Rating.Average, Runtime: e.Runtime,
		})
	}
	if len(s.Episodes) == 0 {
		return nil, errNotFound
	}
	return s, nil
}
