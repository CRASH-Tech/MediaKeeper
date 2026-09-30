package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() { pbkdf2Rounds = 1000 } // real hashing is deliberately slow; tests sign in a lot

// serverFixture starts the combined server over an organized library (the
// one the DLNA tests use) with an administrator and a viewer.
func serverFixture(t *testing.T) (*Server, *httptest.Server) {
	setup(t, Config{TMDBKey: "tmdbkey", Language: "ru-RU"})
	root := dlnaLibraryFiles(t)
	cfg, _, _ := loadConfig()
	s, err := NewServer(ServerOptions{Root: root, Name: "Test", Port: 8200, DLNA: true, NoTags: true, Config: cfg},
		func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"boss", "kid"} { // boss is an administrator
		if _, err := s.auth.SetUser(name, name+"-password", name == "boss"); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.dl.Close() })
	return s, srv
}

// browser is a client with cookies, like the web interface.
type browser struct {
	t    *testing.T
	base string
	http *http.Client
}

func newBrowser(t *testing.T, srv *httptest.Server, user string) *browser {
	jar, _ := cookiejar.New(nil)
	b := &browser{t: t, base: srv.URL, http: &http.Client{Jar: jar}}
	if user != "" {
		if status, body := b.post("/api/login", map[string]string{"username": user, "password": user + "-password"}); status != 200 {
			t.Fatalf("login as %s: %d %s", user, status, body)
		}
	}
	return b
}

func (b *browser) do(method, path string, body any, header map[string]string) (int, string) {
	b.t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, b.base+path, reader)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func (b *browser) get(path string) (int, string)            { return b.do("GET", path, nil, nil) }
func (b *browser) post(path string, body any) (int, string) { return b.do("POST", path, body, nil) }

func (b *browser) json(path string, v any) {
	b.t.Helper()
	status, body := b.get(path)
	if status != 200 {
		b.t.Fatalf("GET %s: %d %s", path, status, body)
	}
	if err := json.Unmarshal([]byte(body), v); err != nil {
		b.t.Fatalf("GET %s: %v\n%s", path, err, body)
	}
}

type libraryView struct {
	Movies []struct {
		ID, Title, LocalTitle string
		Year                  int
		Poster, Played        bool
		Position              float64
	}
	Shows []struct {
		ID, Title string
		Seasons   []struct {
			Number   int
			Episodes []struct {
				ID, Title string
				Episode   int
			}
		}
	}
}

func TestWebInterface(t *testing.T) {
	_, srv := serverFixture(t)

	anon := newBrowser(t, srv, "")
	if status, body := anon.get("/"); status != 200 || !strings.Contains(body, "/static/app.js") {
		t.Errorf("index: %d", status)
	}
	if status, body := anon.get("/static/app.js"); status != 200 || !strings.Contains(body, "renderDownloads") {
		t.Errorf("app.js: %d", status)
	}
	for _, path := range []string{"/api/library", "/api/me", "/api/downloads", "/api/users"} {
		if status, _ := anon.get(path); status != http.StatusUnauthorized {
			t.Errorf("%s without login: %d, want 401", path, status)
		}
	}
	if status, _ := anon.post("/api/login", map[string]string{"username": "boss", "password": "wrong"}); status != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", status)
	}

	kid := newBrowser(t, srv, "kid")
	var lib libraryView
	kid.json("/api/library", &lib)
	if len(lib.Movies) != 2 || lib.Movies[0].Title != "Iron Man & Co" || !lib.Movies[0].Poster || lib.Movies[0].Year != 2008 {
		t.Fatalf("movies: %+v", lib.Movies)
	}
	if len(lib.Shows) != 1 || lib.Shows[0].Title != "Star Trek: Enterprise" || len(lib.Shows[0].Seasons) != 2 ||
		lib.Shows[0].Seasons[0].Episodes[0].Title != "Broken Bow" {
		t.Fatalf("shows: %+v", lib.Shows)
	}
	movie := lib.Movies[0].ID

	// Streaming with seeking, images, subtitles converted for the browser.
	if status, body := kid.do("GET", "/api/stream/"+movie, nil, map[string]string{"Range": "bytes=2-5"}); status != 206 || body != "2345" {
		t.Errorf("stream: %d %q", status, body)
	}
	if status, body := kid.get("/api/image/" + movie + "/poster"); status != 200 || body != "POSTER" {
		t.Errorf("poster: %d %q", status, body)
	}
	episode := lib.Shows[0].Seasons[0].Episodes[1].ID // no still of its own: the season poster
	if status, body := kid.get("/api/image/" + episode + "/poster"); status != 200 || body != "S1POSTER" {
		t.Errorf("episode image: %d %q", status, body)
	}
	if status, body := kid.get("/api/subs/" + movie + "/0.vtt?offset=0.5"); status != 200 ||
		!strings.HasPrefix(body, "WEBVTT\n\n1\n00:00:00.500 --> 00:00:01.500\nHi") {
		t.Errorf("subtitles: %d %q", status, body)
	}
	if status, _ := kid.get("/api/transcode/" + movie); status != http.StatusNotImplemented {
		t.Errorf("transcode without ffmpeg: %d, want 501", status)
	}
	if status, _ := kid.get("/api/stream/0123456789abcdef0123456789abcdef"); status != 404 {
		t.Errorf("unknown video: %d", status)
	}

	// Progress is kept per user; near the end a video counts as watched.
	kid.post("/api/progress/"+movie, map[string]float64{"position": 1500})
	kid.json("/api/library", &lib)
	if lib.Movies[0].Position != 1500 || lib.Movies[0].Played {
		t.Errorf("progress: %+v", lib.Movies[0])
	}
	kid.post("/api/progress/"+movie, map[string]float64{"position": 126*60 - 30})
	kid.json("/api/library", &lib)
	if lib.Movies[0].Position != 0 || !lib.Movies[0].Played {
		t.Errorf("watched: %+v", lib.Movies[0])
	}
	boss := newBrowser(t, srv, "boss")
	boss.json("/api/library", &lib)
	if lib.Movies[0].Played {
		t.Errorf("one user's progress shows for another")
	}

	// A viewer neither downloads nor manages users.
	for _, c := range [][2]string{{"GET", "/api/downloads"}, {"POST", "/api/downloads"}, {"GET", "/api/users"}, {"POST", "/api/users"}} {
		if status, _ := kid.do(c[0], c[1], map[string]string{"source": "http://x/y.mkv", "name": "z", "password": "12345"}, nil); status != http.StatusForbidden {
			t.Errorf("viewer %s %s: %d, want 403", c[0], c[1], status)
		}
	}

	// User management.
	if status, body := boss.post("/api/users", map[string]any{"name": "guest", "password": "guest-password"}); status != 200 {
		t.Fatalf("add user: %d %s", status, body)
	}
	var users []User
	boss.json("/api/users", &users)
	if len(users) != 3 || strings.Contains(mustJSON(users), "pbkdf2") {
		t.Errorf("users: %s", mustJSON(users))
	}
	guest := newBrowser(t, srv, "guest")
	if status, _ := boss.do("DELETE", "/api/users/"+users[2].ID, nil, nil); status != 200 {
		t.Errorf("delete user: %d", status)
	}
	if status, _ := guest.get("/api/library"); status != http.StatusUnauthorized {
		t.Errorf("a deleted user is still signed in: %d", status)
	}
	if status, _ := boss.do("DELETE", "/api/users/"+users[0].ID, nil, nil); status != http.StatusBadRequest {
		t.Errorf("deleting oneself: %d, want 400", status)
	}

	// Sign out ends the session.
	kid.post("/api/logout", nil)
	if status, _ := kid.get("/api/library"); status != http.StatusUnauthorized {
		t.Errorf("after logout: %d", status)
	}
}

func mustJSON(v any) string { data, _ := json.Marshal(v); return string(data) }

func mustUnmarshal(t *testing.T, text string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(text), v); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
}

func TestLoginThrottle(t *testing.T) {
	_, srv := serverFixture(t)
	b := newBrowser(t, srv, "")
	for i := 0; i < loginAttempts; i++ {
		b.post("/api/login", map[string]string{"username": "boss", "password": "guess"})
	}
	// Even the right password is refused now.
	if status, _ := b.post("/api/login", map[string]string{"username": "boss", "password": "boss-password"}); status != http.StatusTooManyRequests {
		t.Errorf("after %d failures: %d, want 429", loginAttempts, status)
	}
}

func TestPasswordHash(t *testing.T) {
	hash := hashPassword("secret")
	if !checkPassword(hash, "secret") || checkPassword(hash, "Secret") || checkPassword("", "secret") || checkPassword("x$y", "") {
		t.Errorf("password check is wrong for %s", hash)
	}
	if hashPassword("secret") == hash {
		t.Errorf("two hashes of one password are equal: no salt")
	}
}

// The Jellyfin API as a native client uses it.
func TestJellyfinAPI(t *testing.T) {
	_, srv := serverFixture(t)
	c := newBrowser(t, srv, "")
	auth := `MediaBrowser Client="Test", Device="dev", DeviceId="1", Version="1"`
	call := func(method, path string, body any, v any) int {
		t.Helper()
		status, text := c.do(method, path, body, map[string]string{"X-Emby-Authorization": auth})
		if v != nil {
			if err := json.Unmarshal([]byte(text), v); err != nil {
				t.Fatalf("%s %s: %d %v\n%s", method, path, status, err, text)
			}
		}
		return status
	}
	type item struct {
		Id, Name, Type, CollectionType, SeriesName string
		IndexNumber, ParentIndexNumber             int
		RunTimeTicks                               int64
		ImageTags                                  map[string]string
		UserData                                   struct {
			Played                bool
			PlaybackPositionTicks int64
			UnplayedItemCount     int
		}
		MediaSources []struct {
			Id, Container                           string
			SupportsDirectPlay, SupportsTranscoding bool
			MediaStreams                            []struct {
				Type, Codec, DeliveryUrl string
				IsExternal               bool
			}
		}
	}
	type list struct {
		Items            []item
		TotalRecordCount int
	}

	var info struct{ Id, ProductName, Version string }
	if call("GET", "/System/Info/Public", nil, &info); info.ProductName != "Jellyfin Server" || len(info.Id) != 32 {
		t.Fatalf("public info: %+v", info)
	}
	if status := call("GET", "/Users/Me", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("without a token: %d", status)
	}
	if status := call("POST", "/Users/AuthenticateByName", map[string]string{"Username": "kid", "Pw": "no"}, nil); status != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", status)
	}
	var login struct {
		AccessToken string
		User        struct {
			Id, Name string
			Policy   struct{ IsAdministrator bool }
		}
	}
	// Paths are matched without regard to case.
	if call("POST", "/users/authenticatebyname", map[string]string{"Username": "kid", "Pw": "kid-password"}, &login); login.AccessToken == "" ||
		login.User.Name != "kid" || login.User.Policy.IsAdministrator || len(login.User.Id) != 32 {
		t.Fatalf("login: %+v", login)
	}
	auth += `, Token="` + login.AccessToken + `"`
	uid := login.User.Id

	var views list
	call("GET", "/Users/"+uid+"/Views", nil, &views)
	if len(views.Items) != 2 || views.Items[0].CollectionType != "movies" || views.Items[1].CollectionType != "tvshows" {
		t.Fatalf("views: %+v", views.Items)
	}
	var movies list
	call("GET", "/Users/"+uid+"/Items?ParentId="+views.Items[0].Id+"&SortBy=SortName", nil, &movies)
	if movies.TotalRecordCount != 2 || movies.Items[0].Name != "Iron Man & Co" || movies.Items[0].Type != "Movie" ||
		movies.Items[0].RunTimeTicks != 126*60*1e7 || movies.Items[0].ImageTags["Primary"] == "" {
		t.Fatalf("movies: %+v", movies.Items)
	}
	movie := movies.Items[0]

	// camelCase parameters, a search, paging.
	var found list
	call("GET", "/Items?userId="+uid+"&recursive=true&includeItemTypes=Episode&searchTerm=broken", nil, &found)
	if len(found.Items) != 1 || found.Items[0].Type != "Episode" || found.Items[0].SeriesName != "Star Trek: Enterprise" {
		t.Errorf("search: %+v", found.Items)
	}
	call("GET", "/Items?Recursive=true&IncludeItemTypes=Movie,Episode&Limit=2&StartIndex=1", nil, &found)
	if found.TotalRecordCount != 5 || len(found.Items) != 2 {
		t.Errorf("paging: %d of %d", len(found.Items), found.TotalRecordCount)
	}

	var full item
	call("GET", "/Users/"+uid+"/Items/"+movie.Id, nil, &full)
	if len(full.MediaSources) != 1 || !full.MediaSources[0].SupportsDirectPlay || full.MediaSources[0].SupportsTranscoding ||
		full.MediaSources[0].Container != "mkv" {
		t.Fatalf("media sources: %+v", full.MediaSources)
	}
	streams := full.MediaSources[0].MediaStreams
	if len(streams) != 1 || !streams[0].IsExternal || streams[0].Type != "Subtitle" {
		t.Fatalf("streams: %+v", streams)
	}
	if status, body := c.get(streams[0].DeliveryUrl); status != 200 || !strings.Contains(body, "-->") {
		t.Errorf("external subtitles: %d %q", status, body)
	}

	// The file itself: by header and by api_key alone; images need no login.
	if status, body := c.do("GET", "/Videos/"+movie.Id+"/stream.mkv?static=true", nil,
		map[string]string{"X-Emby-Authorization": auth, "Range": "bytes=0-3"}); status != 206 || body != "0123" {
		t.Errorf("stream: %d %q", status, body)
	}
	if status, body := c.get("/Videos/" + movie.Id + "/stream?api_key=" + login.AccessToken); status != 200 || body != "0123456789" {
		t.Errorf("stream by api_key: %d %q", status, body)
	}
	if status, _ := c.get("/Videos/" + movie.Id + "/stream"); status != http.StatusUnauthorized {
		t.Errorf("stream without a token: %d", status)
	}
	if status, _ := c.do("GET", "/Videos/"+movie.Id+"/master.m3u8", nil, map[string]string{"X-Emby-Authorization": auth}); status != 404 {
		t.Errorf("HLS is not offered, got %d", status)
	}
	if status, body := c.get("/Items/" + movie.Id + "/Images/Primary?maxWidth=200"); status != 200 || body != "POSTER" {
		t.Errorf("image: %d %q", status, body)
	}

	// Series -> seasons -> episodes.
	var shows, seasons, episodes list
	call("GET", "/Users/"+uid+"/Items?ParentId="+views.Items[1].Id, nil, &shows)
	call("GET", "/Shows/"+shows.Items[0].Id+"/Seasons?userId="+uid, nil, &seasons)
	call("GET", "/Shows/"+shows.Items[0].Id+"/Episodes?seasonId="+seasons.Items[0].Id, nil, &episodes)
	if len(seasons.Items) != 2 || seasons.Items[0].Name != "Season 1" || len(episodes.Items) != 2 ||
		episodes.Items[0].IndexNumber != 1 || episodes.Items[1].IndexNumber != 3 || episodes.Items[0].ParentIndexNumber != 1 {
		t.Fatalf("seasons %+v\nepisodes %+v", seasons.Items, episodes.Items)
	}

	// Progress reports, resume, next up, marking a whole series.
	first := episodes.Items[0]
	call("POST", "/Sessions/Playing/Progress", map[string]any{"ItemId": first.Id, "PositionTicks": int64(600 * 1e7)}, nil)
	var resume, next list
	call("GET", "/Users/"+uid+"/Items/Resume", nil, &resume)
	if len(resume.Items) != 1 || resume.Items[0].Id != first.Id || resume.Items[0].UserData.PlaybackPositionTicks != 600*1e7 {
		t.Errorf("resume: %+v", resume.Items)
	}
	call("POST", "/Users/"+uid+"/PlayedItems/"+first.Id, nil, nil)
	call("GET", "/Shows/NextUp?userId="+uid, nil, &next)
	if len(next.Items) != 1 || next.Items[0].Id != episodes.Items[1].Id {
		t.Errorf("next up: %+v", next.Items)
	}
	var data struct {
		Played            bool
		UnplayedItemCount int
	}
	call("POST", "/UserPlayedItems/"+shows.Items[0].Id, nil, &data)
	if !data.Played || data.UnplayedItemCount != 0 {
		t.Errorf("series marked played: %+v", data)
	}
	call("DELETE", "/Users/"+uid+"/PlayedItems/"+shows.Items[0].Id, nil, &data)
	if data.Played || data.UnplayedItemCount != 3 {
		t.Errorf("series unmarked: %+v", data)
	}

	// Fixed paths win over {id}; the old /emby prefix works; unknown is 404.
	if status := call("GET", "/Items/Filters", nil, nil); status != 200 {
		t.Errorf("/Items/Filters: %d", status)
	}
	if status := call("GET", "/emby/System/Info", nil, nil); status != 200 {
		t.Errorf("/emby prefix: %d", status)
	}
	if status := call("GET", "/Nothing/Here", nil, nil); status != 404 {
		t.Errorf("unknown endpoint: %d", status)
	}
	call("POST", "/Sessions/Logout", nil, nil)
	if status := call("GET", "/Users/Me", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("token after logout: %d", status)
	}

	// DLNA still answers on the same port.
	if status, body := c.get("/rootDesc.xml"); status != 200 || !strings.Contains(body, "MediaServer") {
		t.Errorf("DLNA description: %d", status)
	}
}

type downloadView struct {
	ID, Name, State, Error, Log string
	Pending                     []pendingUnit
}

// waitDownloads polls until no download is in progress.
func waitDownloads(t *testing.T, b *browser) []downloadView {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var list []downloadView
		b.json("/api/downloads", &list)
		busy := false
		for _, d := range list {
			busy = busy || d.State == stateDownloading || d.State == stateOrganizing
		}
		if !busy {
			return list
		}
		if time.Now().After(deadline) {
			t.Fatalf("downloads did not finish: %+v", list)
		}
	}
}

// A link is downloaded, identified and filed; what is unclear waits for
// the administrator's decision.
func TestDownloads(t *testing.T) {
	s, srv := serverFixture(t)
	boss := newBrowser(t, srv, "boss")
	add := func(source string) (int, string) {
		return boss.post("/api/downloads", map[string]string{"source": source})
	}

	for source, want := range map[string]string{
		"ftp://host/file.mkv":                  "give a magnet link",
		"just words":                           "give a magnet link",
		"magnet:?xt=urn:btih:abc&dn=Some+Film": "aria2c is not installed",
		fakeURL + "/files/thing.torrent":       "aria2c is not installed",
	} {
		if status, body := add(source); status != http.StatusBadRequest || !strings.Contains(body, want) {
			t.Errorf("%s: %d %s", source, status, body)
		}
	}

	add(fakeURL + "/files/Iron.Man.2008.BDRip.mkv")             // in the library already
	add(fakeURL + "/files/Myatezh.2025.AMZN.WEB-DLRip.AVC.mkv") // unknown to the catalogues by this name
	add(fakeURL + "/files/notes.txt")
	add(fakeURL + "/files/missing.mkv")
	add(fakeURL + "/files/Star.Trek.Enterprise.s1e01-02.Broken.Bow.mkv")
	list := waitDownloads(t, boss)
	byName := map[string]downloadView{}
	for _, d := range list {
		byName[d.Name] = d
	}
	if d := byName[fakeURL+"/files/notes.txt"]; d.State != stateError || !strings.Contains(d.Error, "not a video") {
		t.Errorf("text file: %+v", d)
	}
	if d := byName[fakeURL+"/files/missing.mkv"]; d.State != stateError || !strings.Contains(d.Error, "404") {
		t.Errorf("missing file: %+v", d)
	}
	// The fixture library already holds this double episode under its English
	// name; the download is filed by the TMDB (Russian) one next to it.
	ent := filepath.Join(s.root, showsFolder, "Звёздный путь - Энтерпрайз (2001)")
	if d := byName["Star.Trek.Enterprise.s1e01-02.Broken.Bow.mkv"]; d.State != stateDone {
		t.Errorf("episode: %+v", d)
	}
	if data, _ := os.ReadFile(filepath.Join(ent, "Season 01", "Звёздный путь - Энтерпрайз S01E01-E02 - Разорванный круг (1) + Разорванный круг (2).mkv")); string(data) != "video:Star.Trek.Enterprise.s1e01-02.Broken.Bow.mkv" {
		t.Errorf("episode file: %q\n  %s", data, strings.Join(tree(t, s.root), "\n  "))
	}
	iron := byName["Iron.Man.2008.BDRip.mkv"]
	if iron.State != stateDone {
		t.Errorf("movie: %+v", iron)
	}
	if data, _ := os.ReadFile(filepath.Join(s.root, moviesFolder, "Железный человек (2008)", "Железный человек (2008).mkv")); string(data) != "video:Iron.Man.2008.BDRip.mkv" {
		t.Errorf("movie file: %q", data)
	}

	// The unclear one waits, with its files still in .incoming.
	waiting := byName["Myatezh.2025.AMZN.WEB-DLRip.AVC.mkv"]
	if waiting.State != stateAttention || len(waiting.Pending) != 1 || waiting.Pending[0].Title != "Myatezh" || waiting.Pending[0].Kind != kindMovie {
		t.Fatalf("waiting: %+v", waiting)
	}
	key := waiting.Pending[0].Key
	var candidates []candidate
	boss.json("/api/downloads/"+waiting.ID+"/search?key="+key, &candidates)
	if len(candidates) == 0 || candidates[0].SourceName != "Wikidata" || candidates[0].ID != "tt32338669" {
		t.Fatalf("candidates for the guessed name: %+v", candidates)
	}
	boss.json("/api/downloads/"+waiting.ID+"/search?key="+key+"&q=Iron+Man", &candidates)
	if len(candidates) == 0 || candidates[0].SourceName != "TMDB" || candidates[0].Title != "Железный человек" {
		t.Errorf("candidates for a typed title: %+v", candidates)
	}
	// A series for a file without an episode number needs a decision.
	if status, body := boss.post("/api/downloads/"+waiting.ID+"/resolve", resolveRequest{Key: key, Source: "tmdb", ID: "314", Kind: kindTV}); status != 400 || !strings.Contains(body, "season and the episode") {
		t.Errorf("series without an episode: %d %s", status, body)
	}
	if status, body := boss.post("/api/downloads/"+waiting.ID+"/resolve", resolveRequest{Key: key, Ref: "nonsense"}); status != 400 {
		t.Errorf("bad reference: %d %s", status, body)
	}
	// The right answer, by IMDb number.
	if status, body := boss.post("/api/downloads/"+waiting.ID+"/resolve", resolveRequest{Key: key, Ref: "tt7777777"}); status != 200 {
		t.Fatalf("resolve: %d %s", status, body)
	}
	for _, d := range waitDownloads(t, boss) {
		if d.ID == waiting.ID && (d.State != stateDone || len(d.Pending) != 0) {
			t.Errorf("after resolving: %+v", d)
		}
	}
	if _, err := os.Stat(filepath.Join(s.root, moviesFolder, "Мятеж (2025)", "Мятеж (2025).mkv")); err != nil {
		t.Errorf("resolved movie is not in the library:\n  %s", strings.Join(tree(t, s.root), "\n  "))
	}
	if left, _ := os.ReadDir(filepath.Join(s.root, incomingDir)); len(left) != 1 { // only downloads.json
		t.Errorf("left in %s: %v", incomingDir, left)
	}

	// The catalogue shows the new titles at once.
	var lib libraryView
	boss.json("/api/library", &lib)
	var titles []string
	for _, m := range lib.Movies {
		titles = append(titles, m.Title)
	}
	if got := strings.Join(titles, " | "); got != "Iron Man & Co | Some Unknown Movie | Железный человек | Мятеж" {
		t.Errorf("movies after downloads: %s", got)
	}

	// Entries can be removed; a restart keeps the list.
	if status, _ := boss.do("DELETE", "/api/downloads/"+waiting.ID, nil, nil); status != 200 {
		t.Errorf("remove: %d", status)
	}
	again := NewDownloads(s)
	if len(again.list) != len(list)-1 {
		t.Errorf("after a restart: %d entries, want %d", len(again.list), len(list)-1)
	}
}

// With guests allowed, the web interface can be watched without an account,
// but nothing is remembered for a guest and nothing can be changed.
func TestGuests(t *testing.T) {
	s, srv := serverFixture(t)
	s.guests = true
	guest := newBrowser(t, srv, "")
	var me struct {
		Name  string
		Guest bool
		Admin bool
	}
	guest.json("/api/me", &me)
	if !me.Guest || me.Admin {
		t.Fatalf("me: %+v", me)
	}
	var lib libraryView
	guest.json("/api/library", &lib)
	movie := lib.Movies[0].ID
	if status, body := guest.do("GET", "/api/stream/"+movie, nil, map[string]string{"Range": "bytes=0-1"}); status != 206 || body != "01" {
		t.Errorf("a guest cannot watch: %d %q", status, body)
	}
	guest.post("/api/progress/"+movie, map[string]any{"position": 1500})
	guest.json("/api/library", &lib)
	if lib.Movies[0].Position != 0 {
		t.Errorf("progress was kept for a guest")
	}
	for _, c := range [][2]string{{"GET", "/api/downloads"}, {"POST", "/api/downloads"}, {"GET", "/api/users"},
		{"POST", "/api/fix/" + movie}, {"GET", "/api/fix/" + movie + "/search"}} {
		if status, _ := guest.do(c[0], c[1], map[string]string{"source": "http://x/y.mkv"}, nil); status != http.StatusForbidden {
			t.Errorf("guest %s %s: %d, want 403", c[0], c[1], status)
		}
	}
	// Guests are told apart, so that one's conversion does not end another's.
	other := newBrowser(t, srv, "")
	a, b := s.guest(httptest.NewRecorder(), cookieRequest(guest)), s.guest(httptest.NewRecorder(), cookieRequest(other))
	if a.ID == b.ID {
		t.Errorf("two guests share an identity: %s", a.ID)
	}
	// The Jellyfin API still needs an account.
	if status, _ := guest.get("/Users/Me"); status != http.StatusUnauthorized {
		t.Errorf("Jellyfin API for a guest: %d", status)
	}
	// Signing in still works, and gives the account's rights.
	guest.post("/api/login", map[string]string{"username": "boss", "password": "boss-password"})
	guest.json("/api/me", &me)
	if me.Guest || !me.Admin {
		t.Errorf("after signing in: %+v", me)
	}
}

// cookieRequest is a request carrying the browser's cookies.
func cookieRequest(b *browser) *http.Request {
	req := httptest.NewRequest("GET", b.base+"/api/me", nil)
	u, _ := url.Parse(b.base)
	for _, c := range b.http.Jar.Cookies(u) {
		req.AddCookie(c)
	}
	return req
}
