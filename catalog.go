package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// CatItem is a playable video of the catalogue: a movie or an episode.
type CatItem struct {
	ID   string // 32 hex digits, stable while the file keeps its place
	Kind string // kindMovie or "episode"

	Path, Rel string // Rel: within its library folder
	Root      string // that library folder
	Size      int64
	ModTime   time.Time
	Subs      []string // .srt files next to the video

	Title, OriginalTitle, LocalTitle string
	Year                             int
	Date, Plot, Tagline, MPAA        string
	Rating                           float64
	Runtime                          int    // minutes, from the .nfo
	Collection                       string // the film series it belongs to
	Genres, Countries, Studios       []string
	Directors, Writers               []string
	Cast                             []Person
	IMDb, TMDB                       string
	Poster, Backdrop, Thumb          string // image files

	// Episodes only.
	Show                *CatShow
	Season              *CatSeason
	Episode, EpisodeEnd int
}

const kindEpisode = "episode"

type CatSeason struct {
	ID       string
	Number   int
	Poster   string
	Show     *CatShow
	Episodes []*CatItem
}

type CatShow struct {
	ID                               string
	Dir                              string
	Title, OriginalTitle, LocalTitle string
	Year                             int
	Date, Plot, Status, MPAA         string
	Rating                           float64
	Genres, Studios                  []string
	Cast                             []Person
	IMDb, TMDB                       string
	Poster, Backdrop                 string
	ModTime                          time.Time // of the newest episode
	Seasons                          []*CatSeason
}

// Catalog is one scan of the library, as the web interface and the
// Jellyfin API present it.
type Catalog struct {
	Movies  []*CatItem
	Shows   []*CatShow
	items   map[string]*CatItem
	shows   map[string]*CatShow
	seasons map[string]*CatSeason
}

// catalogID makes the identifiers Jellyfin clients expect: they parse them
// as GUIDs, so anything but 32 hex digits is rejected.
func catalogID(kind, key string) string {
	sum := md5.Sum([]byte(kind + "\x00" + key))
	return hex.EncodeToString(sum[:])
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if exists(p) {
			return p
		}
	}
	return ""
}

func nfoID(info *nfoInfo, kind string) string {
	for _, id := range info.UniqueIDs {
		if id.Type == kind {
			return id.Value
		}
	}
	return ""
}

// buildCatalog scans the library folders. Titles, plots and artwork come
// from the .nfo and image files MediaKeeper writes; a video without them is
// listed under the name guessed from its file name.
func buildCatalog(roots []Root) (*Catalog, error) {
	cat := &Catalog{items: map[string]*CatItem{}, shows: map[string]*CatShow{}, seasons: map[string]*CatSeason{}}
	for i, r := range roots {
		if err := cat.add(r, rootKey(roots, i)); err != nil {
			return nil, err
		}
	}
	cat.sort()
	return cat, nil
}

// add puts the videos of one library folder into the catalogue. key tells
// its titles from those of the other folders.
func (cat *Catalog) add(r Root, key string) error {
	root := r.Path
	files, err := scanRoot(r)
	if err != nil {
		return err
	}
	for _, f := range files {
		st, err := os.Stat(f.Path)
		if err != nil {
			continue
		}
		dir := filepath.Dir(f.Path)
		stem := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
		it := &CatItem{Path: f.Path, Rel: f.Rel, Root: root, Size: st.Size(), ModTime: st.ModTime(),
			Title: f.Guess.Title, Year: f.Guess.Year}
		for _, sc := range f.Sidecars {
			if strings.EqualFold(filepath.Ext(sc), ".srt") {
				it.Subs = append(it.Subs, sc)
			}
		}
		info := readNFO(filepath.Join(dir, stem+".nfo"))
		isEpisode := f.Guess.IsSeries
		if info != nil {
			isEpisode = info.XMLName.Local == "episodedetails"
			it.Plot, it.Tagline, it.MPAA, it.Genres = info.Plot, info.Tagline, info.MPAA, info.Genres
			it.Collection = info.collection()
			it.Rating, it.Runtime, it.Date = info.Rating, info.Runtime, info.Premiered+info.Aired
			it.OriginalTitle, it.LocalTitle = info.Original, info.Localized
			it.Countries, it.Studios, it.Directors, it.Writers, it.Cast = info.Countries, info.Studios, info.Directors, info.Writers, info.cast()
			it.IMDb, it.TMDB = nfoID(info, "imdb"), nfoID(info, "tmdb")
			if info.Title != "" {
				it.Title = info.Title
			}
			if info.Year > 0 {
				it.Year = info.Year
			}
		}
		if it.Year == 0 {
			it.Year = yearOf(it.Date)
		}

		if !isEpisode {
			it.Kind, it.ID = kindMovie, catalogID(kindMovie, key+f.Rel)
			if it.Title == "" {
				it.Title = stem
			}
			if dir != root { // a loose file in the root shares the folder: only artwork named after it is its own
				it.Poster = firstExisting(filepath.Join(dir, "poster.jpg"), filepath.Join(dir, stem+"-poster.jpg"))
				it.Backdrop = firstExisting(filepath.Join(dir, "backdrop.jpg"), filepath.Join(dir, "fanart.jpg"), filepath.Join(dir, stem+"-backdrop.jpg"))
			} else {
				it.Poster = firstExisting(filepath.Join(dir, stem+"-poster.jpg"))
				it.Backdrop = firstExisting(filepath.Join(dir, stem+"-backdrop.jpg"))
			}
			cat.Movies = append(cat.Movies, it)
			cat.items[it.ID] = it
			continue
		}

		// The series folder is the one with tvshow.nfo; without it the
		// series is known only by the title guessed from the file name.
		showTitle, showKey, showDir := f.Guess.Title, "guess:"+norm(f.Guess.Title), ""
		var showInfo *nfoInfo
		if info != nil && info.ShowTitle != "" {
			showTitle, showKey = info.ShowTitle, "title:"+norm(info.ShowTitle)
		}
		for d := dir; ; d = filepath.Dir(d) {
			if si := readNFO(filepath.Join(d, "tvshow.nfo")); si != nil && si.XMLName.Local == "tvshow" {
				rel, _ := filepath.Rel(root, d)
				showInfo, showDir, showKey = si, d, "dir:"+rel
				break
			}
			if d == root || d == filepath.Dir(d) {
				break
			}
		}
		showKey = key + showKey // a series is not merged with one of the same name on another disk
		showID := catalogID("show", showKey)
		show := cat.shows[showID]
		if show == nil {
			show = &CatShow{ID: showID, Dir: showDir, Title: showTitle, Year: f.Guess.Year}
			if showInfo != nil {
				show.Plot, show.Status, show.MPAA, show.Genres = showInfo.Plot, showInfo.Status, showInfo.MPAA, showInfo.Genres
				show.Rating, show.Date, show.Year = showInfo.Rating, showInfo.Premiered, showInfo.Year
				show.OriginalTitle, show.LocalTitle = showInfo.Original, showInfo.Localized
				show.Studios, show.Cast = showInfo.Studios, showInfo.cast()
				show.IMDb, show.TMDB = nfoID(showInfo, "imdb"), nfoID(showInfo, "tmdb")
				if showInfo.Title != "" {
					show.Title = showInfo.Title
				}
				show.Poster = firstExisting(filepath.Join(showDir, "poster.jpg"))
				show.Backdrop = firstExisting(filepath.Join(showDir, "backdrop.jpg"), filepath.Join(showDir, "fanart.jpg"))
			}
			if show.Title == "" {
				show.Title = "Unknown series"
			}
			cat.shows[showID] = show
			cat.Shows = append(cat.Shows, show)
		}
		if st.ModTime().After(show.ModTime) {
			show.ModTime = st.ModTime()
		}

		seasonNo := f.Guess.Season
		if len(f.Guess.Episodes) > 0 {
			it.Episode, it.EpisodeEnd = f.Guess.Episodes[0], f.Guess.Episodes[len(f.Guess.Episodes)-1]
		}
		if info != nil && info.XMLName.Local == "episodedetails" && info.Episode > 0 {
			seasonNo, it.Episode = info.Season, info.Episode
			it.EpisodeEnd = max(it.EpisodeEnd, it.Episode)
		}
		seasonID := catalogID("season", fmt.Sprintf("%s/%d", showKey, seasonNo))
		season := cat.seasons[seasonID]
		if season == nil {
			season = &CatSeason{ID: seasonID, Number: seasonNo, Show: show}
			if showDir != "" {
				season.Poster = firstExisting(filepath.Join(showDir, fmt.Sprintf("season%02d-poster.jpg", seasonNo)))
			}
			cat.seasons[seasonID] = season
			show.Seasons = append(show.Seasons, season)
		}
		it.Kind, it.ID, it.Show, it.Season = kindEpisode, catalogID(kindEpisode, key+f.Rel), show, season
		if info == nil || info.Title == "" {
			it.Title = fmt.Sprintf("Episode %d", it.Episode)
		}
		it.Thumb = firstExisting(filepath.Join(dir, stem+"-thumb.jpg"))
		season.Episodes = append(season.Episodes, it)
		cat.items[it.ID] = it
	}
	return nil
}

// sort puts titles in alphabetical order and episodes in theirs.
func (cat *Catalog) sort() {
	byTitle := func(a, b string) bool { return strings.ToLower(a) < strings.ToLower(b) }
	sort.SliceStable(cat.Movies, func(a, b int) bool { return byTitle(cat.Movies[a].Title, cat.Movies[b].Title) })
	sort.SliceStable(cat.Shows, func(a, b int) bool { return byTitle(cat.Shows[a].Title, cat.Shows[b].Title) })
	for _, show := range cat.Shows {
		sort.SliceStable(show.Seasons, func(a, b int) bool { return show.Seasons[a].Number < show.Seasons[b].Number })
		for _, season := range show.Seasons {
			sort.SliceStable(season.Episodes, func(a, b int) bool { return season.Episodes[a].Episode < season.Episodes[b].Episode })
		}
	}
}

// byPath indexes the videos by their files.
func (cat *Catalog) byPath() map[string]*CatItem {
	out := make(map[string]*CatItem, len(cat.items))
	for _, it := range cat.items {
		out[it.Path] = it
	}
	return out
}

// Episodes returns all episodes of the show in order.
func (s *CatShow) Episodes() []*CatItem {
	var out []*CatItem
	for _, season := range s.Seasons {
		out = append(out, season.Episodes...)
	}
	return out
}

// Library keeps the catalogue of a directory current: it is rescanned when
// the last scan is old, so new files show up without a restart.
type Library struct {
	roots  []Root
	prober *prober
	log    func(string, ...any) // told what appears in the library and what is gone

	mu      sync.Mutex
	cat     *Catalog
	scanned time.Time
}

const catalogRescanAfter = 30 * time.Second

func NewLibrary(roots []Root, p *prober) *Library { return &Library{roots: roots, prober: p} }

func (l *Library) Catalog() (*Catalog, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cat != nil && time.Since(l.scanned) < catalogRescanAfter {
		return l.cat, nil
	}
	cat, err := buildCatalog(l.roots)
	if err != nil {
		if l.cat != nil {
			return l.cat, nil // keep serving what is known
		}
		return nil, err
	}
	for _, it := range cat.items {
		l.prober.request(it.Path, it.Size, it.ModTime)
	}
	catalogChanges(l.cat, cat, l.log)
	l.cat, l.scanned = cat, time.Now()
	return cat, nil
}

// setRoots changes the folders of the library; the next request rescans.
func (l *Library) setRoots(roots []Root) {
	l.mu.Lock()
	l.roots, l.scanned = roots, time.Time{}
	l.mu.Unlock()
}

// Invalidate makes the next request rescan: files have just been added.
func (l *Library) Invalidate() {
	l.mu.Lock()
	l.scanned = time.Time{}
	l.mu.Unlock()
}

// Duration of a video: measured by ffprobe, or the runtime from the .nfo.
func (l *Library) Duration(it *CatItem) time.Duration {
	if info, ok := l.prober.get(it.Path, it.Size, it.ModTime); ok && info.Duration > 0 {
		return info.Duration
	}
	return time.Duration(it.Runtime) * time.Minute
}

// ProbeNow is Probe for a file whose tracks must be known right away.
func (l *Library) ProbeNow(it *CatItem) probeInfo {
	return l.prober.now(it.Path, it.Size, it.ModTime)
}

func (l *Library) Probe(it *CatItem) probeInfo {
	info, _ := l.prober.get(it.Path, it.Size, it.ModTime)
	return info
}
