package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// Wikidata knows the titles of films and series in many languages together
// with their IMDb numbers. It is what makes a Russian title findable when
// every other connected source is English-only: the match is found here by
// its Russian name and the details are then loaded by IMDb number (see
// Hub.Load). No key.
type Wikidata struct {
	mu    sync.Mutex
	cache map[string][]SearchResult
}

func NewWikidata() *Wikidata { return &Wikidata{cache: map[string][]SearchResult{}} }

func (*Wikidata) Key() string  { return "wikidata" }
func (*Wikidata) Name() string { return "Wikidata" }

// Classes (P31) that mean "a series"; everything else with an IMDb title
// number is treated as a movie.
var wikidataTV = map[string]bool{
	"Q5398426":   true, // television series
	"Q1259759":   true, // miniseries
	"Q581714":    true, // animated series
	"Q63952888":  true, // anime television series
	"Q117467246": true, // animated television series
	"Q15416":     true, // television program
	"Q526877":    true, // web series
}

// One SPARQL request does it all: the label search of wikidata.org, then
// the IMDb number, class, year and both labels of every hit. (The plain
// API needs two requests per search and cuts anonymous clients off after
// four in a row.)
var wikidataSPARQL = "https://query.wikidata.org/sparql"

const wikidataQuery = `SELECT ?ord ?imdb ?ru ?en (MIN(YEAR(?d)) AS ?year)
  (GROUP_CONCAT(DISTINCT STRAFTER(STR(?cls), "entity/"); separator=",") AS ?classes) WHERE {
  SERVICE wikibase:mwapi {
    bd:serviceParam wikibase:endpoint "www.wikidata.org"; wikibase:api "EntitySearch";
      mwapi:search "%s"; mwapi:language "%s"; mwapi:limit 12 .
    ?item wikibase:apiOutputItem mwapi:item. ?ord wikibase:apiOrdinal true.
  }
  ?item wdt:P345 ?imdb.
  OPTIONAL { ?item wdt:P31 ?cls }
  OPTIONAL { ?item wdt:P577|wdt:P580 ?d }
  OPTIONAL { ?item rdfs:label ?ru FILTER(LANG(?ru) = "ru") }
  OPTIONAL { ?item rdfs:label ?en FILTER(LANG(?en) = "en") }
} GROUP BY ?ord ?imdb ?ru ?en ORDER BY ?ord`

// Things that have an IMDb title number but are not what is searched for.
var wikidataSkip = map[string]bool{
	"Q7889":     true, // video game
	"Q21191270": true, // television series episode
	"Q3464665":  true, // television series season
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

func (c *Wikidata) Search(kind, query string, year int) ([]SearchResult, error) {
	// Cached: every search asks for movies and for series, and one answer
	// holds both.
	c.mu.Lock()
	defer c.mu.Unlock()
	all, ok := c.cache[query]
	if !ok {
		var err error
		if all, err = c.lookup(query); err != nil {
			return nil, err
		}
		c.cache[query] = all
	}
	var out []SearchResult
	for _, r := range all {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *Wikidata) lookup(query string) ([]SearchResult, error) {
	lang := "en"
	if hasCyrillic(query) {
		lang = "ru"
	}
	literal := strings.NewReplacer(`\`, " ", `"`, " ", "\n", " ").Replace(query)
	q := url.Values{"query": {fmt.Sprintf(wikidataQuery, literal, lang)}}
	body, _, err := httpGet(wikidataSPARQL+"?"+q.Encode(), map[string]string{"Accept": "application/sparql-results+json"})
	if err != nil {
		return nil, err
	}
	type value struct {
		Value string `json:"value"`
	}
	var resp struct {
		Results struct {
			Bindings []struct {
				IMDb, Ru, En, Year, Classes value
			} `json:"bindings"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	var out []SearchResult
rows:
	for _, b := range resp.Results.Bindings {
		if !strings.HasPrefix(b.IMDb.Value, "tt") {
			continue // a person
		}
		r := SearchResult{Source: "wikidata", Kind: kindMovie, ID: b.IMDb.Value,
			Title: b.Ru.Value, OriginalTitle: b.En.Value, Year: atoi(b.Year.Value)}
		if lang == "en" && b.En.Value != "" {
			r.Title, r.OriginalTitle = b.En.Value, ""
		}
		if r.Title == "" {
			r.Title, r.OriginalTitle = r.OriginalTitle, ""
		}
		for _, class := range strings.Split(b.Classes.Value, ",") {
			if wikidataSkip[class] {
				continue rows
			}
			if wikidataTV[class] {
				r.Kind = kindTV
			}
		}
		if r.Title != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

var reIMDbID = regexp.MustCompile(`^tt\d+$`)

// RussianTitle returns the Russian name of the film or series with the
// given IMDb number, or "" if Wikidata has none.
func (c *Wikidata) RussianTitle(imdb string) (string, error) {
	if !reIMDbID.MatchString(imdb) {
		return "", nil
	}
	query := fmt.Sprintf(`SELECT ?ru WHERE { ?item wdt:P345 "%s"; rdfs:label ?ru. FILTER(LANG(?ru) = "ru") } LIMIT 1`, imdb)
	body, _, err := httpGet(wikidataSPARQL+"?"+url.Values{"query": {query}}.Encode(),
		map[string]string{"Accept": "application/sparql-results+json"})
	if err != nil {
		return "", err
	}
	var resp struct {
		Results struct {
			Bindings []struct {
				Ru struct {
					Value string `json:"value"`
				} `json:"ru"`
			} `json:"bindings"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Results.Bindings) == 0 {
		return "", err
	}
	return resp.Results.Bindings[0].Ru.Value, nil
}

func (*Wikidata) ByIMDb(string) (*SearchResult, error) { return nil, errNotFound }
func (*Wikidata) Movie(string) (*Movie, error)         { return nil, errNotFound }
func (*Wikidata) Show(string) (*Show, error)           { return nil, errNotFound }
func (*Wikidata) Season(string, int) (*Season, error)  { return nil, errNotFound }
