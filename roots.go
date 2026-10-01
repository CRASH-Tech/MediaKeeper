package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// A library can be spread over several folders — disks, shares — each
// holding movies, series, or both. Every title stays in the folder it is
// in; the catalogue, the Jellyfin API and DLNA show them all together.

// The kinds of library folders.
const (
	rootMixed  = ""       // both: sorted into Movies and Shows inside
	rootMovies = "movies" // movies only, each in its own folder right inside
	rootShows  = "shows"  // series only, likewise
)

// Root is one folder of the library.
type Root struct {
	Path string `yaml:"path" json:"path"`
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`
	// Key tells the identifiers of its titles from those of other folders.
	// It is kept with the folder in the settings, so removing or reordering
	// folders changes no identifiers (and loses no watch progress). Folders
	// without one (the command line) get one by their place: see rootKey.
	Key    string `yaml:"-" json:"key"`
	HasKey bool   `yaml:"-" json:"hasKey"`
}

// parseRootKind accepts the usual words for the kinds.
func parseRootKind(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "mixed", "both", "all":
		return rootMixed, nil
	case "movies", "movie", "films", "film":
		return rootMovies, nil
	case "shows", "show", "series", "tv":
		return rootShows, nil
	}
	return "", fmt.Errorf("unknown kind of library folder %q (movies, shows, or nothing for both)", s)
}

// UnmarshalYAML takes a folder written as a plain path (holding both
// movies and series) or as {path: ..., kind: ...}.
func (r *Root) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		r.Path, r.Kind = node.Value, rootMixed
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a library folder is a path, or path: and kind:", node.Line)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "path":
			r.Path = value.Value
		case "kind":
			kind, err := parseRootKind(value.Value)
			if err != nil {
				return fmt.Errorf("line %d: %w", value.Line, err)
			}
			r.Kind = kind
		default: // as strict as the rest of the file
			return fmt.Errorf("line %d: unknown setting %q of a library folder (path, kind)", key.Line, key.Value)
		}
	}
	if r.Path == "" {
		return fmt.Errorf("line %d: a library folder without a path", node.Line)
	}
	return nil
}

func (r Root) String() string {
	if r.Kind == rootMixed {
		return r.Path
	}
	return r.Path + " (" + r.Kind + ")"
}

// checkRoots makes the paths absolute and checks that they are folders and
// that none lies inside another: its files would be listed twice.
func checkRoots(roots []Root) ([]Root, error) {
	out := make([]Root, 0, len(roots))
	for _, r := range roots {
		path, err := filepath.Abs(expandHome(r.Path))
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(path); err != nil {
			return nil, err
		} else if !st.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", path)
		}
		for _, o := range out {
			if within(o.Path, path) || within(path, o.Path) {
				return nil, fmt.Errorf("the library folders %s and %s overlap", o.Path, path)
			}
		}
		out = append(out, Root{Path: path, Kind: r.Kind, Key: r.Key, HasKey: r.HasKey})
	}
	if len(out) == 0 {
		return nil, errors.New("no library folders")
	}
	return out, nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// rootOf finds the library folder a path lies in.
func rootOf(roots []Root, path string) (Root, bool) {
	for _, r := range roots {
		if within(r.Path, path) {
			return r, true
		}
	}
	return Root{}, false
}

// rootFor picks where a new title of a category (moviesFolder or
// showsFolder) goes: the first folder of that kind, else the first holding
// both, else the first one.
func rootFor(roots []Root, category string) Root {
	want := rootMovies
	if category == showsFolder {
		want = rootShows
	}
	for _, kind := range []string{want, rootMixed} {
		for _, r := range roots {
			if r.Kind == kind {
				return r
			}
		}
	}
	return roots[0]
}

// rootKey makes the identifiers of titles in different folders distinct:
// the folder's own key, or one by its place — none for the first, so that
// its titles keep the identifiers (and the watch progress) they had while
// it was the only one, its path for the others.
func rootKey(roots []Root, i int) string {
	switch {
	case roots[i].HasKey:
		return roots[i].Key
	case i == 0:
		return ""
	}
	return roots[i].Path + "\x00"
}

// keyed gives every folder its key for good, the one it has by its place:
// what is saved in the settings then stays, whatever happens to the others.
func keyed(roots []Root) []Root {
	out := make([]Root, len(roots))
	for i, r := range roots {
		r.Key, r.HasKey = rootKey(roots, i), true
		out[i] = r
	}
	return out
}

// newRootKey is the key of a folder added to the library: none when it is
// the first of a new library, else its path.
func newRootKey(existing []Root, path string) string {
	if len(existing) == 0 {
		return ""
	}
	return path + "\x00"
}

// scanRoot scans one folder of the library. In a folder of movies nothing
// is taken for an episode, whatever its name looks like.
func scanRoot(r Root) ([]*MediaFile, error) {
	files, err := Scan(r.Path)
	if r.Kind == rootMovies {
		for _, f := range files {
			f.Guess.IsSeries = false
		}
	}
	return files, err
}

// rootList is a repeatable command-line flag: -movies /a -movies /b.
type rootList struct {
	kind  string
	roots *[]Root
}

func (l rootList) String() string { return "" }

func (l rootList) Set(path string) error {
	*l.roots = append(*l.roots, Root{Path: path, Kind: l.kind})
	return nil
}
