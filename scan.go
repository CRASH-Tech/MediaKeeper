package main

import (
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var videoExts = map[string]bool{
	".mkv": true, ".avi": true, ".mp4": true, ".m4v": true, ".mov": true, ".wmv": true,
	".mpg": true, ".mpeg": true, ".ts": true, ".m2ts": true, ".webm": true, ".flv": true,
}

// Files that belong to a video with the same base name and move with it.
var sidecarExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true, ".vtt": true,
	".ac3": true, ".mka": true, ".dts": true,
}

type MediaFile struct {
	Path     string // absolute
	Rel      string // relative to the scanned root
	Guess    Guess
	Sidecars []string // absolute paths
}

// Unit is what gets identified in one go: a movie or all files of a series.
type Unit struct {
	Kind  string
	Title string
	Year  int
	Files []*MediaFile
}

func Scan(root string) ([]*MediaFile, error) {
	var files []*MediaFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && path != root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !videoExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, &MediaFile{Path: path, Rel: rel, Guess: ParsePath(rel), Sidecars: sidecars(path)})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files, err
}

func sidecars(video string) []string {
	dir := filepath.Dir(video)
	stem := strings.TrimSuffix(filepath.Base(video), filepath.Ext(video))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, stem+".") || !sidecarExts[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// Group turns files into units: every movie is its own unit, episodes are
// collected by the guessed series title.
func Group(files []*MediaFile) []*Unit {
	var units []*Unit
	series := map[string]*Unit{}
	for _, f := range files {
		g := f.Guess
		if !g.IsSeries {
			units = append(units, &Unit{Kind: kindMovie, Title: g.Title, Year: g.Year, Files: []*MediaFile{f}})
			continue
		}
		key := norm(g.Title)
		u := series[key]
		if u == nil {
			u = &Unit{Kind: kindTV, Title: g.Title}
			series[key] = u
			units = append(units, u)
		}
		if u.Year == 0 {
			u.Year = g.Year
		}
		u.Files = append(u.Files, f)
	}
	return units
}

// existingID returns the source and the id recorded in an .nfo next to the
// unit's files by a previous run, so that a library is not identified twice.
func existingID(root string, u *Unit) (source, id, kind string, local LocalTitle) {
	f := u.Files[0]
	dir := filepath.Dir(f.Path)
	var candidates []string
	if u.Kind == kindMovie {
		stem := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
		candidates = append(candidates, filepath.Join(dir, stem+".nfo"))
	} else {
		for d := dir; ; d = filepath.Dir(d) {
			candidates = append(candidates, filepath.Join(d, "tvshow.nfo"))
			if d == root || d == filepath.Dir(d) {
				break
			}
		}
	}
	for _, c := range candidates {
		data, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		if m := reNFOMarker.FindSubmatch(data); m != nil {
			if l := reNFOLocal.FindSubmatch(data); l != nil {
				local = LocalTitle{Title: html.UnescapeString(string(l[1])), Known: true}
			}
			return string(m[1]), string(m[2]), string(m[3]), local
		}
	}
	return "", "", "", local
}
