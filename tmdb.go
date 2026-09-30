package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	defaultTMDBURL      = "https://api.themoviedb.org/3"
	defaultTMDBImageURL = "https://image.tmdb.org/t/p/original"
)

// TMDB is the richest source and the only one here with localized titles
// for foreign films; it needs a free key.
type TMDB struct {
	key, lang        string
	apiURL, imageURL string
}

func NewTMDB(key, lang, apiURL, imageURL string) *TMDB {
	if apiURL == "" {
		apiURL = defaultTMDBURL
	}
	if imageURL == "" {
		imageURL = defaultTMDBImageURL
	}
	return &TMDB{key: key, lang: lang,
		apiURL: strings.TrimRight(apiURL, "/"), imageURL: strings.TrimRight(imageURL, "/")}
}

func (c *TMDB) Key() string  { return "tmdb" }
func (c *TMDB) Name() string { return "TMDB" }

type tmdbNamed struct {
	Name string `json:"name"`
}

type tmdbCast struct {
	Name        string `json:"name"`
	Character   string `json:"character"`
	ProfilePath string `json:"profile_path"`
}

type tmdbCrew struct {
	Name       string `json:"name"`
	Job        string `json:"job"`
	Department string `json:"department"`
}

type tmdbResult struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseDate   string `json:"release_date"`
	Name          string `json:"name"`
	OriginalName  string `json:"original_name"`
	FirstAirDate  string `json:"first_air_date"`
}

func (r tmdbResult) result(kind string) SearchResult {
	if kind == kindTV {
		return SearchResult{"tmdb", kindTV, itoa(r.ID), r.Name, r.OriginalName, yearOf(r.FirstAirDate)}
	}
	return SearchResult{"tmdb", kindMovie, itoa(r.ID), r.Title, r.OriginalTitle, yearOf(r.ReleaseDate)}
}

func (c *TMDB) get(path string, q url.Values, v any) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("language", c.lang)
	// A v4 token is a JWT and goes into the header, a v3 key into the query.
	var header map[string]string
	if strings.HasPrefix(c.key, "eyJ") {
		header = map[string]string{"Authorization": "Bearer " + c.key}
	} else {
		q.Set("api_key", c.key)
	}
	body, _, err := httpGet(c.apiURL+path+"?"+q.Encode(), header)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func (c *TMDB) image(path string) string {
	if path == "" {
		return ""
	}
	return c.imageURL + path
}

func (c *TMDB) Search(kind, query string, year int) ([]SearchResult, error) {
	q := url.Values{"query": {query}}
	if year > 0 {
		if kind == kindTV {
			q.Set("first_air_date_year", strconv.Itoa(year))
		} else {
			q.Set("year", strconv.Itoa(year))
		}
	}
	var resp struct {
		Results []tmdbResult `json:"results"`
	}
	if err := c.get("/search/"+kind, q, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, r.result(kind))
	}
	return out, nil
}

func (c *TMDB) ByIMDb(imdb string) (*SearchResult, error) {
	var resp struct {
		Movies []tmdbResult `json:"movie_results"`
		TV     []tmdbResult `json:"tv_results"`
	}
	if err := c.get("/find/"+imdb, url.Values{"external_source": {"imdb_id"}}, &resp); err != nil {
		return nil, err
	}
	switch {
	case len(resp.Movies) > 0:
		r := resp.Movies[0].result(kindMovie)
		return &r, nil
	case len(resp.TV) > 0:
		r := resp.TV[0].result(kindTV)
		return &r, nil
	}
	return nil, errNotFound
}

func (c *TMDB) cast(list []tmdbCast) []Person {
	var out []Person
	for _, a := range list {
		out = append(out, Person{a.Name, a.Character, c.image(a.ProfilePath)})
	}
	return out
}

func tmdbNames(list []tmdbNamed) []string {
	var out []string
	for _, n := range list {
		out = append(out, n.Name)
	}
	return out
}

func tmdbCrewNames(crew []tmdbCrew, keep func(tmdbCrew) bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, cm := range crew {
		if keep(cm) && !seen[cm.Name] {
			seen[cm.Name] = true
			out = append(out, cm.Name)
		}
	}
	return out
}

func isDirector(cm tmdbCrew) bool { return cm.Job == "Director" }
func isWriter(cm tmdbCrew) bool   { return cm.Department == "Writing" }

func (c *TMDB) Movie(id string) (*Movie, error) {
	var r struct {
		tmdbResult
		IMDbID       string      `json:"imdb_id"`
		Overview     string      `json:"overview"`
		Tagline      string      `json:"tagline"`
		Runtime      int         `json:"runtime"`
		VoteAverage  float64     `json:"vote_average"`
		PosterPath   string      `json:"poster_path"`
		BackdropPath string      `json:"backdrop_path"`
		Genres       []tmdbNamed `json:"genres"`
		Companies    []tmdbNamed `json:"production_companies"`
		Countries    []tmdbNamed `json:"production_countries"`
		Collection   *tmdbNamed  `json:"belongs_to_collection"`
		Credits      struct {
			Cast []tmdbCast `json:"cast"`
			Crew []tmdbCrew `json:"crew"`
		} `json:"credits"`
		ReleaseDates struct {
			Results []struct {
				Country      string `json:"iso_3166_1"`
				ReleaseDates []struct {
					Certification string `json:"certification"`
				} `json:"release_dates"`
			} `json:"results"`
		} `json:"release_dates"`
	}
	if err := c.get("/movie/"+id, url.Values{"append_to_response": {"credits,release_dates"}}, &r); err != nil {
		return nil, err
	}
	m := &Movie{
		Source: "tmdb", ID: itoa(r.ID), IDs: map[string]string{"tmdb": itoa(r.ID)},
		Title: r.Title, OriginalTitle: r.OriginalTitle, Overview: r.Overview, Tagline: r.Tagline,
		Released: r.ReleaseDate, Year: yearOf(r.ReleaseDate), Runtime: r.Runtime, Rating: r.VoteAverage,
		Genres: tmdbNames(r.Genres), Countries: tmdbNames(r.Countries), Studios: tmdbNames(r.Companies),
		Directors: tmdbCrewNames(r.Credits.Crew, isDirector), Writers: tmdbCrewNames(r.Credits.Crew, isWriter),
		Cast: c.cast(r.Credits.Cast), Poster: c.image(r.PosterPath), Backdrop: c.image(r.BackdropPath),
	}
	if r.IMDbID != "" {
		m.IDs["imdb"] = r.IMDbID
	}
	if r.Collection != nil {
		m.Collection = r.Collection.Name
	}
	for _, rel := range r.ReleaseDates.Results {
		if rel.Country != "US" {
			continue
		}
		for _, d := range rel.ReleaseDates {
			if d.Certification != "" {
				m.MPAA = d.Certification
				break
			}
		}
	}
	return m, nil
}

func (c *TMDB) Show(id string) (*Show, error) {
	var r struct {
		tmdbResult
		Overview     string      `json:"overview"`
		Status       string      `json:"status"`
		VoteAverage  float64     `json:"vote_average"`
		PosterPath   string      `json:"poster_path"`
		BackdropPath string      `json:"backdrop_path"`
		Genres       []tmdbNamed `json:"genres"`
		Networks     []tmdbNamed `json:"networks"`
		Credits      struct {
			Cast []tmdbCast `json:"cast"`
		} `json:"credits"`
		ExternalIDs struct {
			IMDbID string `json:"imdb_id"`
			TVDBID int    `json:"tvdb_id"`
		} `json:"external_ids"`
		ContentRatings struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Rating  string `json:"rating"`
			} `json:"results"`
		} `json:"content_ratings"`
		Seasons []struct {
			Number     int    `json:"season_number"`
			PosterPath string `json:"poster_path"`
		} `json:"seasons"`
	}
	q := url.Values{"append_to_response": {"credits,external_ids,content_ratings"}}
	if err := c.get("/tv/"+id, q, &r); err != nil {
		return nil, err
	}
	s := &Show{
		Source: "tmdb", ID: itoa(r.ID), IDs: map[string]string{"tmdb": itoa(r.ID)},
		Title: r.Name, OriginalTitle: r.OriginalName, Overview: r.Overview,
		Premiered: r.FirstAirDate, Year: yearOf(r.FirstAirDate), Status: r.Status, Rating: r.VoteAverage,
		Genres: tmdbNames(r.Genres), Studios: tmdbNames(r.Networks), Cast: c.cast(r.Credits.Cast),
		Poster: c.image(r.PosterPath), Backdrop: c.image(r.BackdropPath), SeasonPosters: map[int]string{},
	}
	if r.ExternalIDs.IMDbID != "" {
		s.IDs["imdb"] = r.ExternalIDs.IMDbID
	}
	if r.ExternalIDs.TVDBID > 0 {
		s.IDs["tvdb"] = itoa(r.ExternalIDs.TVDBID)
	}
	for _, cr := range r.ContentRatings.Results {
		if cr.Country == "US" {
			s.MPAA = cr.Rating
		}
	}
	for _, sn := range r.Seasons {
		if sn.PosterPath != "" {
			s.SeasonPosters[sn.Number] = c.image(sn.PosterPath)
		}
	}
	return s, nil
}

func (c *TMDB) Season(showID string, n int) (*Season, error) {
	var r struct {
		PosterPath string `json:"poster_path"`
		Episodes   []struct {
			Number      int        `json:"episode_number"`
			Name        string     `json:"name"`
			Overview    string     `json:"overview"`
			AirDate     string     `json:"air_date"`
			StillPath   string     `json:"still_path"`
			VoteAverage float64    `json:"vote_average"`
			Runtime     int        `json:"runtime"`
			Crew        []tmdbCrew `json:"crew"`
			GuestStars  []tmdbCast `json:"guest_stars"`
		} `json:"episodes"`
	}
	if err := c.get(fmt.Sprintf("/tv/%s/season/%d", showID, n), nil, &r); err != nil {
		return nil, err
	}
	s := &Season{Number: n, Poster: c.image(r.PosterPath)}
	for _, e := range r.Episodes {
		s.Episodes = append(s.Episodes, Episode{
			Number: e.Number, Title: e.Name, Overview: e.Overview, Aired: e.AirDate,
			Thumb: c.image(e.StillPath), Rating: e.VoteAverage, Runtime: e.Runtime,
			Directors: tmdbCrewNames(e.Crew, isDirector),
			Writers:   tmdbCrewNames(e.Crew, func(cm tmdbCrew) bool { return cm.Job == "Writer" || isWriter(cm) }),
			Guests:    c.cast(e.GuestStars),
		})
	}
	return s, nil
}
