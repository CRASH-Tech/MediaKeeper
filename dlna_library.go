package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// dlnaNode is an entry of the tree a DLNA client browses: a folder or a
// video. The same video appears in several folders ("Movies", "Folders"),
// each time as a separate node with its own ID.
type dlnaNode struct {
	ID, Parent string
	Title      string
	Children   []*dlnaNode

	// Videos only.
	Path    string
	Size    int64
	ModTime time.Time
	Subs    []string // subtitle files next to the video

	Art     string // image file: poster or episode still
	Date    string
	Plot    string
	Genres  []string
	Runtime int // minutes, from the .nfo; used until ffprobe measures the file
}

func (n *dlnaNode) IsItem() bool { return n.Path != "" }

// dlnaLibrary is one scan of the directory, turned into a tree.
type dlnaLibrary struct {
	nodes     map[string]*dlnaNode
	signature string // changes when files appear, disappear or are replaced
	movies    int
	episodes  int
}

const (
	dlnaRootID    = "0"
	dlnaMoviesID  = "movies"
	dlnaSeriesID  = "series"
	dlnaFoldersID = "folders"
)

// nfoInfo is what the server reads back from the .nfo files MediaKeeper
// writes (and from Kodi/Jellyfin ones, which share the format).
type nfoInfo struct {
	XMLName   xml.Name
	Title     string   `xml:"title"`
	Original  string   `xml:"originaltitle"`
	Localized string   `xml:"localizedtitle"`
	Tagline   string   `xml:"tagline"`
	MPAA      string   `xml:"mpaa"`
	Rating    float64  `xml:"rating"`
	Status    string   `xml:"status"`
	UniqueIDs []nfoUID `xml:"uniqueid"`
	ShowTitle string   `xml:"showtitle"`
	Year      int      `xml:"year"`
	Premiered string   `xml:"premiered"`
	Aired     string   `xml:"aired"`
	Plot      string   `xml:"plot"`
	Runtime   int      `xml:"runtime"`
	Season    int      `xml:"season"`
	Episode   int      `xml:"episode"`
	Genres    []string `xml:"genre"`
	Countries []string `xml:"country"`
	Studios   []string `xml:"studio"`
	Directors []string `xml:"director"`
	Writers   []string `xml:"credits"`
	Actors    []struct {
		Name string `xml:"name"`
		Role string `xml:"role"`
	} `xml:"actor"`
}

func (n *nfoInfo) cast() []Person {
	var out []Person
	for _, a := range n.Actors {
		if a.Name != "" {
			out = append(out, Person{Name: a.Name, Role: a.Role})
		}
	}
	return out
}

// readNFO parses the first document of an .nfo file (an episode file may
// hold several); nil if there is none.
func readNFO(path string) *nfoInfo {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var info nfoInfo
	if xml.NewDecoder(bytes.NewReader(data)).Decode(&info) != nil {
		return nil
	}
	return &info
}

// withLocal puts the Russian title next to the main one:
// "Iron Man / Железный человек".
func withLocal(title, local string) string {
	if local == "" || strings.EqualFold(local, title) {
		return title
	}
	return title + " / " + local
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func nodeID(prefix, rel string) string {
	sum := sha1.Sum([]byte(rel))
	return prefix + hex.EncodeToString(sum[:8])
}

// buildLibrary scans the library folders and arranges the videos into
//
//	Movies/                    every movie, by title
//	Series/Show/Season N/      episodes in order
//	Folders/                   the directory as it is on disk (one folder
//	                           per library folder, when there are several)
//
// Titles, plots and artwork come from the .nfo and image files next to the
// videos; a video without them is shown under the name guessed from its
// file name.
func buildLibrary(roots []Root) (*dlnaLibrary, error) {
	lib := &dlnaLibrary{nodes: map[string]*dlnaNode{}}
	add := func(parent *dlnaNode, n *dlnaNode) *dlnaNode {
		if have := lib.nodes[n.ID]; have != nil {
			return have
		}
		n.Parent = parent.ID
		parent.Children = append(parent.Children, n)
		lib.nodes[n.ID] = n
		return n
	}
	top := &dlnaNode{ID: dlnaRootID, Parent: "-1", Title: "MediaKeeper"}
	lib.nodes[top.ID] = top
	movies := add(top, &dlnaNode{ID: dlnaMoviesID, Title: "Movies"})
	series := add(top, &dlnaNode{ID: dlnaSeriesID, Title: "Shows"})
	folders := add(top, &dlnaNode{ID: dlnaFoldersID, Title: "Folders"})

	type episodeKey struct{ season, episode int }
	order := map[*dlnaNode]episodeKey{}
	seasons := map[*dlnaNode]int{}
	var sig strings.Builder

	names := map[string]int{} // folder names already used under Folders
	for i, r := range roots {
		root, key := r.Path, rootKey(roots, i)
		files, err := scanRoot(r)
		if err != nil {
			return nil, err
		}
		diskFolder := folders
		if len(roots) > 1 { // the files of each library folder under its name
			name := filepath.Base(root)
			if names[name]++; names[name] > 1 {
				name = fmt.Sprintf("%s (%d)", name, names[name])
			}
			diskFolder = add(folders, &dlnaNode{ID: nodeID("r-", root), Title: name})
		}
		for _, f := range files {
			st, err := os.Stat(f.Path)
			if err != nil {
				continue
			}
			fmt.Fprintf(&sig, "%s|%d|%d\n", f.Rel, st.Size(), st.ModTime().UnixNano())
			dir := filepath.Dir(f.Path)
			stem := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
			video := dlnaNode{Path: f.Path, Size: st.Size(), ModTime: st.ModTime()}
			for _, sc := range f.Sidecars {
				if ext := strings.ToLower(filepath.Ext(sc)); ext == ".srt" {
					video.Subs = append(video.Subs, sc)
				}
			}

			info := readNFO(filepath.Join(dir, stem+".nfo"))
			isEpisode := f.Guess.IsSeries
			if info != nil {
				isEpisode = info.XMLName.Local == "episodedetails"
				video.Plot, video.Genres, video.Runtime = info.Plot, info.Genres, info.Runtime
				video.Date = info.Premiered + info.Aired
			}

			// Folders: the file under its real name, in its real place.
			parent := diskFolder
			if rel := filepath.Dir(f.Rel); rel != "." {
				path := ""
				for _, part := range strings.Split(rel, string(filepath.Separator)) {
					path = filepath.Join(path, part)
					parent = add(parent, &dlnaNode{ID: nodeID("d-", key+path), Title: part})
				}
			}
			plain := video
			plain.ID, plain.Title = nodeID("f-", key+f.Rel), filepath.Base(f.Path)
			if thumb := filepath.Join(dir, stem+"-thumb.jpg"); exists(thumb) {
				plain.Art = thumb
			} else if poster := filepath.Join(dir, "poster.jpg"); dir != root && exists(poster) {
				plain.Art = poster
			}
			add(parent, &plain)

			if !isEpisode {
				lib.movies++
				movie := video
				movie.ID, movie.Title, movie.Art = nodeID("m-", key+f.Rel), withYear(f.Guess.Title, f.Guess.Year), plain.Art
				if info != nil && info.Title != "" {
					movie.Title = withLocal(info.Title, info.Localized)
					if info.Year > 0 {
						movie.Title += fmt.Sprintf(" (%d)", info.Year)
					}
				}
				if movie.Title == "" {
					movie.Title = stem
				}
				add(movies, &movie)
				continue
			}

			lib.episodes++
			// The series folder is the one with tvshow.nfo; without it the
			// series is known only by the title guessed from the file name.
			showTitle, showKey, showDir := f.Guess.Title, "guess:"+norm(f.Guess.Title), ""
			if info != nil && info.ShowTitle != "" {
				showTitle, showKey = info.ShowTitle, "title:"+norm(info.ShowTitle)
			}
			for d := dir; ; d = filepath.Dir(d) {
				if show := readNFO(filepath.Join(d, "tvshow.nfo")); show != nil && show.XMLName.Local == "tvshow" {
					showDir, showKey = d, "dir:"+d
					if show.Title != "" {
						showTitle = withLocal(show.Title, show.Localized)
					}
					break
				}
				if d == root || d == filepath.Dir(d) {
					break
				}
			}
			if showTitle == "" {
				showTitle = "Unknown series"
			}
			showKey = key + showKey
			show := add(series, &dlnaNode{ID: nodeID("s-", showKey), Title: showTitle})
			if showDir != "" && show.Art == "" && exists(filepath.Join(showDir, "poster.jpg")) {
				show.Art = filepath.Join(showDir, "poster.jpg")
			}

			seasonNo, episodeNo, last := f.Guess.Season, 0, 0
			if len(f.Guess.Episodes) > 0 {
				episodeNo, last = f.Guess.Episodes[0], f.Guess.Episodes[len(f.Guess.Episodes)-1]
			}
			if info != nil && info.XMLName.Local == "episodedetails" && info.Episode > 0 {
				seasonNo, episodeNo = info.Season, info.Episode
				last = max(last, episodeNo)
			}
			seasonTitle := fmt.Sprintf("Season %d", seasonNo)
			if seasonNo == 0 {
				seasonTitle = "Specials"
			}
			season := add(show, &dlnaNode{ID: nodeID("n-", fmt.Sprintf("%s/%d", showKey, seasonNo)), Title: seasonTitle, Art: show.Art})
			seasons[season] = seasonNo
			if poster := filepath.Join(showDir, fmt.Sprintf("season%02d-poster.jpg", seasonNo)); showDir != "" && exists(poster) {
				season.Art = poster
			}

			ep := video
			ep.ID = nodeID("e-", key+f.Rel)
			ep.Title = fmt.Sprintf("%02d", episodeNo)
			if last > episodeNo {
				ep.Title += fmt.Sprintf("-%02d", last)
			}
			if info != nil && info.Title != "" {
				ep.Title += ". " + info.Title
			} else {
				ep.Title = "Episode " + ep.Title
			}
			if ep.Art = plain.Art; ep.Art == "" {
				ep.Art = season.Art
			}
			order[add(season, &ep)] = episodeKey{seasonNo, episodeNo}
		}
	}

	sort.SliceStable(movies.Children, func(a, b int) bool {
		return strings.ToLower(movies.Children[a].Title) < strings.ToLower(movies.Children[b].Title)
	})
	sort.SliceStable(series.Children, func(a, b int) bool {
		return strings.ToLower(series.Children[a].Title) < strings.ToLower(series.Children[b].Title)
	})
	for _, show := range series.Children {
		sort.SliceStable(show.Children, func(a, b int) bool { return seasons[show.Children[a]] < seasons[show.Children[b]] })
		for _, season := range show.Children {
			sort.SliceStable(season.Children, func(a, b int) bool {
				return order[season.Children[a]].episode < order[season.Children[b]].episode
			})
		}
	}
	lib.signature = sig.String()
	return lib, nil
}

// items returns every video below the node, in display order.
func (n *dlnaNode) items() []*dlnaNode {
	if n.IsItem() {
		return []*dlnaNode{n}
	}
	var out []*dlnaNode
	for _, c := range n.Children {
		out = append(out, c.items()...)
	}
	return out
}
