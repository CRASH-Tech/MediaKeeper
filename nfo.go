package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"regexp"
)

const nfoHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

type nfoActor struct {
	Name  string `xml:"name"`
	Role  string `xml:"role,omitempty"`
	Order int    `xml:"order"`
	Thumb string `xml:"thumb,omitempty"`
}

type nfoUID struct {
	Type    string `xml:"type,attr"`
	Default bool   `xml:"default,attr,omitempty"`
	Value   string `xml:",chardata"`
}

type nfoThumb struct {
	Aspect string `xml:"aspect,attr"`
	URL    string `xml:",chardata"`
}

type nfoSet struct {
	Name string `xml:"name"`
}

// nfoLocal is MediaKeeper's own element: media centers ignore it, the
// built-in DLNA server shows it next to the main title.
type nfoLocal struct {
	Lang  string `xml:"lang,attr"`
	Title string `xml:",chardata"`
}

func localNFO(l LocalTitle) *nfoLocal {
	if !l.Known {
		return nil
	}
	return &nfoLocal{Lang: "ru", Title: l.Title}
}

type movieNFO struct {
	XMLName       xml.Name   `xml:"movie"`
	Title         string     `xml:"title"`
	OriginalTitle string     `xml:"originaltitle,omitempty"`
	Localized     *nfoLocal  `xml:"localizedtitle"`
	Rating        float64    `xml:"rating,omitempty"`
	Year          int        `xml:"year,omitempty"`
	Premiered     string     `xml:"premiered,omitempty"`
	Plot          string     `xml:"plot,omitempty"`
	Tagline       string     `xml:"tagline,omitempty"`
	Runtime       int        `xml:"runtime,omitempty"`
	MPAA          string     `xml:"mpaa,omitempty"`
	UniqueIDs     []nfoUID   `xml:"uniqueid"`
	TMDBID        string     `xml:"tmdbid,omitempty"`
	IMDbID        string     `xml:"imdbid,omitempty"`
	Genres        []string   `xml:"genre"`
	Countries     []string   `xml:"country"`
	Studios       []string   `xml:"studio"`
	Set           *nfoSet    `xml:"set,omitempty"`
	Directors     []string   `xml:"director"`
	Writers       []string   `xml:"credits"`
	Actors        []nfoActor `xml:"actor"`
	Thumbs        []nfoThumb `xml:"thumb"`
}

type showNFO struct {
	XMLName       xml.Name   `xml:"tvshow"`
	Title         string     `xml:"title"`
	OriginalTitle string     `xml:"originaltitle,omitempty"`
	Localized     *nfoLocal  `xml:"localizedtitle"`
	Rating        float64    `xml:"rating,omitempty"`
	Year          int        `xml:"year,omitempty"`
	Premiered     string     `xml:"premiered,omitempty"`
	Plot          string     `xml:"plot,omitempty"`
	MPAA          string     `xml:"mpaa,omitempty"`
	Status        string     `xml:"status,omitempty"`
	UniqueIDs     []nfoUID   `xml:"uniqueid"`
	TMDBID        string     `xml:"tmdbid,omitempty"`
	IMDbID        string     `xml:"imdb_id,omitempty"`
	Genres        []string   `xml:"genre"`
	Studios       []string   `xml:"studio"`
	Actors        []nfoActor `xml:"actor"`
	Thumbs        []nfoThumb `xml:"thumb"`
}

type episodeNFO struct {
	XMLName   xml.Name   `xml:"episodedetails"`
	Title     string     `xml:"title"`
	ShowTitle string     `xml:"showtitle"`
	Season    int        `xml:"season"`
	Episode   int        `xml:"episode"`
	Rating    float64    `xml:"rating,omitempty"`
	Aired     string     `xml:"aired,omitempty"`
	Plot      string     `xml:"plot,omitempty"`
	Runtime   int        `xml:"runtime,omitempty"`
	Directors []string   `xml:"director"`
	Writers   []string   `xml:"credits"`
	Actors    []nfoActor `xml:"actor"`
	Thumbs    []nfoThumb `xml:"thumb"`
}

// marshalNFO writes the documents after a comment that records where the
// data came from; the next run reads it back instead of searching again.
func marshalNFO(source, id, kind string, docs ...any) []byte {
	var b bytes.Buffer
	b.WriteString(nfoHeader)
	if source != "" {
		fmt.Fprintf(&b, "<!-- mediakeeper source=%q id=%q kind=%q -->\n", source, id, kind)
	}
	for _, d := range docs {
		out, err := xml.MarshalIndent(d, "", "  ")
		if err != nil {
			panic(err) // plain structs of strings and numbers cannot fail
		}
		b.Write(out)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

var reNFOLocal = regexp.MustCompile(`<localizedtitle lang="ru">([^<]*)</localizedtitle>`)

var reNFOMarker = regexp.MustCompile(`<!-- mediakeeper source="([^"]+)" id="([^"]+)" kind="([^"]+)" -->`)

func MovieNFO(m *Movie) []byte {
	n := movieNFO{
		Title: m.Title, Rating: round1(m.Rating), Year: m.Year, Premiered: m.Released,
		Plot: m.Overview, Tagline: m.Tagline, Runtime: m.Runtime, MPAA: m.MPAA,
		UniqueIDs: uniqueIDs(m.IDs), TMDBID: m.IDs["tmdb"], IMDbID: m.IDs["imdb"],
		Genres: m.Genres, Countries: m.Countries, Studios: m.Studios,
		Directors: m.Directors, Writers: m.Writers,
		Actors: actors(m.Cast), Thumbs: thumbs(m.Poster, "poster"),
	}
	n.Localized = localNFO(m.Local)
	if m.OriginalTitle != m.Title {
		n.OriginalTitle = m.OriginalTitle
	}
	if m.Collection != "" {
		n.Set = &nfoSet{Name: m.Collection}
	}
	return marshalNFO(m.Source, m.ID, m.SourceKind(), n)
}

func ShowNFO(s *Show) []byte {
	n := showNFO{
		Title: s.Title, Rating: round1(s.Rating), Year: s.Year, Premiered: s.Premiered,
		Plot: s.Overview, MPAA: s.MPAA, Status: s.Status,
		UniqueIDs: uniqueIDs(s.IDs), TMDBID: s.IDs["tmdb"], IMDbID: s.IDs["imdb"],
		Genres: s.Genres, Studios: s.Studios,
		Actors: actors(s.Cast), Thumbs: thumbs(s.Poster, "poster"),
	}
	n.Localized = localNFO(s.Local)
	if s.OriginalTitle != s.Title {
		n.OriginalTitle = s.OriginalTitle
	}
	return marshalNFO(s.Source, s.ID, kindTV, n)
}

// EpisodeNFO writes one <episodedetails> per episode, so a file holding
// several episodes is described by several root elements.
func EpisodeNFO(s *Show, season int, numbers []int, eps []*Episode) []byte {
	docs := make([]any, 0, len(numbers))
	for i, num := range numbers {
		n := episodeNFO{ShowTitle: s.Title, Season: season, Episode: num}
		if e := eps[i]; e != nil {
			n.Title = e.Title
			n.Rating = round1(e.Rating)
			n.Aired = e.Aired
			n.Plot = e.Overview
			n.Runtime = e.Runtime
			n.Directors = e.Directors
			n.Writers = e.Writers
			n.Actors = actors(e.Guests)
			n.Thumbs = thumbs(e.Thumb, "thumb")
		}
		docs = append(docs, n)
	}
	return marshalNFO("", "", "", docs...)
}

// uniqueIDs lists the catalogue numbers in a fixed order; the first one
// present is marked as the default.
func uniqueIDs(ids map[string]string) []nfoUID {
	var out []nfoUID
	for _, typ := range []string{"tmdb", "imdb", "tvdb", "kinopoisk", "tvmaze"} {
		if v := ids[typ]; v != "" {
			out = append(out, nfoUID{Type: typ, Default: len(out) == 0, Value: v})
		}
	}
	return out
}

func thumbs(url, aspect string) []nfoThumb {
	if url == "" {
		return nil
	}
	return []nfoThumb{{Aspect: aspect, URL: url}}
}

func actors(cast []Person) []nfoActor {
	const maxActors = 20
	var out []nfoActor
	for i, a := range cast {
		if i == maxActors {
			break
		}
		out = append(out, nfoActor{Name: a.Name, Role: a.Role, Order: i, Thumb: a.Thumb})
	}
	return out
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
