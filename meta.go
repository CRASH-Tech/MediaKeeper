package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // uploaded artwork may be PNG
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Editing a description by hand from the web interface. The change goes
// into the .nfo — only the edited elements are replaced, everything else in
// the file stays as it was — and into the video files' own tags. Movies are
// here; series and their episodes are in meta_show.go.

// xmlNode is any XML element, kept with its attributes and children.
type xmlNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Text    string     `xml:",chardata"`
	Nodes   []xmlNode  `xml:",any"`
}

func (n *xmlNode) child(name string) *xmlNode {
	for i := range n.Nodes {
		if n.Nodes[i].XMLName.Local == name {
			return &n.Nodes[i]
		}
	}
	return nil
}

func (n *xmlNode) text(name string) string {
	if c := n.child(name); c != nil {
		return strings.TrimSpace(c.Text)
	}
	return ""
}

func (n *xmlNode) texts(name string) []string {
	var out []string
	for _, c := range n.Nodes {
		if c.XMLName.Local == name && strings.TrimSpace(c.Text) != "" {
			out = append(out, strings.TrimSpace(c.Text))
		}
	}
	return out
}

// set replaces the first element of that name (or adds one); an empty value
// removes it.
func (n *xmlNode) set(name, value string, attrs ...xml.Attr) {
	value = strings.TrimSpace(value)
	for i := range n.Nodes {
		if n.Nodes[i].XMLName.Local == name {
			if value == "" {
				n.Nodes = append(n.Nodes[:i], n.Nodes[i+1:]...)
			} else {
				n.Nodes[i] = xmlNode{XMLName: xml.Name{Local: name}, Attrs: attrs, Text: value}
			}
			return
		}
	}
	if value != "" {
		n.Nodes = append(n.Nodes, xmlNode{XMLName: xml.Name{Local: name}, Attrs: attrs, Text: value})
	}
}

// setAll replaces all elements of that name with the given values, at the
// place of the first of them.
func (n *xmlNode) setAll(name string, values []string, build func(string) xmlNode) {
	at := -1
	kept := n.Nodes[:0:0]
	for _, c := range n.Nodes {
		if c.XMLName.Local == name {
			if at < 0 {
				at = len(kept)
			}
			continue
		}
		kept = append(kept, c)
	}
	if at < 0 {
		at = len(kept)
	}
	var fresh []xmlNode
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			fresh = append(fresh, build(v))
		}
	}
	n.Nodes = append(kept[:at:at], append(fresh, kept[at:]...)...)
}

// tidy drops the whitespace between child elements, so that re-indenting
// does not pile it up.
func (n *xmlNode) tidy() {
	if len(n.Nodes) > 0 && strings.TrimSpace(n.Text) == "" {
		n.Text = ""
	}
	for i := range n.Nodes {
		n.Nodes[i].tidy()
	}
}

// nfoFile is an .nfo split into what comes before the description (the XML
// header, MediaKeeper's comment with the source), the description, and
// whatever follows it.
type nfoFile struct {
	path          string
	before, after []byte
	root          xmlNode
	created       bool // there was no .nfo yet
}

func readNFOFile(path, rootName string) (*nfoFile, error) {
	f := &nfoFile{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		f.created, f.before = true, []byte(nfoHeader)
		f.root = xmlNode{XMLName: xml.Name{Local: rootName}}
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		offset := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%s is not a media description", filepath.Base(path))
		}
		if start, ok := tok.(xml.StartElement); ok {
			if start.Name.Local != rootName {
				return nil, fmt.Errorf("%s describes a %s, not a %s", filepath.Base(path), start.Name.Local, rootName)
			}
			f.before = data[:offset]
			if err := dec.DecodeElement(&f.root, &start); err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
			}
			f.after = data[dec.InputOffset():]
			return f, nil
		}
	}
}

func (f *nfoFile) write() error {
	f.root.tidy()
	body, err := xml.MarshalIndent(f.root, "", "  ")
	if err != nil {
		return err
	}
	var b bytes.Buffer
	b.Write(bytes.TrimRight(f.before, " \t\r\n"))
	b.WriteByte('\n')
	b.Write(body)
	b.WriteByte('\n')
	if after := bytes.TrimSpace(f.after); len(after) > 0 {
		b.Write(after)
		b.WriteByte('\n')
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// movieMeta is the part of a movie's or a series' description that can be
// edited. A series has no tagline, directors, writers or countries of its
// own, and a movie no status.
type movieMeta struct {
	Title         string   `json:"title"`
	OriginalTitle string   `json:"originalTitle"`
	LocalTitle    string   `json:"localTitle"`
	Year          int      `json:"year"`
	Released      string   `json:"released"` // YYYY-MM-DD
	Tagline       string   `json:"tagline"`
	Plot          string   `json:"plot"`
	MPAA          string   `json:"mpaa"`
	Rating        float64  `json:"rating"`
	Genres        []string `json:"genres"`
	Directors     []string `json:"directors"`
	Writers       []string `json:"writers"`
	Studios       []string `json:"studios"`
	Countries     []string `json:"countries"`
	Cast          []Person `json:"cast"`
	Status        string   `json:"status,omitempty"` // a series: Continuing, Ended
}

// nfoPathFor is the .nfo a movie is described in: named after the video,
// or movie.nfo in its own folder, or (when there is none) a new one named
// after the video.
func nfoPathFor(it *CatItem, root string) string {
	dir := filepath.Dir(it.Path)
	stem := strings.TrimSuffix(filepath.Base(it.Path), filepath.Ext(it.Path))
	own := filepath.Join(dir, stem+".nfo")
	if !exists(own) && dir != root && exists(filepath.Join(dir, "movie.nfo")) {
		return filepath.Join(dir, "movie.nfo")
	}
	return own
}

func metaFromNFO(n *xmlNode) movieMeta {
	m := movieMeta{
		Title: n.text("title"), OriginalTitle: n.text("originaltitle"), LocalTitle: n.text("localizedtitle"),
		Released: n.text("premiered"), Tagline: n.text("tagline"), Plot: n.text("plot"), MPAA: n.text("mpaa"),
		Genres: n.texts("genre"), Directors: n.texts("director"), Writers: n.texts("credits"),
		Studios: n.texts("studio"), Countries: n.texts("country"), Status: n.text("status"),
	}
	m.Year, _ = strconv.Atoi(n.text("year"))
	m.Rating, _ = strconv.ParseFloat(n.text("rating"), 64)
	for _, c := range n.Nodes {
		if c.XMLName.Local == "actor" && c.text("name") != "" {
			m.Cast = append(m.Cast, Person{Name: c.text("name"), Role: c.text("role"), Thumb: c.text("thumb")})
		}
	}
	return m
}

func (m *movieMeta) check() error {
	switch {
	case strings.TrimSpace(m.Title) == "":
		return errors.New("the title cannot be empty")
	case m.Year != 0 && (m.Year < 1870 || m.Year > 2100):
		return errors.New("the year is not right")
	case m.Released != "" && !reDate.MatchString(m.Released):
		return errors.New("the release date must look like 2008-04-30")
	case m.Rating < 0 || m.Rating > 10:
		return errors.New("the rating goes from 0 to 10")
	}
	return nil
}

var reDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func (m *movieMeta) apply(n *xmlNode) {
	show := n.XMLName.Local == "tvshow"
	n.set("title", m.Title)
	n.set("originaltitle", m.OriginalTitle)
	n.set("localizedtitle", m.LocalTitle, xml.Attr{Name: xml.Name{Local: "lang"}, Value: "ru"})
	n.set("year", map[bool]string{true: strconv.Itoa(m.Year), false: ""}[m.Year > 0])
	n.set("premiered", m.Released)
	if show {
		n.set("status", m.Status)
	} else {
		n.set("tagline", m.Tagline)
	}
	n.set("plot", m.Plot)
	n.set("mpaa", m.MPAA)
	n.set("rating", map[bool]string{true: strconv.FormatFloat(m.Rating, 'f', -1, 64), false: ""}[m.Rating > 0])
	plain := func(name string) func(string) xmlNode {
		return func(v string) xmlNode { return xmlNode{XMLName: xml.Name{Local: name}, Text: v} }
	}
	n.setAll("genre", m.Genres, plain("genre"))
	n.setAll("studio", m.Studios, plain("studio"))
	if !show {
		n.setAll("director", m.Directors, plain("director"))
		n.setAll("credits", m.Writers, plain("credits"))
		n.setAll("country", m.Countries, plain("country"))
	}
	// Actors keep their photos when only their roles or order change.
	photos := map[string]string{}
	for _, c := range n.Nodes {
		if c.XMLName.Local == "actor" {
			photos[c.text("name")] = c.text("thumb")
		}
	}
	names := make([]string, 0, len(m.Cast))
	roles := map[string]string{}
	for _, p := range m.Cast {
		names = append(names, p.Name)
		roles[strings.TrimSpace(p.Name)] = strings.TrimSpace(p.Role)
	}
	order := 0
	for _, p := range m.Cast {
		if p.Thumb != "" { // filled in from a catalogue
			photos[strings.TrimSpace(p.Name)] = p.Thumb
		}
	}
	n.setAll("actor", names, func(name string) xmlNode {
		a := xmlNode{XMLName: xml.Name{Local: "actor"}}
		a.set("name", name)
		a.set("role", roles[name])
		a.set("order", strconv.Itoa(order))
		a.set("thumb", photos[name])
		order++
		return a
	})
}

// metaSave is what the edit form sends: the fields, and when they were
// filled in from a catalogue, where from and its artwork.
type metaSave struct {
	movieMeta
	IDs         map[string]string `json:"ids"`
	Source      string            `json:"source"`
	SourceID    string            `json:"sourceId"`
	SourceKind  string            `json:"sourceKind"`
	PosterURL   string            `json:"posterUrl"`
	BackdropURL string            `json:"backdropUrl"`
	Rename      bool              `json:"rename"` // name the files after the title and year
}

// setSource records the catalogue the description now comes from: the
// comment MediaKeeper reads back, and the catalogue numbers media centers
// use.
func (f *nfoFile) setSource(source, id, kind string, ids map[string]string) {
	imdbElement, defaultKind := "imdbid", kindMovie
	if f.root.XMLName.Local == "tvshow" { // that is how media centers name them
		imdbElement, defaultKind = "imdb_id", kindTV
	}
	marker := []byte(fmt.Sprintf("<!-- mediakeeper source=%q id=%q kind=%q -->", source, id, firstNonEmpty(kind, defaultKind)))
	if reNFOMarker.Match(f.before) {
		f.before = reNFOMarker.ReplaceAll(f.before, marker)
	} else {
		f.before = append(bytes.TrimRight(f.before, " \t\r\n"), append([]byte("\n"), marker...)...)
	}
	if len(ids) == 0 {
		return
	}
	var fresh []string
	for _, uid := range uniqueIDs(ids) {
		fresh = append(fresh, uid.Type)
	}
	byType := map[string]nfoUID{}
	for _, uid := range uniqueIDs(ids) {
		byType[uid.Type] = uid
	}
	f.root.setAll("uniqueid", fresh, func(typ string) xmlNode {
		uid := byType[typ]
		attrs := []xml.Attr{{Name: xml.Name{Local: "type"}, Value: typ}}
		if uid.Default {
			attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "default"}, Value: "true"})
		}
		return xmlNode{XMLName: xml.Name{Local: "uniqueid"}, Attrs: attrs, Text: uid.Value}
	})
	f.root.set("tmdbid", ids["tmdb"])
	f.root.set(imdbElement, ids["imdb"])
}

// lookupMeta loads a catalogue entry and turns it into the fields of the
// edit form; nothing is saved yet. A series picked for a movie file is
// taken as a movie: the file is the whole series.
func (s *Server) lookupMeta(id string, req resolveRequest) (map[string]any, error) {
	var out bytes.Buffer
	a, u, _, err := s.fixUnit(id, &out)
	if err != nil {
		return nil, err
	}
	series := u.Kind == kindTV
	req.AsMovie = !series
	m, err := matchFor(a, u, req)
	if err != nil {
		return nil, err
	}
	if series && m.Show == nil {
		return nil, fmt.Errorf("%q is a movie, not a series", m.Movie.Title)
	}
	a.localize(m)
	mv := m.Movie
	if mv == nil {
		mv = m.Show.AsMovie()
	}
	meta := movieMeta{
		Title: mv.Title, OriginalTitle: mv.OriginalTitle, LocalTitle: mv.Local.Title, Year: mv.Year,
		Released: mv.Released, Tagline: mv.Tagline, Plot: mv.Overview, MPAA: mv.MPAA, Rating: round1(mv.Rating),
		Genres: mv.Genres, Directors: mv.Directors, Writers: mv.Writers, Studios: mv.Studios, Countries: mv.Countries, Cast: mv.Cast,
	}
	if series {
		meta.Status = m.Show.Status
	}
	if meta.OriginalTitle == meta.Title {
		meta.OriginalTitle = ""
	}
	if len(meta.Cast) > 20 {
		meta.Cast = meta.Cast[:20]
	}
	return map[string]any{
		"meta": meta, "ids": mv.IDs, "source": mv.Source, "sourceName": a.sourceName(mv.Source),
		"sourceId": mv.ID, "sourceKind": mv.SourceKind(), "posterUrl": mv.Poster, "backdropUrl": mv.Backdrop,
	}, nil
}

// renameMovie names a movie's files after its title and year, the way a new
// title is filed: its own folder is renamed with everything in it, the file
// and what is named after it follow. The change is journaled for -undo.
func (s *Server) renameMovie(it *CatItem, title string, year int) (string, error) {
	if fileTags.writing(it.Path) {
		return it.Path, errors.New("the tags of the file are still being written: rename it in a minute")
	}
	s.organizing.Lock()
	defer s.organizing.Unlock()
	var out bytes.Buffer
	a, u, _, err := s.fixUnit(it.ID, &out)
	if err != nil {
		return it.Path, err
	}
	a.noTags = true // they were just written
	name := withYear(title, year)
	base, own := a.library(u.Files)
	dir := filepath.Join(a.category(base, moviesFolder), name)
	plan := &Plan{}
	f := plan.relocate(own, dir, u.Files)[0]
	oldDir := filepath.Dir(f.Path)
	stem := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
	item := &Item{From: u.Files[0].Path, Move: Move{f.Path, filepath.Join(dir, name+strings.ToLower(filepath.Ext(f.Path)))},
		Sidecars: sidecarMoves(f, dir, name)}
	if stem != name { // its description and artwork named after it go along
		for _, suffix := range []string{".nfo", "-poster.jpg", "-backdrop.jpg", "-thumb.jpg"} {
			if exists(filepath.Join(oldDir, stem+suffix)) {
				item.Sidecars = append(item.Sidecars, Move{filepath.Join(oldDir, stem+suffix), filepath.Join(dir, name+suffix)})
			}
		}
	}
	if item.From == item.Move.Dst && len(plan.Items) == 0 {
		return it.Path, nil // already so named
	}
	if item.Move.Src != item.Move.Dst && exists(item.Move.Dst) {
		return it.Path, fmt.Errorf("%s is already in the library", a.rel(a.root, item.Move.Dst))
	}
	plan.add(item)
	a.Apply(plan)
	s.refresh()
	if s.dl != nil && exists(item.Move.Dst) {
		s.dl.moved(item.From, item.Move.Dst)
	}
	if err := firstFailure(out.String()); err != nil {
		return it.Path, err
	}
	return item.Move.Dst, nil
}

// tagsFor are the tags written into a movie's file after an edit.
func tagsFor(m movieMeta, n *xmlNode, cover string) *TagInfo {
	t := &TagInfo{Title: m.Title, Date: m.Released, Description: m.Plot, Genres: m.Genres, Cover: cover}
	if t.Date == "" && m.Year > 0 {
		t.Date = strconv.Itoa(m.Year)
	}
	for _, c := range n.Nodes {
		if c.XMLName.Local == "uniqueid" {
			for _, a := range c.Attrs {
				if a.Name.Local == "type" && a.Value == "imdb" {
					t.IMDb = strings.TrimSpace(c.Text)
				} else if a.Name.Local == "type" && a.Value == "tmdb" {
					t.TMDB = strings.TrimSpace(c.Text)
				}
			}
		}
	}
	return t
}

// artPath is where a movie's poster or backdrop lives: the shared names in
// its own folder, or names after the file for a loose file in the root.
func artPath(it *CatItem, root, kind string) string {
	dir := filepath.Dir(it.Path)
	if dir == root {
		stem := strings.TrimSuffix(filepath.Base(it.Path), filepath.Ext(it.Path))
		return filepath.Join(dir, stem+"-"+kind+".jpg")
	}
	return filepath.Join(dir, kind+".jpg")
}

// tagWriter writes tags into files in the background, never twice at once
// into the same file: remuxing a large AVI or MP4 takes a while.
type tagWriter struct {
	mu   sync.Mutex
	busy map[string]bool
}

var fileTags = &tagWriter{busy: map[string]bool{}}

// tagJob is the tags of one file.
type tagJob struct {
	path string
	tags *TagInfo
}

func (w *tagWriter) writing(path string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.busy[path]
}

// write writes the files one after another, so the episodes of a series
// are not all remuxed at once. Files already being written are skipped; it
// tells whether anything was started.
func (w *tagWriter) write(s *Server, jobs ...tagJob) bool {
	w.mu.Lock()
	var mine []tagJob
	for _, j := range jobs {
		if !w.busy[j.path] {
			w.busy[j.path] = true
			mine = append(mine, j)
		}
	}
	w.mu.Unlock()
	if len(mine) == 0 {
		return false
	}
	go func() {
		for _, j := range mine {
			if err := WriteTags(j.path, j.tags); err != nil && !errors.Is(err, errNoTagTool) {
				s.log("tags of %s: %v", filepath.Base(j.path), err)
			} else if err == nil {
				s.log("tags written into %s", filepath.Base(j.path))
			}
			w.mu.Lock()
			delete(w.busy, j.path)
			w.mu.Unlock()
		}
		s.refresh()
	}()
	return true
}

// metaAPI serves /api/meta/{id} for administrators:
//
//	GET              the description as it is
//	POST lookup      the fields filled in from a catalogue entry (resolveRequest)
//	POST             a new description (JSON metaSave)
//	POST poster      a new poster  (an image file in the "image" form field)
//	POST backdrop    a new backdrop
func (s *Server) metaAPI(w http.ResponseWriter, r *http.Request, id, part string) {
	cat, err := s.lib.Catalog()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	it := cat.items[id]
	switch {
	case cat.shows[id] != nil:
		s.showMetaAPI(w, r, cat.shows[id], part)
		return
	case it != nil && it.Kind == kindEpisode:
		s.episodeMetaAPI(w, r, it, part)
		return
	case it == nil:
		apiError(w, http.StatusNotFound, errNotFound)
		return
	}
	nfo, err := readNFOFile(nfoPathFor(it, it.Root), "movie")
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	switch {
	case part == "" && r.Method == http.MethodGet:
		m := metaFromNFO(&nfo.root)
		if nfo.created { // nothing written yet: what the library shows
			m.Title, m.Year = it.Title, it.Year
		}
		writeJSON(w, http.StatusOK, m)
	case part == "lookup" && r.Method == http.MethodPost:
		var req resolveRequest
		if err := readJSON(r, &req); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		filled, err := s.lookupMeta(it.ID, req)
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, filled)
	case part == "" && r.Method == http.MethodPost:
		var m metaSave
		if err := readJSON(r, &m); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		if err := m.check(); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		s.organizing.Lock()
		m.apply(&nfo.root)
		if m.Source != "" {
			nfo.setSource(m.Source, m.SourceID, m.SourceKind, m.IDs)
		}
		err := nfo.write()
		s.organizing.Unlock()
		if err != nil {
			apiError(w, http.StatusInternalServerError, err)
			return
		}
		// Artwork of the catalogue the fields were filled from.
		var problems []string
		for kind, url := range map[string]string{"poster": m.PosterURL, "backdrop": m.BackdropURL} {
			if url == "" {
				continue
			}
			if err := DownloadFile(url, artPath(it, it.Root, kind)); err != nil {
				problems = append(problems, fmt.Sprintf("the %s could not be downloaded: %v", kind, err))
			}
		}
		s.refresh()
		s.log("description of %q edited by hand", m.Title)
		// Renamed first, so that the tags are written into the file where it
		// ends up.
		if m.Rename {
			if path, err := s.renameMovie(it, m.Title, m.Year); err != nil {
				problems = append(problems, err.Error())
			} else {
				moved := *it
				moved.Path = path
				it = &moved
			}
		}
		tags := s.retag(it, &nfo.root, "")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tags": tags, "problems": problems})
	case (part == "poster" || part == "backdrop") && r.Method == http.MethodPost:
		dst := artPath(it, it.Root, part)
		if err := saveUploadedImage(r, dst); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		s.refresh()
		cover := ""
		if part == "poster" {
			cover = dst
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tags": cover != "" && s.retag(it, &nfo.root, cover)})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// retag writes the description into the file's tags in the background and
// tells whether it started (not with -no-tags, not twice at once).
func (s *Server) retag(it *CatItem, n *xmlNode, cover string) bool {
	if s.noTags {
		return false
	}
	if cover == "" {
		cover = artPath(it, it.Root, "poster")
		if !exists(cover) {
			cover = ""
		}
	}
	return fileTags.write(s, tagJob{it.Path, tagsFor(metaFromNFO(n), n, cover)})
}

// saveUploadedImage stores an uploaded JPEG as it is, and a PNG converted to
// JPEG, which is what media centers look for.
func saveUploadedImage(r *http.Request, dst string) error {
	file, _, err := r.FormFile("image")
	if err != nil {
		return errors.New("no image in the request")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 20<<20))
	if err != nil {
		return err
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return errors.New("this is not a JPEG or PNG image")
	}
	if b := img.Bounds(); b.Dx() < 100 || b.Dy() < 100 {
		return errors.New("the image is too small")
	}
	if format != "jpeg" {
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 90}); err != nil {
			return err
		}
		data = out.Bytes()
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
