package main

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	kindMovie = "movie"
	kindTV    = "tv"

	userAgent = "Mozilla/5.0 (compatible; MediaKeeper/1.0)"
)

var (
	errNotFound = errors.New("not found")
	errBusy     = errors.New("too many requests, try again in a moment")
)

// unavailable means the whole service cannot be used right now: no network,
// no valid key, quota exhausted. Such a provider is switched off for the
// rest of the session instead of failing on every file.
type unavailable struct{ reason string }

func (e *unavailable) Error() string { return e.reason }

// LocalTitle is the Russian title kept next to the main one when the data
// itself is in another language. Known is set once it has been looked up,
// found or not, so that the lookup is not repeated on every run.
type LocalTitle struct {
	Title string
	Known bool
}

type Person struct {
	Name, Role, Thumb string
}

// Movie, Show, Season and Episode are provider-neutral: every source
// converts its own answer into them. Image fields hold full URLs.
type Movie struct {
	Source, ID string
	IDs        map[string]string // tmdb, imdb, kinopoisk, ...
	FromShow   bool              // the source lists it as a series
	Local      LocalTitle

	Title, OriginalTitle, Overview, Tagline string
	Released                                string // YYYY-MM-DD
	Year, Runtime                           int
	Rating                                  float64 // 0..10
	MPAA, Collection                        string
	Genres, Countries, Studios              []string
	Directors, Writers                      []string
	Cast                                    []Person
	Poster, Backdrop                        string
}

// SourceKind tells how the source itself files this entry: a miniseries
// kept as a single file is a Movie here but a series there.
func (m *Movie) SourceKind() string {
	if m.FromShow {
		return kindTV
	}
	return kindMovie
}

// AsMovie presents a series as a movie, for a series stored in one file.
func (s *Show) AsMovie() *Movie {
	return &Movie{
		Source: s.Source, ID: s.ID, IDs: s.IDs, FromShow: true, Local: s.Local,
		Title: s.Title, OriginalTitle: s.OriginalTitle, Overview: s.Overview,
		Released: s.Premiered, Year: s.Year, Rating: s.Rating, MPAA: s.MPAA,
		Genres: s.Genres, Studios: s.Studios, Cast: s.Cast, Poster: s.Poster, Backdrop: s.Backdrop,
	}
}

type Show struct {
	Source, ID string
	IDs        map[string]string
	Local      LocalTitle

	Title, OriginalTitle, Overview string
	Premiered, Status, MPAA        string
	Year                           int
	Rating                         float64
	Genres, Studios                []string
	Cast                           []Person
	Poster, Backdrop               string
	SeasonPosters                  map[int]string
}

type Episode struct {
	Number                 int
	Title, Overview, Aired string
	Thumb                  string
	Rating                 float64
	Runtime                int
	Directors, Writers     []string
	Guests                 []Person
}

type Season struct {
	Number   int
	Poster   string
	Episodes []Episode
}

func (s *Season) Episode(n int) *Episode {
	if s == nil {
		return nil
	}
	for i := range s.Episodes {
		if s.Episodes[i].Number == n {
			return &s.Episodes[i]
		}
	}
	return nil
}

// SearchResult is a movie or a series in a candidate list.
type SearchResult struct {
	Source, Kind, ID     string
	Title, OriginalTitle string
	Year                 int
}

// Provider is one metadata source. Anything a source cannot do (TVMaze has
// no movies, Letterboxd no series) returns errNotFound.
type Provider interface {
	Key() string  // stable id: "tmdb"
	Name() string // shown to the user: "TMDB"
	Search(kind, query string, year int) ([]SearchResult, error)
	ByIMDb(imdb string) (*SearchResult, error)
	Movie(id string) (*Movie, error)
	Show(id string) (*Show, error)
	Season(showID string, n int) (*Season, error)
}

// Lookuper is implemented by sources whose IDs do not tell a movie from a
// series, or that accept links.
type Lookuper interface {
	Lookup(ref string) (*SearchResult, error)
}

// Hub holds the sources in priority order and hides the ones that failed.
type Hub struct {
	all    []Provider
	mu     sync.Mutex
	down   map[string]bool
	notify func(name, reason string)
}

func NewHub(providers []Provider, notify func(name, reason string)) *Hub {
	return &Hub{all: providers, down: map[string]bool{}, notify: notify}
}

func (h *Hub) Active() []Provider {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Provider
	for _, p := range h.all {
		if !h.down[p.Key()] {
			out = append(out, p)
		}
	}
	return out
}

func (h *Hub) Get(key string) Provider {
	for _, p := range h.Active() {
		if p.Key() == key {
			return p
		}
	}
	return nil
}

// check switches a provider off when its error says the service as a whole
// is unusable, and passes the error through.
func (h *Hub) check(p Provider, err error) error {
	var un *unavailable
	if !errors.As(err, &un) {
		return err
	}
	h.mu.Lock()
	first := !h.down[p.Key()]
	h.down[p.Key()] = true
	h.mu.Unlock()
	if first && h.notify != nil {
		h.notify(p.Name(), un.reason)
	}
	return fmt.Errorf("%s: %w", p.Name(), err)
}

// Found is what one source returned for a query.
type Found struct {
	Provider Provider
	Results  []SearchResult
}

// SearchAll asks every active source at once for every given kind; the
// results of the first kind come first. Sources that failed or found
// nothing are simply absent from the answer. A year only reorders the
// list: entries of that year go to the top.
func (h *Hub) SearchAll(kinds []string, query string, year int) []Found {
	type slot struct {
		rs  []SearchResult
		err error
	}
	active := h.Active()
	slots := make([][]slot, len(active))
	var wg sync.WaitGroup
	for i, p := range active {
		slots[i] = make([]slot, len(kinds))
		for k, kind := range kinds {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rs, err := p.Search(kind, query, year)
				if err == nil && len(rs) == 0 && year > 0 {
					rs, err = p.Search(kind, query, 0)
				}
				slots[i][k] = slot{rs, err}
			}()
		}
	}
	wg.Wait()
	var out []Found
	for i, p := range active {
		var rs []SearchResult
		for _, s := range slots[i] {
			if h.check(p, s.err) == nil {
				rs = append(rs, s.rs...)
			}
		}
		if h.Get(p.Key()) == nil || len(rs) == 0 {
			continue
		}
		if year > 0 {
			// Stable, so the expected kind still leads within each half.
			sort.SliceStable(rs, func(a, b int) bool { return nearYear(rs[a], year) && !nearYear(rs[b], year) })
			sort.SliceStable(rs, func(a, b int) bool { return rs[a].Kind == kinds[0] && rs[b].Kind != kinds[0] })
		}
		out = append(out, Found{p, rs})
	}
	// Sources that found the expected kind go before the ones that only
	// have look-alikes of the other kind.
	sort.SliceStable(out, func(a, b int) bool {
		return out[a].Results[0].Kind == kinds[0] && out[b].Results[0].Kind != kinds[0]
	})
	return out
}

func nearYear(r SearchResult, year int) bool {
	d := r.Year - year
	return d >= -1 && d <= 1
}

// Match is the catalogue entry chosen for a unit: exactly one field is set.
type Match struct {
	Movie *Movie
	Show  *Show
}

// Load fetches full details for a search result. IMDb knows only a title
// and a poster and Wikidata only the IMDb number, so their results are
// looked up by that number in the other sources first.
func (h *Hub) Load(r SearchResult) (*Match, error) {
	if r.Source == "imdb" || r.Source == "wikidata" {
		return h.LoadByIMDb(r.ID)
	}
	p := h.Get(r.Source)
	if p == nil {
		return nil, fmt.Errorf("source %s is not available now", r.Source)
	}
	return h.load(p, r)
}

func (h *Hub) load(p Provider, r SearchResult) (*Match, error) {
	if r.Kind == kindTV {
		s, err := p.Show(r.ID)
		if err != nil {
			return nil, h.check(p, err)
		}
		return &Match{Show: s}, nil
	}
	m, err := p.Movie(r.ID)
	if err != nil {
		return nil, h.check(p, err)
	}
	return &Match{Movie: m}, nil
}

// LoadByIMDb takes the details from the first source, in priority order,
// that knows this IMDb number.
func (h *Hub) LoadByIMDb(imdb string) (*Match, error) {
	var thin Provider
	for _, p := range h.Active() {
		if p.Key() == "imdb" {
			thin = p
			continue
		}
		if m := h.tryIMDb(p, imdb); m != nil {
			return m, nil
		}
	}
	if thin != nil {
		if m := h.tryIMDb(thin, imdb); m != nil {
			return m, nil
		}
	}
	return nil, fmt.Errorf("%s: %w in any source", imdb, errNotFound)
}

func (h *Hub) tryIMDb(p Provider, imdb string) *Match {
	r, err := p.ByIMDb(imdb)
	if h.check(p, err) != nil || r == nil {
		return nil
	}
	m, err := h.load(p, *r)
	if err != nil {
		return nil
	}
	return m
}

var (
	httpClient     = &http.Client{Timeout: 20 * time.Second}
	downloadClient = &http.Client{Timeout: 3 * time.Minute}
)

// httpGet returns the body and the final URL (after redirects) of a 200
// answer; everything else becomes errNotFound or *unavailable.
func httpGet(rawURL string, header map[string]string) ([]byte, *url.URL, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			// url.Error repeats the URL, which may carry an API key.
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			return nil, nil, &unavailable{"no connection (" + err.Error() + ")"}
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		switch {
		case err != nil:
			return nil, nil, &unavailable{"connection lost"}
		case resp.StatusCode == http.StatusOK:
			return body, resp.Request.URL, nil
		case resp.StatusCode == http.StatusTooManyRequests && attempt < 2:
			wait := 1500 * time.Millisecond
			if sec := atoi(resp.Header.Get("Retry-After")); sec > 5 {
				return nil, nil, errBusy // not worth holding the user up
			} else if sec > 0 {
				wait = time.Duration(sec) * time.Second
			}
			time.Sleep(wait)
		case resp.StatusCode == http.StatusTooManyRequests:
			// Throttled for a moment: this request is lost, the source is not.
			return nil, nil, errBusy
		case resp.StatusCode == http.StatusNotFound:
			return nil, nil, errNotFound
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, nil, &unavailable{"API key rejected (401)"}
		case resp.StatusCode == http.StatusPaymentRequired:
			return nil, nil, &unavailable{"request limit reached"}
		case resp.StatusCode == http.StatusForbidden:
			return nil, nil, &unavailable{"access denied (403)"}
		default:
			return nil, nil, &unavailable{"service error: " + resp.Status}
		}
	}
}

// Download saves an image to dest via a temporary file.
func Download(imageURL, dest string) error {
	req, err := http.NewRequest(http.MethodGet, imageURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := downloadClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".mk-*")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dest)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

var reTag = regexp.MustCompile(`<[^>]*>`)

// plainText turns an HTML fragment into text.
func plainText(s string) string {
	return strings.TrimSpace(html.UnescapeString(reTag.ReplaceAllString(s, "")))
}

func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	return atoi(date[:4])
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
