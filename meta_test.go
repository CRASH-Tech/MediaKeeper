package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The description of a movie is edited by hand: the .nfo gets the new
// values and keeps everything else, the file gets new tags and the poster.
func TestEditDescription(t *testing.T) {
	path := os.Getenv("PATH")
	s, srv := serverFixture(t)
	boss, kid := newBrowser(t, srv, "boss"), newBrowser(t, srv, "kid")
	var lib libraryView
	boss.json("/api/library", &lib)
	iron, loose := lib.Movies[0].ID, lib.Movies[1].ID

	nfoPath := filepath.Join(s.firstRoot(), "Iron Man (2008)", "Iron Man (2008).nfo")
	os.WriteFile(nfoPath, []byte(nfoHeader+`<!-- mediakeeper source="tmdb" id="1726" kind="movie" -->
<movie>
  <title>Iron Man</title>
  <year>2008</year>
  <uniqueid type="imdb" default="true">tt0371746</uniqueid>
  <genre>Action</genre>
  <genre>Sci-Fi</genre>
  <actor><name>Robert Downey Jr.</name><role>Tony</role><order>0</order><thumb>http://img/rdj.jpg</thumb></actor>
  <fileinfo><streamdetails><video><codec>h264</codec></video></streamdetails></fileinfo>
</movie>
`), 0o644)
	s.refresh()

	if status, _ := kid.get("/api/meta/" + iron); status != http.StatusForbidden {
		t.Errorf("a viewer reads the editor: %d", status)
	}
	var m movieMeta
	boss.json("/api/meta/"+iron, &m)
	if m.Title != "Iron Man" || m.Year != 2008 || strings.Join(m.Genres, ",") != "Action,Sci-Fi" || len(m.Cast) != 1 || m.Cast[0].Role != "Tony" {
		t.Fatalf("description as it is: %+v", m)
	}

	m.Title, m.LocalTitle, m.Plot, m.Released, m.Rating = "Iron Man <Mk I>", "Железный человек", "Tony & the suit.", "2008-05-02", 7.9
	m.Genres = []string{"Action", "Adventure"}
	m.Cast = []Person{{Name: "Jeff Bridges", Role: "Obadiah"}, {Name: "Robert Downey Jr.", Role: "Tony Stark"}}
	if status, body := boss.post("/api/meta/"+iron, m); status != 200 {
		t.Fatalf("save: %d %s", status, body)
	}
	nfo := mustRead(t, nfoPath)
	wantAll(t, "nfo", nfo,
		`<!-- mediakeeper source="tmdb" id="1726" kind="movie" -->`, // the source stays known
		"<title>Iron Man &lt;Mk I&gt;</title>", `<localizedtitle lang="ru">Железный человек</localizedtitle>`,
		"<plot>Tony &amp; the suit.</plot>", "<premiered>2008-05-02</premiered>", "<rating>7.9</rating>",
		"<genre>Adventure</genre>", `<uniqueid type="imdb" default="true">tt0371746</uniqueid>`,
		"<codec>h264</codec>",               // elements MediaKeeper does not edit are kept
		"<thumb>http://img/rdj.jpg</thumb>") // and the photo of an actor still in the cast
	if strings.Contains(nfo, "Sci-Fi") || strings.Index(nfo, "Jeff Bridges") > strings.Index(nfo, "Robert Downey") {
		t.Errorf("lists were not replaced in order:\n%s", nfo)
	}
	boss.json("/api/library", &lib)
	if lib.Movies[0].Title != "Iron Man <Mk I>" {
		t.Errorf("the library does not show the edit: %+v", lib.Movies[0])
	}

	for _, bad := range []movieMeta{{Title: ""}, {Title: "x", Year: 3000}, {Title: "x", Released: "May 2"}, {Title: "x", Rating: 11}} {
		if status, _ := boss.post("/api/meta/"+iron, bad); status != 400 {
			t.Errorf("%+v accepted", bad)
		}
	}

	// A loose file without an .nfo gets one, and artwork named after it.
	m = movieMeta{Title: "Some Unknown Movie", Year: 2019, Plot: "Hand-written."}
	if status, body := boss.post("/api/meta/"+loose, m); status != 200 {
		t.Fatalf("save a new description: %d %s", status, body)
	}
	if nfo := mustRead(t, filepath.Join(s.firstRoot(), "Some.Unknown.Movie.2019.WEB-DL.nfo")); !strings.Contains(nfo, "<plot>Hand-written.</plot>") {
		t.Errorf("new nfo:\n%s", nfo)
	}
	if status, body := upload(t, boss, "/api/meta/"+loose+"/poster", pngImage(300, 450)); status != 200 {
		t.Fatalf("poster: %d %s", status, body)
	}
	poster, _ := os.ReadFile(filepath.Join(s.firstRoot(), "Some.Unknown.Movie.2019.WEB-DL-poster.jpg"))
	if !bytes.HasPrefix(poster, []byte("\xff\xd8")) {
		t.Errorf("a PNG poster was not stored as JPEG")
	}
	if status, body := kid.get("/api/image/" + loose + "/poster"); status != 200 || body != string(poster) {
		t.Errorf("the catalogue does not show the new poster: %d", status)
	}
	if status, _ := upload(t, boss, "/api/meta/"+loose+"/poster", []byte("not an image")); status != 400 {
		t.Errorf("garbage accepted as a poster: %d", status)
	}
	if status, _ := upload(t, kid, "/api/meta/"+loose+"/poster", pngImage(300, 450)); status != http.StatusForbidden {
		t.Errorf("a viewer changes posters: %d", status)
	}

	// With the tools at hand the tags and the cover go into the file itself.
	t.Setenv("PATH", path)
	if _, err := exec.LookPath("mkvpropedit"); err != nil {
		t.Skip("mkvtoolnix is not installed: the tags inside the file are not checked")
	}
	ffmpeg, _ := exec.LookPath("ffmpeg")
	video := filepath.Join(s.firstRoot(), "Iron Man (2008)", "Iron Man (2008).mkv")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=d=2:s=320x240:r=10", "-c:v", "libx264", video).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	s.live.cfg.Server.NoTags = false
	s.refresh()
	boss.json("/api/library", &lib)
	if status, body := upload(t, boss, "/api/meta/"+lib.Movies[0].ID+"/poster", pngImage(200, 300)); status != 200 || !strings.Contains(body, `"tags":true`) {
		t.Fatalf("poster with tags: %d %s", status, body)
	}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		out, _ := exec.Command("mkvmerge", "-J", video).Output()
		var info struct {
			Attachments []struct {
				FileName string `json:"file_name"`
			}
			Container struct{ Properties struct{ Title string } }
		}
		json.Unmarshal(out, &info)
		if len(info.Attachments) == 1 && info.Attachments[0].FileName == "cover.jpg" && info.Container.Properties.Title == "Iron Man <Mk I>" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the file has no new title and cover: %s", out)
		}
	}
}

func pngImage(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.RGBA{200, 30, 30, 255})
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func upload(t *testing.T, b *browser, path string, data []byte) (int, string) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("image", "image.png")
	part.Write(data)
	form.Close()
	return b.do("POST", path, &rawBody{body.Bytes(), form.FormDataContentType()}, nil)
}

// rawBody is a request body that is sent as it is.
type rawBody struct {
	data        []byte
	contentType string
}

// A cover goes into MP4 as an attached picture and into Matroska as an
// attachment, replacing an earlier one; AVI has no place for it and still
// gets its tags.
func TestCoverInMP4(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	cover := filepath.Join(dir, "cover.jpg")
	exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=s=200x300", "-frames:v", "1", cover).Run()
	for _, name := range []string{"film.mp4", "film.avi", "film.mkv"} {
		video := filepath.Join(dir, name)
		if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=2:s=320x240:r=10",
			"-f", "lavfi", "-i", "sine=d=2", "-c:v", "mpeg4", "-c:a", "mp3", "-shortest", video).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		for i := 0; i < 2; i++ { // twice: the second cover replaces the first
			if err := WriteTags(video, &TagInfo{Title: "Film", Cover: cover}); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		out, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type:stream_disposition=attached_pic:format_tags=title", "-of", "json", video).Output()
		pictures := strings.Count(string(out), `"attached_pic": 1`) + strings.Count(string(out), `"codec_type": "attachment"`)
		want := map[string]int{"film.mp4": 1, "film.avi": 0, "film.mkv": 1}[name]
		if pictures != want || !strings.Contains(strings.ToLower(string(out)), `"title": "film"`) { // Matroska says TITLE
			t.Errorf("%s: %d covers (want %d)\n%s", name, pictures, want, out)
		}
	}
}

// The fields are filled in from a catalogue entry; saving records where they
// came from, takes its artwork, and renames the files when asked to.
func TestFillFromCatalogue(t *testing.T) {
	s, srv := serverFixture(t)
	boss := newBrowser(t, srv, "boss")
	var lib libraryView
	boss.json("/api/library", &lib)
	loose := lib.Movies[1].ID // Some.Unknown.Movie.2019.WEB-DL.avi, no .nfo

	var filled struct {
		Meta                               movieMeta
		IDs                                map[string]string
		Source, SourceName, SourceID       string
		SourceKind, PosterURL, BackdropURL string
	}
	status, body := boss.post("/api/meta/"+loose+"/lookup", resolveRequest{Ref: "tmdb:1726"})
	if status != 200 {
		t.Fatalf("lookup: %d %s", status, body)
	}
	mustUnmarshal(t, body, &filled)
	if filled.Meta.Title != "Железный человек" || filled.Meta.Year != 2008 || filled.SourceName != "TMDB" || filled.IDs["imdb"] != "tt0371746" ||
		!strings.HasSuffix(filled.PosterURL, "/ironman.jpg") || len(filled.Meta.Cast) == 0 || filled.Meta.Cast[0].Thumb == "" {
		t.Fatalf("filled in: %+v", filled)
	}
	// Nothing is saved by a lookup.
	if exists(filepath.Join(s.firstRoot(), "Some.Unknown.Movie.2019.WEB-DL.nfo")) {
		t.Fatalf("a lookup wrote an .nfo")
	}
	// A series picked for a movie file is taken as a movie.
	if status, body := boss.post("/api/meta/"+loose+"/lookup", resolveRequest{Ref: "tv:314"}); status != 200 || !strings.Contains(body, `"sourceKind":"tv"`) {
		t.Errorf("series as a movie: %d %s", status, body)
	}

	save := map[string]any{"rename": true, "source": filled.Source, "sourceId": filled.SourceID, "sourceKind": filled.SourceKind,
		"ids": filled.IDs, "posterUrl": filled.PosterURL, "backdropUrl": filled.BackdropURL}
	data, _ := json.Marshal(filled.Meta)
	json.Unmarshal(data, &save)
	save["plot"] = "Checked and changed by hand."
	if status, body := boss.post("/api/meta/"+loose, save); status != 200 || strings.Contains(body, "problems\":[\"") {
		t.Fatalf("save: %d %s", status, body)
	}
	dir := filepath.Join(s.firstRoot(), moviesFolder, "Железный человек (2008)")
	nfo := mustRead(t, filepath.Join(dir, "Железный человек (2008).nfo"))
	wantAll(t, "nfo", nfo, `<!-- mediakeeper source="tmdb" id="1726" kind="movie" -->`,
		`<uniqueid type="tmdb" default="true">1726</uniqueid>`, `<uniqueid type="imdb">tt0371746</uniqueid>`,
		"<plot>Checked and changed by hand.</plot>", "<thumb>"+filled.Meta.Cast[0].Thumb+"</thumb>")
	if data, _ := os.ReadFile(filepath.Join(dir, "Железный человек (2008).avi")); string(data) != "loose" {
		t.Errorf("the video was not renamed:\n  %s", strings.Join(tree(t, s.firstRoot()), "\n  "))
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "Железный человек (2008)-poster.jpg")); !strings.Contains(string(data), "ironman.jpg") {
		t.Errorf("the catalogue's poster was not taken:\n  %s", strings.Join(tree(t, s.firstRoot()), "\n  "))
	}
	if exists(filepath.Join(s.firstRoot(), "Some.Unknown.Movie.2019.WEB-DL.avi")) {
		t.Errorf("the old file is still there")
	}
	// The new name is in the library, and the run can be undone.
	boss.json("/api/library", &lib)
	found := false
	for _, m := range lib.Movies {
		found = found || m.Title == "Железный человек"
	}
	if !found || len(journals(s.firstRoot())) != 1 {
		t.Errorf("after saving: %+v, journals %d", lib.Movies, len(journals(s.firstRoot())))
	}
}

// A series is edited like a movie: tvshow.nfo gets the new values, the
// episodes their own, and filling it in from a catalogue files it anew.
func TestEditSeries(t *testing.T) {
	s, srv := serverFixture(t)
	boss, kid := newBrowser(t, srv, "boss"), newBrowser(t, srv, "kid")
	var lib libraryView
	boss.json("/api/library", &lib)
	show := lib.Shows[0].ID
	ent := filepath.Join(s.firstRoot(), "Star Trek - Enterprise (2001)")

	if status, _ := kid.get("/api/meta/" + show); status != http.StatusForbidden {
		t.Errorf("a viewer reads the editor: %d", status)
	}
	var m movieMeta
	boss.json("/api/meta/"+show, &m)
	if m.Title != "Star Trek: Enterprise" {
		t.Fatalf("description as it is: %+v", m)
	}
	m.Title, m.Status, m.Year, m.Genres, m.Plot = "Enterprise", "Ended", 2001, []string{"Sci-Fi"}, "Before Kirk."
	m.Directors = []string{"nobody"} // a series has none: not written
	if status, body := boss.post("/api/meta/"+show, m); status != 200 {
		t.Fatalf("save: %d %s", status, body)
	}
	nfo := mustRead(t, filepath.Join(ent, "tvshow.nfo"))
	wantAll(t, "tvshow.nfo", nfo, "<tvshow>", "<title>Enterprise</title>", "<status>Ended</status>", "<plot>Before Kirk.</plot>", "<genre>Sci-Fi</genre>")
	if strings.Contains(nfo, "director") {
		t.Errorf("a series got directors:\n%s", nfo)
	}
	// The episodes that name their series follow the new title.
	if ep := mustRead(t, filepath.Join(ent, "Season 02", "Star Trek - Enterprise S02E01 - Shockwave.nfo")); !strings.Contains(ep, "<showtitle>Enterprise</showtitle>") {
		t.Errorf("episode:\n%s", ep)
	}
	boss.json("/api/library", &lib)
	if lib.Shows[0].Title != "Enterprise" || lib.Shows[0].ID != show {
		t.Errorf("the library does not show the edit: %+v", lib.Shows)
	}

	// The episodes, in order, with what their .nfo files say.
	var eps []episodeView
	boss.json("/api/meta/"+show+"/episodes", &eps)
	if len(eps) != 3 || eps[0].Title != "Broken Bow" || eps[0].EpisodeEnd != 2 || !eps[0].Thumb || eps[1].Title != "" || eps[2].Season != 2 {
		t.Fatalf("episodes: %+v", eps)
	}
	// An episode without an .nfo gets one that places it.
	fight := eps[1]
	if status, body := boss.post("/api/meta/"+fight.ID, episodeMeta{Title: "Fight or Flight", Aired: "2001-10-03", Plot: "A derelict ship."}); status != 200 {
		t.Fatalf("save an episode: %d %s", status, body)
	}
	nfo = mustRead(t, filepath.Join(ent, "Season 01", "Star Trek - Enterprise S01E03 - Fight or Flight.nfo"))
	wantAll(t, "episode nfo", nfo, "<episodedetails>", "<title>Fight or Flight</title>", "<season>1</season>", "<episode>3</episode>",
		"<aired>2001-10-03</aired>", "<plot>A derelict ship.</plot>", "<showtitle>Enterprise</showtitle>")
	if status, _ := boss.post("/api/meta/"+fight.ID, episodeMeta{Title: "x", Aired: "October 3"}); status != 400 {
		t.Errorf("a wrong date accepted: %d", status)
	}
	if status, _ := kid.post("/api/meta/"+fight.ID, episodeMeta{Title: "x"}); status != http.StatusForbidden {
		t.Errorf("a viewer edits an episode: %d", status)
	}

	// Artwork: a season poster and a still.
	if status, body := upload(t, boss, "/api/meta/"+show+"/season02", pngImage(300, 450)); status != 200 {
		t.Fatalf("season poster: %d %s", status, body)
	}
	if status, body := upload(t, boss, "/api/meta/"+fight.ID+"/thumb", pngImage(320, 180)); status != 200 {
		t.Fatalf("still: %d %s", status, body)
	}
	for _, name := range []string{"season02-poster.jpg", "Season 01/Star Trek - Enterprise S01E03 - Fight or Flight-thumb.jpg"} {
		if data, _ := os.ReadFile(filepath.Join(ent, filepath.FromSlash(name))); !bytes.HasPrefix(data, []byte("\xff\xd8")) {
			t.Errorf("%s is not a JPEG", name)
		}
	}

	// Filled in from a catalogue: a movie is not taken for a series.
	if status, body := boss.post("/api/meta/"+show+"/lookup", resolveRequest{Ref: "tmdb:1726"}); status != 400 {
		t.Errorf("a movie for a series: %d %s", status, body)
	}
	var filled struct {
		Meta                               movieMeta
		IDs                                map[string]string
		Source, SourceName, SourceID       string
		SourceKind, PosterURL, BackdropURL string
	}
	status, body := boss.post("/api/meta/"+show+"/lookup", resolveRequest{Ref: "tv:314"})
	if status != 200 {
		t.Fatalf("lookup: %d %s", status, body)
	}
	mustUnmarshal(t, body, &filled)
	if filled.Meta.Title != "Звёздный путь: Энтерпрайз" || filled.SourceKind != kindTV || filled.Meta.Year != 2001 {
		t.Fatalf("filled in: %+v", filled)
	}
	// Saved without renaming, only the description changes.
	save := map[string]any{"source": filled.Source, "sourceId": filled.SourceID, "sourceKind": filled.SourceKind, "ids": filled.IDs}
	data, _ := json.Marshal(filled.Meta)
	json.Unmarshal(data, &save)
	if status, body := boss.post("/api/meta/"+show, save); status != 200 {
		t.Fatalf("save filled in: %d %s", status, body)
	}
	wantAll(t, "tvshow.nfo", mustRead(t, filepath.Join(ent, "tvshow.nfo")),
		`<!-- mediakeeper source="`+filled.Source+`" id="`+filled.SourceID+`" kind="tv" -->`, "<title>Звёздный путь: Энтерпрайз</title>")

	// With renaming, the series is filed anew after the entry, with the
	// changes made by hand.
	save["rename"], save["plot"], save["posterUrl"] = true, "Checked by hand.", filled.PosterURL
	status, body = boss.post("/api/meta/"+show, save)
	if status != 200 {
		t.Fatalf("save and rename: %d %s", status, body)
	}
	var res struct{ ID string }
	mustUnmarshal(t, body, &res)
	dir := filepath.Join(s.firstRoot(), showsFolder, "Звёздный путь - Энтерпрайз (2001)")
	wantAll(t, "tvshow.nfo", mustRead(t, filepath.Join(dir, "tvshow.nfo")), "<plot>Checked by hand.</plot>", `kind="tv"`)
	if !exists(filepath.Join(dir, "Season 01", "Звёздный путь - Энтерпрайз S01E01-E02 - Разорванный круг (1) + Разорванный круг (2).mkv")) || exists(ent) {
		t.Errorf("not filed anew:\n  %s", strings.Join(tree(t, s.firstRoot()), "\n  "))
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "poster.jpg")); string(data) == "SHOWPOSTER" {
		t.Errorf("the catalogue's poster was not taken")
	}
	boss.json("/api/library", &lib)
	if len(lib.Shows) != 1 || lib.Shows[0].ID != res.ID || res.ID == show {
		t.Errorf("the new address of the series: %q, library %+v", res.ID, lib.Shows)
	}
}

// Episodes lying among other files have no folder to describe their series
// in: only filing them from a catalogue puts them into one.
func TestSeriesWithoutFolder(t *testing.T) {
	s, srv := serverFixture(t)
	os.WriteFile(filepath.Join(s.firstRoot(), "Loose.Show.S01E01.mkv"), []byte("x"), 0o644)
	s.refresh()
	boss := newBrowser(t, srv, "boss")
	var lib libraryView
	boss.json("/api/library", &lib)
	var loose string
	for _, sh := range lib.Shows {
		if sh.Title == "Loose Show" {
			loose = sh.ID
		}
	}
	var m struct {
		Title    string
		NoFolder bool
	}
	boss.json("/api/meta/"+loose, &m)
	if m.Title != "Loose Show" || !m.NoFolder {
		t.Fatalf("description: %+v", m)
	}
	if status, body := boss.post("/api/meta/"+loose, movieMeta{Title: "Loose Show", Plot: "x"}); status != 400 || !strings.Contains(body, "folder of their own") {
		t.Errorf("saved without a folder: %d %s", status, body)
	}
	if exists(filepath.Join(s.firstRoot(), "tvshow.nfo")) {
		t.Errorf("the whole library was described as one series")
	}
}

// The film series a movie belongs to is read from the .nfo in both forms
// Kodi wrote, shown in the library, and can be set by hand.
func TestCollection(t *testing.T) {
	s, srv := serverFixture(t)
	boss := newBrowser(t, srv, "boss")
	nfo := filepath.Join(s.firstRoot(), "Iron Man (2008)", "Iron Man (2008).nfo")
	for _, set := range []string{"<set><name>Iron Man Collection</name></set>", "<set>Iron Man Collection</set>"} {
		os.WriteFile(nfo, []byte(nfoHeader+"<movie><title>Iron Man</title><year>2008</year>"+set+"</movie>"), 0o644)
		s.refresh()
		var lib struct {
			Movies []struct{ ID, Title, Collection string }
		}
		boss.json("/api/library", &lib)
		if lib.Movies[0].Collection != "Iron Man Collection" {
			t.Errorf("%s: %+v", set, lib.Movies[0])
		}
	}
	var lib libraryView
	boss.json("/api/library", &lib)
	id := lib.Movies[0].ID
	var m movieMeta
	boss.json("/api/meta/"+id, &m)
	if m.Collection != "Iron Man Collection" {
		t.Fatalf("in the editor: %+v", m)
	}
	m.Collection = "Marvel Cinematic Universe"
	boss.post("/api/meta/"+id, m)
	if text := mustRead(t, nfo); !strings.Contains(text, "<set>\n    <name>Marvel Cinematic Universe</name>\n  </set>") && !strings.Contains(text, "<name>Marvel Cinematic Universe</name>") || strings.Contains(text, "Iron Man Collection") {
		t.Errorf("written:\n%s", text)
	}
	m.Collection = ""
	boss.post("/api/meta/"+id, m)
	if text := mustRead(t, nfo); strings.Contains(text, "<set") {
		t.Errorf("an empty series is removed:\n%s", text)
	}
}
