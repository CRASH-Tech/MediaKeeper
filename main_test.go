package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestParsePath(t *testing.T) {
	tests := []struct {
		rel  string
		want Guess
	}{
		{"Iron Man (2008) IMAX.mkv", Guess{Title: "Iron Man", Year: 2008}},
		{"Myatezh.2025.AMZN.WEB-DLRip.AVC.mkv", Guess{Title: "Myatezh", Year: 2025}},
		{"Атомный Поезд (Екатеринбург Арт).avi", Guess{Title: "Атомный Поезд"}},
		{"На линии огня.mkv", Guess{Title: "На линии огня"}},
		{"Форсаж. На пределе скорости_2026_WEB-DLRip.avi", Guess{Title: "Форсаж. На пределе скорости", Year: 2026}},
		{"Startrek/Star.Trek.Enterprise.s1e01-02.Broken.Bow.mkv",
			Guess{Title: "Star Trek Enterprise", IsSeries: true, Season: 1, Episodes: []int{1, 2}}},
		{"Startrek/Star.Trek.Enterprise.s2e01.Shockwave,Pt.2.mkv",
			Guess{Title: "Star Trek Enterprise", IsSeries: true, Season: 2, Episodes: []int{1}}},
		{"Prison Break/Season 1/Prison Break (2005) - S01E02 - Allen (1080p BluRay x265 Silence).mkv",
			Guess{Title: "Prison Break", Year: 2005, IsSeries: true, Season: 1, Episodes: []int{2}}},
		{"Blade.Runner.2049.2017.1080p.BluRay.x264.mkv", Guess{Title: "Blade Runner 2049", Year: 2017}},
		{"2001.A.Space.Odyssey.1968.mkv", Guess{Title: "2001 A Space Odyssey", Year: 1968}},
		{"The.Matrix.1080p.BDRip.mkv", Guess{Title: "The Matrix"}},
		{"Lost/Season 2/05 - And Found.mkv", Guess{Title: "Lost", IsSeries: true, Season: 2, Episodes: []int{5}}},
		{"Друзья/Сезон 3/Серия 7.avi", Guess{Title: "Друзья", IsSeries: true, Season: 3, Episodes: []int{7}}},
		{"Show.Name.3x07.HDTV.avi", Guess{Title: "Show Name", IsSeries: true, Season: 3, Episodes: []int{7}}},
		{"Show S01E01E02.mkv", Guess{Title: "Show", IsSeries: true, Season: 1, Episodes: []int{1, 2}}},
		{"Movie [1920x1080].mkv", Guess{Title: "Movie"}},
	}
	for _, tt := range tests {
		if got := ParsePath(filepath.FromSlash(tt.rel)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tt.rel, got, tt.want)
		}
	}
}

func TestParseRef(t *testing.T) {
	tests := map[string]Ref{
		"tt0371746": {"imdb", "tt0371746", ""},
		"https://www.imdb.com/title/tt0371746/?x=1":    {"imdb", "tt0371746", ""},
		"https://www.themoviedb.org/tv/314-enterprise": {"tmdb", "314", "tv"},
		"tmdb:1726":                            {"tmdb", "1726", ""},
		"tv:314":                               {"tmdb", "314", "tv"},
		"kp:61237":                             {"kinopoisk", "61237", ""},
		"https://www.kinopoisk.ru/film/61237/": {"kinopoisk", "61237", ""},
		"https://letterboxd.com/film/iron-man-2008/": {"letterboxd", "letterboxd.com/film/iron-man-2008", ""},
		"https://boxd.it/2a0o":                       {"letterboxd", "boxd.it/2a0o", ""},
		"lb:iron-man-2008":                           {"letterboxd", "iron-man-2008", ""},
		"https://www.tvmaze.com/shows/714/star-trek": {"tvmaze", "714", "tv"},
		"tvmaze:714":                                 {"tvmaze", "714", "tv"},
	}
	for in, want := range tests {
		if got, ok := parseRef(in); !ok || got != want {
			t.Errorf("%q: got %+v %v, want %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{"Железный человек", "iron man 2", "attack", "Movie 43", "tv 2000"} {
		if _, ok := parseRef(in); ok {
			t.Errorf("%q must be a search query, not an ID", in)
		}
	}
}

func TestAutoPick(t *testing.T) {
	rs := []SearchResult{
		{ID: "a", Title: "Iron Man", Year: 2008},
		{ID: "b", Title: "Iron Man 2", Year: 2010},
		{ID: "c", Title: "Iron Man", Year: 2008}, // an obscure namesake
	}
	if r := autoPick("Iron Man", 2008, rs); r == nil || r.ID != "a" {
		t.Errorf("top-ranked exact match of the right year must win, got %+v", r)
	}
	if r := autoPick("Iron Man", 0, rs); r != nil {
		t.Errorf("without a year namesakes are ambiguous, got %+v", r)
	}
	if r := autoPick("Iron Man", 2008, []SearchResult{rs[1], rs[0], rs[2]}); r != nil {
		t.Errorf("namesakes below the top are ambiguous, got %+v", r)
	}
}

func TestSanitize(t *testing.T) {
	tests := map[string]string{
		"Star Trek: Enterprise": "Star Trek - Enterprise",
		"What If...?":           "What If",
		"AC/DC":                 "AC-DC",
	}
	for in, want := range tests {
		if got := sanitize(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

type obj = map[string]any

// wikidataTitleLookups counts the Russian-title lookups that found nothing.
var wikidataTitleLookups int

const lbIronMan = `<html><head>
<meta property="og:title" content="Iron Man (2008)" />
<meta property="og:image" content="IMG/lb-backdrop.jpg" />
</head><body class="film backdropped" data-type="film" data-tmdb-type="movie" data-tmdb-id="1726">
<h4 class="tagline">Heroes aren&#039;t born. They&#039;re built.</h4>
<a href="http://www.imdb.com/title/tt0371746/maindetails">IMDb</a>
<script type="application/ld+json">
/* <![CDATA[ */
{"image":"IMG/lb-poster.jpg","@type":"Movie","director":[{"@type":"Person","name":"Jon Favreau"}],
"description":"Tony Stark builds a suit.","duration":"PT2H6M","dateCreated":"2008-04-30","name":"Iron Man",
"genre":["Adventure","Action"],"actor":[{"name":"Robert Downey Jr."},{"name":"Jeff Bridges"}],
"productionCompany":[{"name":"Marvel Studios"}],"countryOfOrigin":[{"name":"USA"}],
"aggregateRating":{"ratingValue":3.82}}
/* ]]> */
</script></body></html>`

// fakeServices imitates every catalogue the program talks to and points
// the providers at it.
func fakeServices(t *testing.T) *httptest.Server {
	reply := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	img := srv.URL + "/img"

	// --- TMDB
	ironMan := obj{"id": 1726, "title": "Железный человек", "original_title": "Iron Man", "release_date": "2008-04-30"}
	mutiny := obj{"id": 777, "title": "Мятеж", "original_title": "Мятеж", "release_date": "2025-03-01"}
	enterprise := obj{"id": 314, "name": "Звёздный путь: Энтерпрайз", "original_name": "Star Trek: Enterprise", "first_air_date": "2001-09-26"}
	lineOfFire := obj{"id": 10, "title": "На линии огня", "original_title": "In the Line of Fire", "release_date": "1993-07-08"}
	with := func(base, extra obj) obj {
		for k, v := range base {
			extra[k] = v
		}
		return extra
	}
	mux.HandleFunc("/tmdb/search/movie", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "tmdbkey" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var rs []obj
		switch r.URL.Query().Get("query") {
		case "Iron Man", "Железный человек":
			rs = []obj{ironMan, {"id": 2, "title": "Железный человек 2", "original_title": "Iron Man 2", "release_date": "2010-04-28"}}
		case "На линии огня":
			rs = []obj{lineOfFire, {"id": 11, "title": "На линии огня", "original_title": "Line of Fire", "release_date": "2003-01-01"}}
		}
		reply(w, obj{"results": rs})
	})
	mux.HandleFunc("/tmdb/search/tv", func(w http.ResponseWriter, r *http.Request) {
		var rs []obj
		if r.URL.Query().Get("query") == "Star Trek Enterprise" {
			rs = []obj{enterprise}
		}
		reply(w, obj{"results": rs})
	})
	mux.HandleFunc("/tmdb/find/tt7777777", func(w http.ResponseWriter, r *http.Request) {
		reply(w, obj{"movie_results": []obj{mutiny}})
	})
	mux.HandleFunc("/tmdb/movie/1726", func(w http.ResponseWriter, r *http.Request) {
		reply(w, with(ironMan, obj{"imdb_id": "tt0371746", "overview": "Миллиардер <Тони> & броня.", "runtime": 126,
			"poster_path": "/ironman.jpg", "backdrop_path": "/ironman-bg.jpg", "vote_average": 7.649,
			"genres": []obj{{"name": "боевик"}, {"name": "фантастика"}},
			"credits": obj{
				"cast": []obj{{"name": "Роберт Дауни мл.", "character": "Tony Stark", "profile_path": "/rdj.jpg"}},
				"crew": []obj{{"name": "Джон Фавро", "job": "Director", "department": "Directing"}},
			}}))
	})
	mux.HandleFunc("/tmdb/movie/777", func(w http.ResponseWriter, r *http.Request) {
		reply(w, with(mutiny, obj{"imdb_id": "tt7777777", "poster_path": "/mutiny.jpg"}))
	})
	mux.HandleFunc("/tmdb/movie/10", func(w http.ResponseWriter, r *http.Request) { reply(w, lineOfFire) })
	mux.HandleFunc("/tmdb/tv/314", func(w http.ResponseWriter, r *http.Request) {
		reply(w, with(enterprise, obj{"poster_path": "/ent.jpg",
			"external_ids": obj{"imdb_id": "tt0244365", "tvdb_id": 73893},
			"seasons":      []obj{{"season_number": 1, "poster_path": "/ent-s1.jpg"}}}))
	})
	mux.HandleFunc("/tmdb/tv/314/season/1", func(w http.ResponseWriter, r *http.Request) {
		reply(w, obj{"season_number": 1, "poster_path": "/ent-s1.jpg", "episodes": []obj{
			{"episode_number": 1, "name": "Разорванный круг (1)", "still_path": "/ent-s1e1.jpg", "air_date": "2001-09-26"},
			{"episode_number": 2, "name": "Разорванный круг (2)"},
		}})
	})
	mux.HandleFunc("/tmdb/tv/314/season/2", func(w http.ResponseWriter, r *http.Request) {
		reply(w, obj{"season_number": 2, "episodes": []obj{{"episode_number": 1, "name": "Ударная волна: Часть 2"}}})
	})

	// --- TVMaze
	tvmShow := obj{"id": 714, "name": "Star Trek: Enterprise", "premiered": "2001-09-26", "status": "Ended",
		"genres": []string{"Science-Fiction"}, "rating": obj{"average": 8.2}, "network": obj{"name": "UPN"},
		"webChannel": nil, "summary": "<p><b>Enterprise</b> &amp; its crew.</p>",
		"externals": obj{"thetvdb": 73893, "imdb": "tt0244365"}, "image": obj{"original": img + "/tvm-poster.jpg"},
		"_embedded": obj{
			"cast":    []obj{{"person": obj{"name": "Scott Bakula", "image": nil}, "character": obj{"name": "Archer"}}},
			"seasons": []obj{{"number": 1, "image": obj{"original": img + "/tvm-s1.jpg"}}, {"number": 2, "image": nil}},
		}}
	mux.HandleFunc("/tvmaze/search/shows", func(w http.ResponseWriter, r *http.Request) {
		rs := []obj{}
		if strings.Contains(strings.ToLower(r.URL.Query().Get("q")), "enterprise") {
			rs = append(rs, obj{"score": 1.4, "show": tvmShow})
		}
		reply(w, rs)
	})
	mux.HandleFunc("/tvmaze/lookup/shows", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("imdb") != "tt0244365" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.Redirect(w, r, "/tvmaze/shows/714", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/tvmaze/shows/714", func(w http.ResponseWriter, r *http.Request) { reply(w, tvmShow) })
	mux.HandleFunc("/tvmaze/shows/714/images", func(w http.ResponseWriter, r *http.Request) {
		reply(w, []obj{
			{"type": "poster", "resolutions": obj{"original": obj{"url": img + "/x.jpg"}}},
			{"type": "background", "resolutions": obj{"original": obj{"url": img + "/tvm-bg.jpg"}}},
		})
	})
	mux.HandleFunc("/tvmaze/shows/714/episodes", func(w http.ResponseWriter, r *http.Request) {
		reply(w, []obj{
			{"name": "Broken Bow", "season": 1, "number": 1, "airdate": "2001-09-26", "runtime": 60,
				"rating": obj{"average": 7.3}, "image": obj{"original": img + "/tvm-e1.jpg"}, "summary": "<p>Pilot.</p>"},
			{"name": "Broken Bow", "season": 1, "number": 2, "airdate": "2001-09-26", "image": nil, "summary": nil},
			{"name": "Shockwave (2)", "season": 2, "number": 1, "rating": obj{"average": nil}},
		})
	})

	// --- OMDb: answers only to the right key
	mux.HandleFunc("/omdb/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("apikey") != "omdbkey":
			w.WriteHeader(http.StatusUnauthorized)
			reply(w, obj{"Response": "False", "Error": "Invalid API key!"})
		case q.Get("s") == "In the Line of Fire":
			reply(w, obj{"Response": "True", "Search": []obj{
				{"Title": "In the Line of Fire", "Year": "1993", "imdbID": "tt0107206", "Type": "movie"}}})
		case q.Get("i") == "tt0107206":
			reply(w, obj{"Response": "True", "Title": "In the Line of Fire", "Year": "1993", "Rated": "R",
				"Released": "09 Jul 1993", "Runtime": "128 min", "Genre": "Action, Crime", "Director": "Wolfgang Petersen",
				"Writer": "Jeff Maguire", "Actors": "Clint Eastwood, John Malkovich", "Plot": "An agent.", "Country": "United States",
				"Poster": img + "/omdb._V1_SX300.jpg", "imdbRating": "7.2", "imdbID": "tt0107206", "Type": "movie", "Production": "N/A"})
		default:
			reply(w, obj{"Response": "False", "Error": "Movie not found!"})
		}
	})

	// --- IMDb suggestions
	mux.HandleFunc("/imdb/", func(w http.ResponseWriter, r *http.Request) {
		items := []obj{}
		switch q := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/imdb/"), ".json"); q {
		case "iron man", "tt0371746":
			items = []obj{
				{"id": "tt0371746", "l": "Iron Man", "qid": "movie", "y": 2008, "s": "Robert Downey Jr., Gwyneth Paltrow",
					"i": obj{"imageUrl": img + "/imdb-ironman.jpg"}},
				{"id": "tt1300854", "l": "Iron Man 3", "qid": "movie", "y": 2013},
				{"id": "nm0000375", "l": "Robert Downey Jr."},
			}
		case "на линии огня", "in the line of fire":
			items = []obj{{"id": "tt0107206", "l": "In the Line of Fire", "qid": "movie", "y": 1993}}
		case "star trek enterprise", "tt0244365":
			items = []obj{{"id": "tt0244365", "l": "Star Trek: Enterprise", "qid": "tvSeries", "y": 2001}}
		case "tt0144039":
			items = []obj{{"id": "tt0144039", "l": "Atomic Train", "qid": "tvMiniSeries", "y": 1999}}
		case "tt32338669":
			items = []obj{{"id": "tt32338669", "l": "Mutiny", "qid": "movie", "y": 2026}}
		case "tt2026000":
			items = []obj{{"id": "tt2026000", "l": "Fast New", "qid": "movie", "y": 2026}}
		case "tt0000404":
			items = []obj{{"id": "tt0000404", "l": "Obscure Film", "qid": "movie", "y": 1950, "s": "Someone",
				"i": obj{"imageUrl": img + "/obscure.jpg"}}}
		}
		reply(w, obj{"d": items})
	})

	// --- Wikidata (SPARQL): knows Russian titles and IMDb numbers
	mux.HandleFunc("/wd/sparql", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		val := func(s string) obj { return obj{"value": s} }
		rows := []obj{}
		switch {
		case strings.Contains(q, `wdt:P345 "tt0371746"`):
			rows = []obj{{"ru": val("Железный человек")}}
		case strings.Contains(q, `wdt:P345 "tt0244365"`):
			rows = []obj{{"ru": val("Звёздный путь: Энтерпрайз")}}
		case strings.Contains(q, `wdt:P345 "tt`):
			wikidataTitleLookups++
		case strings.Contains(q, `"Атомный Поезд"`):
			rows = []obj{
				{"imdb": val("nm0000001"), "ru": val("Кто-то")},
				{"imdb": val("tt9990001"), "ru": val("Атомный поезд"), "classes": val("Q7889")},
				{"imdb": val("tt0144039"), "ru": val("Атомный поезд"), "en": val("Atomic Train"), "year": val("1999"), "classes": val("Q1259759,Q506240")},
			}
		case strings.Contains(q, `"мятеж"`):
			rows = []obj{{"imdb": val("tt32338669"), "ru": val("Мятеж"), "en": val("Mutiny"), "year": val("2026"), "classes": val("Q11424")}}
		case strings.Contains(q, `"Форсаж"`):
			rows = []obj{
				{"imdb": val("tt0232500"), "ru": val("Форсаж"), "en": val("The Fast and the Furious"), "year": val("2001"), "classes": val("Q11424")},
				{"imdb": val("tt2026000"), "ru": val("Форсаж. Новый"), "en": val("Fast New"), "year": val("2026"), "classes": val("Q11424")},
			}
		}
		reply(w, obj{"results": obj{"bindings": rows}})
	})

	// --- Letterboxd
	mux.HandleFunc("/lb/film/iron-man-2008/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.ReplaceAll(lbIronMan, "IMG", img))
	})
	mux.HandleFunc("/lb/imdb/tt0371746/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/lb/film/iron-man-2008/", http.StatusFound)
	})

	// --- Kinopoisk
	mux.HandleFunc("/kp/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "kpkey" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/kp/api/v2.1/films/search-by-keyword" && strings.Contains(r.URL.Query().Get("keyword"), "линии огня"):
			reply(w, obj{"films": []obj{
				{"filmId": 6461, "nameRu": "На линии огня", "nameEn": "In the Line of Fire", "type": "FILM", "year": "1993", "filmLength": "2:08"},
				{"filmId": 9999, "nameRu": "На линии огня", "type": "FILM", "year": "2003"},
				{"filmId": 5555, "nameRu": "На линии огня", "type": "TV_SERIES", "year": "2010"},
			}})
		case r.URL.Path == "/kp/api/v2.2/films/6461":
			reply(w, obj{"kinopoiskId": 6461, "imdbId": "tt0107206", "nameRu": "На линии огня", "nameEn": nil,
				"nameOriginal": "In the Line of Fire", "posterUrl": img + "/kp-poster.jpg", "coverUrl": nil,
				"ratingKinopoisk": 7.5, "year": 1993, "filmLength": 128, "slogan": "Слоган", "description": "Агент секретной службы.",
				"type": "FILM", "ratingMpaa": "r", "countries": []obj{{"country": "США"}}, "genres": []obj{{"genre": "триллер"}}})
		case r.URL.Path == "/kp/api/v1/staff":
			reply(w, []obj{
				{"nameRu": "Вольфганг Петерсен", "professionKey": "DIRECTOR"},
				{"nameRu": "Клинт Иствуд", "description": "Frank Horrigan", "professionKey": "ACTOR"},
			})
		case strings.HasSuffix(r.URL.Path, "/search-by-keyword"):
			reply(w, obj{"films": []obj{}})
		case r.URL.Path == "/kp/api/v2.2/films":
			reply(w, obj{"items": []obj{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "JPEG"+r.URL.Path) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/tmdb/search/") {
			reply(w, obj{"results": []obj{}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	old := []string{tvmazeURL, omdbURL, imdbURL, letterboxdURL, kinopoiskURL, wikidataSPARQL}
	tvmazeURL, omdbURL, imdbURL = srv.URL+"/tvmaze", srv.URL+"/omdb/", srv.URL+"/imdb/"
	letterboxdURL, kinopoiskURL, wikidataSPARQL = srv.URL+"/lb", srv.URL+"/kp", srv.URL+"/wd/sparql"
	t.Cleanup(func() {
		tvmazeURL, omdbURL, imdbURL, letterboxdURL, kinopoiskURL, wikidataSPARQL = old[0], old[1], old[2], old[3], old[4], old[5]
	})
	return srv
}

// setup writes a config with the given keys and creates a library of empty
// files with the given names.
func setup(t *testing.T, cfg Config, names ...string) string {
	srv := fakeServices(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, env := range []string{"TMDB_API_KEY", "KINOPOISK_API_KEY", "OMDB_API_KEY"} {
		t.Setenv(env, "")
	}
	t.Setenv("PATH", "") // no mkvpropedit/ffmpeg: tagging is tested separately
	cfg.TMDBURL, cfg.TMDBImageURL = srv.URL+"/tmdb", srv.URL+"/img"
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

var exampleFiles = []string{
	"Iron Man (2008) IMAX.mkv",
	"Iron Man (2008) IMAX.ru.srt",
	"Myatezh.2025.AMZN.WEB-DLRip.AVC.mkv",
	"На линии огня.mkv",
	"Атомный Поезд (Екатеринбург Арт).avi",
	"Startrek/Star.Trek.Enterprise.s1e01-02.Broken.Bow.mkv",
	"Startrek/Star.Trek.Enterprise.s2e01.Shockwave,Pt.2.mkv",
}

// tree lists files and empty directories, without the undo journal.
func tree(t *testing.T, root string) []string {
	var out []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == journalDir {
				return filepath.SkipDir
			}
			if entries, _ := os.ReadDir(path); len(entries) == 0 {
				out = append(out, rel+"/")
			}
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

func mustRun(t *testing.T, input string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(args, strings.NewReader(input), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return out.String()
}

func wantAll(t *testing.T, what, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Errorf("%s lacks %q\n%s", what, p, text)
		}
	}
}

func TestRunRerunUndo(t *testing.T) {
	root := setup(t, Config{TMDBKey: "tmdbkey", Language: "ru-RU"}, exampleFiles...)
	before := tree(t, root)

	// Answers, in the order the dialogs appear (files are sorted by name):
	//   Myatezh        -> IMDb number
	//   Атомный Поезд  -> skip
	//   На линии огня  -> first candidate
	//   "Применить?"   -> yes
	out := mustRun(t, "tt7777777\ns\n1\ny\n", root)
	wantAll(t, "output", out,
		"Sources: TMDB, TVMaze, Wikidata, IMDb, Letterboxd",
		"Not used, no key: OMDb, Kinopoisk",
		"Iron Man (2008) IMAX.mkv → Железный человек (2008) [TMDB]",
		"Myatezh.2025.AMZN.WEB-DLRip.AVC.mkv → Мятеж (2025) [TMDB]",
		"Звёздный путь - Энтерпрайз (2001) [TMDB]",
		"Атомный Поезд (Екатеринбург Арт).avi: skipped",
		"mediakeeper -undo")

	ent := "Shows/Звёздный путь - Энтерпрайз (2001)/"
	double := ent + "Season 01/Звёздный путь - Энтерпрайз S01E01-E02 - Разорванный круг (1) + Разорванный круг (2)"
	after := tree(t, root)
	want := []string{
		"Movies/Железный человек (2008)/backdrop.jpg",
		"Movies/Железный человек (2008)/poster.jpg",
		"Movies/Железный человек (2008)/Железный человек (2008).mkv",
		"Movies/Железный человек (2008)/Железный человек (2008).nfo",
		"Movies/Железный человек (2008)/Железный человек (2008).ru.srt",
		double + "-thumb.jpg", double + ".mkv", double + ".nfo",
		ent + "Season 02/Звёздный путь - Энтерпрайз S02E01 - Ударная волна - Часть 2.mkv",
		ent + "Season 02/Звёздный путь - Энтерпрайз S02E01 - Ударная волна - Часть 2.nfo",
		ent + "poster.jpg", ent + "season01-poster.jpg", ent + "tvshow.nfo",
		"Movies/Мятеж (2025)/poster.jpg",
		"Movies/Мятеж (2025)/Мятеж (2025).mkv",
		"Movies/Мятеж (2025)/Мятеж (2025).nfo",
		"Movies/На линии огня (1993)/На линии огня (1993).mkv",
		"Movies/На линии огня (1993)/На линии огня (1993).nfo",
		"Атомный Поезд (Екатеринбург Арт).avi",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("tree:\n got:\n  %s\nwant:\n  %s", strings.Join(after, "\n  "), strings.Join(want, "\n  "))
	}

	data, _ := os.ReadFile(filepath.Join(root, "Movies", "Железный человек (2008)", "Железный человек (2008).mkv"))
	if string(data) != "Iron Man (2008) IMAX.mkv" {
		t.Errorf("movie content changed: %q", data)
	}
	nfo, _ := os.ReadFile(filepath.Join(root, "Movies", "Железный человек (2008)", "Железный человек (2008).nfo"))
	wantAll(t, "movie nfo", string(nfo),
		`<!-- mediakeeper source="tmdb" id="1726" kind="movie" -->`,
		"<title>Железный человек</title>", "<originaltitle>Iron Man</originaltitle>", "<year>2008</year>",
		`<uniqueid type="tmdb" default="true">1726</uniqueid>`, `<uniqueid type="imdb">tt0371746</uniqueid>`,
		"<plot>Миллиардер &lt;Тони&gt; &amp; броня.</plot>", "<director>Джон Фавро</director>",
		"<rating>7.6</rating>", "<genre>боевик</genre>", "<role>Tony Stark</role>")
	multi, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(double+".nfo")))
	if n := strings.Count(string(multi), "<episodedetails>"); n != 2 {
		t.Errorf("double episode nfo has %d <episodedetails>, want 2\n%s", n, multi)
	}

	// A second run recognizes everything by .nfo and changes nothing.
	out = mustRun(t, "", "-yes", root)
	wantAll(t, "second run", out, "Renames: 0")
	if again := tree(t, root); !reflect.DeepEqual(again, after) {
		t.Errorf("second run changed the tree:\n  %s", strings.Join(again, "\n  "))
	}
	if n := len(journals(root)); n != 1 {
		t.Errorf("a run without changes must not leave a journal; journals: %d", n)
	}

	// Undo brings back the original names, folders included.
	out = mustRun(t, "", "-undo", root)
	wantAll(t, "undo", out, "Files moved back: 6")
	if restored := tree(t, root); !reflect.DeepEqual(restored, before) {
		t.Errorf("undo did not restore the tree:\n got:\n  %s\nwant:\n  %s",
			strings.Join(restored, "\n  "), strings.Join(before, "\n  "))
	}
	if _, err := os.Stat(filepath.Join(root, journalDir)); err == nil {
		t.Errorf("journal directory left after undo")
	}
	wantAll(t, "second undo", mustRun(t, "", "-undo", root), "nothing to undo")
}

func TestUndoRestoresOverwrittenNFO(t *testing.T) {
	root := setup(t, Config{TMDBKey: "tmdbkey"}, "Железный человек (2008)/Железный человек (2008).mkv")
	nfo := filepath.Join(root, "Железный человек (2008)", "Железный человек (2008).nfo")
	os.WriteFile(nfo, []byte("my own nfo"), 0o644)
	os.WriteFile(filepath.Join(root, "Железный человек (2008)", "extras.txt"), []byte("keep me"), 0o644)
	before := tree(t, root)

	// The movie's folder moves to Movies/ as a whole, extras included.
	mustRun(t, "", "-yes", root)
	moved := filepath.Join(root, "Movies", "Железный человек (2008)")
	if data, _ := os.ReadFile(filepath.Join(moved, "Железный человек (2008).nfo")); !strings.Contains(string(data), "<movie>") {
		t.Fatalf("nfo was not regenerated: %s\n  %s", data, strings.Join(tree(t, root), "\n  "))
	}
	if data, _ := os.ReadFile(filepath.Join(moved, "extras.txt")); string(data) != "keep me" {
		t.Errorf("other files of the folder did not move with it:\n  %s", strings.Join(tree(t, root), "\n  "))
	}
	if _, err := os.Stat(filepath.Dir(nfo)); err == nil {
		t.Errorf("the old folder is still there")
	}
	mustRun(t, "", "-undo", root)
	if data, _ := os.ReadFile(nfo); string(data) != "my own nfo" {
		t.Errorf("undo did not restore the previous nfo: %q", data)
	}
	if after := tree(t, root); !reflect.DeepEqual(after, before) {
		t.Errorf("tree after undo:\n  %s", strings.Join(after, "\n  "))
	}
}

// The dialog shows candidates from every source that answered, and a title
// typed by the user is searched everywhere.
func TestSearchAcrossSources(t *testing.T) {
	root := setup(t, Config{TMDBKey: "tmdbkey", KinopoiskKey: "kpkey", OMDbKey: "wrong"}, "На линии огня.mkv")
	// First window: the guessed title. Then a new title is typed, then the
	// Kinopoisk candidate of the first window's numbering is no longer valid,
	// so search the Russian title again and take Kinopoisk's first entry.
	out := mustRun(t, "In the Line of Fire\nНа линии огня\n3\ny\n", root)
	t.Log(out)
	wantAll(t, "output", out,
		"source OMDb is switched off for this run: API key rejected",
		"│ TMDB:", "│ Kinopoisk:", "│ IMDb:",
		"1) На линии огня (1993) — In the Line of Fire",
		"На линии огня.mkv → На линии огня (1993) [Kinopoisk]")
	if strings.Contains(out, "│ OMDb:") {
		t.Errorf("a source without a valid key must not be listed")
	}
	if strings.Contains(out, "│ TVMaze:") {
		t.Errorf("series must not be offered for a movie search")
	}
	nfo, _ := os.ReadFile(filepath.Join(root, "Movies", "На линии огня (1993)", "На линии огня (1993).nfo"))
	wantAll(t, "nfo", string(nfo),
		`<!-- mediakeeper source="kinopoisk" id="6461" kind="movie" -->`,
		`<uniqueid type="imdb" default="true">tt0107206</uniqueid>`, `<uniqueid type="kinopoisk">6461</uniqueid>`,
		"<originaltitle>In the Line of Fire</originaltitle>", "<runtime>128</runtime>", "<mpaa>R</mpaa>",
		"<director>Вольфганг Петерсен</director>", "<name>Клинт Иствуд</name>", "<country>США</country>")
	if data, _ := os.ReadFile(filepath.Join(root, "Movies", "На линии огня (1993)", "poster.jpg")); string(data) != "JPEG/img/kp-poster.jpg" {
		t.Errorf("poster: %q", data)
	}
}

func TestOMDbSource(t *testing.T) {
	root := setup(t, Config{OMDbKey: "omdbkey", Sources: []string{"omdb"}}, "In.the.Line.of.Fire.1993.BDRip.mkv")
	out := mustRun(t, "", "-yes", root)
	wantAll(t, "output", out, "→ In the Line of Fire (1993) [OMDb]")
	nfo, _ := os.ReadFile(filepath.Join(root, "Movies", "In the Line of Fire (1993)", "In the Line of Fire (1993).nfo"))
	wantAll(t, "nfo", string(nfo), "<premiered>1993-07-09</premiered>", "<runtime>128</runtime>",
		"<genre>Crime</genre>", "<name>John Malkovich</name>", "<rating>7.2</rating>", "omdb._V1_.jpg")
	if strings.Contains(string(nfo), "N/A") {
		t.Errorf("N/A leaked into nfo:\n%s", nfo)
	}
}

// Without any key: IMDb finds the title, Letterboxd and TVMaze give details.
func TestKeylessSources(t *testing.T) {
	root := setup(t, Config{}, "Iron Man (2008) IMAX.mkv", "Startrek/Star.Trek.Enterprise.s1e01-02.Broken.Bow.mkv",
		"Startrek/Star.Trek.Enterprise.s2e01.Shockwave,Pt.2.mkv", "Unknown.Thing.mkv")
	// The unknown file gets an IMDb number that only IMDb itself knows.
	out := mustRun(t, "tt0000404\ny\n", root)
	wantAll(t, "output", out,
		"Sources: TVMaze, Wikidata, IMDb, Letterboxd",
		"Iron Man (2008) IMAX.mkv → Iron Man (2008) [Letterboxd]",
		"Star Trek - Enterprise (2001) [TVMaze]",
		"Unknown.Thing.mkv → Obscure Film (1950) [IMDb]")

	nfo, _ := os.ReadFile(filepath.Join(root, "Movies", "Iron Man (2008)", "Iron Man (2008).nfo"))
	wantAll(t, "letterboxd nfo", string(nfo),
		`<!-- mediakeeper source="letterboxd" id="iron-man-2008" kind="movie" -->`,
		`<uniqueid type="tmdb" default="true">1726</uniqueid>`, `<uniqueid type="imdb">tt0371746</uniqueid>`,
		"<tagline>Heroes aren&#39;t born. They&#39;re built.</tagline>", "<runtime>126</runtime>", "<rating>7.6</rating>",
		"<premiered>2008-04-30</premiered>", "<director>Jon Favreau</director>", "<name>Jeff Bridges</name>",
		"<studio>Marvel Studios</studio>", "<plot>Tony Stark builds a suit.</plot>")

	ent := filepath.Join(root, "Shows", "Star Trek - Enterprise (2001)")
	got := tree(t, ent)
	want := []string{
		"Season 01/Star Trek - Enterprise S01E01-E02 - Broken Bow-thumb.jpg",
		"Season 01/Star Trek - Enterprise S01E01-E02 - Broken Bow.mkv",
		"Season 01/Star Trek - Enterprise S01E01-E02 - Broken Bow.nfo",
		"Season 02/Star Trek - Enterprise S02E01 - Shockwave (2).mkv",
		"Season 02/Star Trek - Enterprise S02E01 - Shockwave (2).nfo",
		"backdrop.jpg", "poster.jpg", "season01-poster.jpg", "tvshow.nfo",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("series tree:\n  %s", strings.Join(got, "\n  "))
	}
	show, _ := os.ReadFile(filepath.Join(ent, "tvshow.nfo"))
	wantAll(t, "tvshow.nfo", string(show), `source="tvmaze" id="714"`, "<plot>Enterprise &amp; its crew.</plot>",
		`<uniqueid type="tvdb">73893</uniqueid>`, "<studio>UPN</studio>", "<role>Archer</role>")
	if data, _ := os.ReadFile(filepath.Join(ent, "backdrop.jpg")); string(data) != "JPEG/img/tvm-bg.jpg" {
		t.Errorf("backdrop: %q", data)
	}

	// The Russian titles are looked up by IMDb number and kept in the .nfo;
	// "not found" is remembered too, so the second run asks nothing again.
	wantAll(t, "movie nfo", string(nfo), `<localizedtitle lang="ru">Железный человек</localizedtitle>`)
	wantAll(t, "tvshow.nfo", string(show), `<localizedtitle lang="ru">Звёздный путь: Энтерпрайз</localizedtitle>`)
	obscure, _ := os.ReadFile(filepath.Join(root, "Movies", "Obscure Film (1950)", "Obscure Film (1950).nfo"))
	wantAll(t, "nfo without a Russian title", string(obscure), `<localizedtitle lang="ru"></localizedtitle>`)
	lookups := wikidataTitleLookups

	// The second run finds the sources by the marker in .nfo.
	wantAll(t, "second run", mustRun(t, "", "-yes", root), "Renames: 0")
	if wikidataTitleLookups != lookups {
		t.Errorf("a title known to be missing was looked up again")
	}
	again, _ := os.ReadFile(filepath.Join(root, "Movies", "Iron Man (2008)", "Iron Man (2008).nfo"))
	if string(again) != string(nfo) {
		t.Errorf("the second run changed the nfo:\n%s", again)
	}

	// The DLNA server shows both names.
	dlna, err := NewDLNAServer(root, "test", 8200, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	var shown []string
	for _, id := range []string{dlnaMoviesID, dlnaSeriesID} {
		for _, n := range dlna.lib.nodes[id].Children {
			shown = append(shown, n.Title)
		}
	}
	if got := strings.Join(shown, " | "); got != "Iron Man / Железный человек (2008) | Obscure Film (1950) | Star Trek: Enterprise / Звёздный путь: Энтерпрайз" {
		t.Errorf("DLNA titles: %s", got)
	}
}

func TestQueryVariants(t *testing.T) {
	got := queryVariants("Форсаж. На пределе скорости")
	want := []string{"Форсаж. На пределе скорости", "Форсаж На пределе скорости", "Форсаж", "На пределе скорости"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
	if got := queryVariants("Iron Man"); len(got) != 1 {
		t.Errorf("a plain title has no variants, got %q", got)
	}
}

func TestToCyrillic(t *testing.T) {
	tests := map[string]string{
		"Myatezh": "мятеж", "Brat 2": "брат 2", "Chuzhoy": "чужой", "Shchit": "щит",
		"Мятеж": "", "2012": "",
	}
	for in, want := range tests {
		if got := toCyrillic(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// What the file name alone could not find: a miniseries kept as one file, a
// transliterated name, a localized subtitle, and a year typed into a query.
func TestSmarterSearch(t *testing.T) {
	root := setup(t, Config{}, "Атомный Поезд (Екатеринбург Арт).avi", "Myatezh.2025.AMZN.WEB-DLRip.AVC.mkv",
		"Форсаж. На пределе скорости_2026_WEB-DLRip.avi")
	// Files in name order: Myatezh, Атомный Поезд, Форсаж.
	//   Myatezh  -> 1: the Cyrillic spelling finds it in Wikidata
	//   Атомный  -> 1: a series; Enter keeps the single file as a movie
	//   Форсаж   -> type "Форсаж 2026": the entry of 2026 moves to the top -> 1
	out := mustRun(t, "1\n1\n\nФорсаж 2026\n1\ny\n", root)
	t.Log(out)
	wantAll(t, "output", out,
		"1) Мятеж (2026) — Mutiny",
		"1) Атомный поезд (1999) — Atomic Train [series]",
		"\"Atomic Train\" is a series",
		"2) Форсаж (2001) — The Fast and the Furious", // found by the first part of the title
		"1) Форсаж. Новый (2026) — Fast New",          // the year of the file name goes first
		"Myatezh.2025.AMZN.WEB-DLRip.AVC.mkv → Mutiny (2026) [IMDb]",
		"Атомный Поезд (Екатеринбург Арт).avi → Atomic Train (1999) [IMDb]",
		"→ Fast New (2026) [IMDb]")
	for _, banned := range []string{"Кто-то", "tt9990001"} {
		if strings.Contains(out, banned) {
			t.Errorf("people and games must not be listed: %s", banned)
		}
	}
	got := tree(t, root)
	want := []string{
		"Movies/Atomic Train (1999)/Atomic Train (1999).avi", "Movies/Atomic Train (1999)/Atomic Train (1999).nfo",
		"Movies/Fast New (2026)/Fast New (2026).avi", "Movies/Fast New (2026)/Fast New (2026).nfo",
		"Movies/Mutiny (2026)/Mutiny (2026).mkv", "Movies/Mutiny (2026)/Mutiny (2026).nfo",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tree:\n  %s", strings.Join(got, "\n  "))
	}
	nfo, _ := os.ReadFile(filepath.Join(root, "Movies", "Atomic Train (1999)", "Atomic Train (1999).nfo"))
	wantAll(t, "nfo", string(nfo), `source="imdb" id="tt0144039" kind="tv"`, "<movie>")

	// The series stored as a movie is recognized again without questions.
	wantAll(t, "second run", mustRun(t, "", "-yes", root), "Renames: 0", "Atomic Train (1999) [IMDb]")
}

// Scanning a parent directory must not pull titles out of their folders.
func TestTitlesStayInTheirFolders(t *testing.T) {
	root := setup(t, Config{TMDBKey: "tmdbkey", Language: "ru-RU"},
		"media/Iron Man (2008) IMAX.mkv",
		"media/Startrek/Star.Trek.Enterprise.s1e01-02.Broken.Bow.mkv",
		"media/Startrek/Season 2/Star.Trek.Enterprise.s2e01.Shockwave,Pt.2.mkv",
		"films/old/Iron.Man.2008.BDRip.mkv",
		"notes.txt")
	mustRun(t, "", "-yes", "-refresh", root)
	ent := "media/Shows/Звёздный путь - Энтерпрайз (2001)/"
	for _, want := range []string{
		"media/Movies/Железный человек (2008)/Железный человек (2008).mkv",
		ent + "tvshow.nfo",
		ent + "Season 01/Звёздный путь - Энтерпрайз S01E01-E02 - Разорванный круг (1) + Разорванный круг (2).mkv",
		ent + "Season 02/Звёздный путь - Энтерпрайз S02E01 - Ударная волна - Часть 2.mkv",
		// A folder with a single movie is that movie's folder: it is replaced, not nested.
		"films/Movies/Железный человек (2008)/Железный человек (2008).mkv",
		"notes.txt",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(want))); err != nil {
			t.Errorf("missing %s", want)
		}
	}
	got := tree(t, root)
	for _, path := range got {
		if !strings.HasPrefix(path, "media/") && !strings.HasPrefix(path, "films/") && path != "notes.txt" {
			t.Errorf("moved out of its folder: %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "media", "Startrek")); err == nil {
		t.Errorf("the emptied series folder was not removed")
	}
	// Running on any of the folders afterwards finds everything in place:
	// Movies/ and Shows/ are not nested into themselves.
	for _, dir := range []string{"media/Movies", "media/Shows", "media", ""} {
		wantAll(t, "rerun on "+dir, mustRun(t, "", "-yes", filepath.Join(root, filepath.FromSlash(dir))), "Renames: 0")
	}

	// A series filed under Movies/ goes to the Shows/ next to it.
	misfiled := filepath.Join(root, "media", "Movies", "Star.Trek.Enterprise.s2e01.mkv")
	os.Rename(filepath.Join(root, filepath.FromSlash(ent+"Season 02/Звёздный путь - Энтерпрайз S02E01 - Ударная волна - Часть 2.mkv")), misfiled)
	mustRun(t, "", "-yes", root)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ent+"Season 02/Звёздный путь - Энтерпрайз S02E01 - Ударная волна - Часть 2.mkv"))); err != nil {
		t.Errorf("misfiled episode did not return to Shows/:\n  %s", strings.Join(tree(t, root), "\n  "))
	}

	// -out gathers everything in one place, in the same two folders.
	lib := filepath.Join(t.TempDir(), "lib")
	mustRun(t, "", "-yes", "-out", lib, root)
	for _, want := range []string{"Shows/Звёздный путь - Энтерпрайз (2001)/tvshow.nfo", "Movies/Железный человек (2008)/Железный человек (2008).mkv"} {
		if _, err := os.Stat(filepath.Join(lib, filepath.FromSlash(want))); err != nil {
			t.Errorf("-out: %v\n  %s", err, strings.Join(tree(t, lib), "\n  "))
		}
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	root := setup(t, Config{TMDBKey: "tmdbkey"}, exampleFiles...)
	before := tree(t, root)
	out := mustRun(t, "", "-dry-run", "-yes", root)
	if after := tree(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("dry run changed files:\n  %s", strings.Join(after, "\n  "))
	}
	if len(journals(root)) != 0 {
		t.Errorf("dry run left a journal")
	}
	wantAll(t, "output", out, "→ Железный человек (2008)", "На линии огня.mkv: skipped", "nothing was changed")
}

func TestNeverOverwrites(t *testing.T) {
	root := setup(t, Config{TMDBKey: "tmdbkey"}, exampleFiles...)
	occupied := filepath.Join(root, "Movies", "Железный человек (2008)", "Железный человек (2008).mkv")
	os.MkdirAll(filepath.Dir(occupied), 0o755)
	os.WriteFile(occupied, []byte("other"), 0o644)
	mustRun(t, "", "-yes", root)
	if data, _ := os.ReadFile(occupied); string(data) != "other" {
		t.Errorf("existing file was overwritten")
	}
	if _, err := os.Stat(filepath.Join(root, "Iron Man (2008) IMAX.mkv")); err != nil {
		t.Errorf("source must stay in place when the target is occupied: %v", err)
	}
}

func TestUnknownSource(t *testing.T) {
	root := setup(t, Config{}, "a.mkv")
	var out bytes.Buffer
	if err := run([]string{"-sources", "tvmaze,netflix", root}, strings.NewReader(""), &out); err == nil {
		t.Errorf("an unknown source must be an error")
	}
}

// Real mkvpropedit and ffmpeg on tiny generated videos.
func TestWriteTagsWithRealTools(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe", "mkvpropedit"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	dir := t.TempDir()
	info := &TagInfo{Title: "Железный человек", Date: "2008-04-30", Description: "Описание: с \"кавычками\" = и т.д.",
		Genres: []string{"боевик", "фантастика"}, IMDb: "tt0371746", TMDB: "1726"}
	for _, ext := range []string{".mkv", ".avi", ".mp4"} {
		path := filepath.Join(dir, "video"+ext)
		gen := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=d=1:s=64x64:r=5",
			"-f", "lavfi", "-i", "sine=d=1", "-c:v", "mpeg4", "-c:a", "mp3", "-shortest", path)
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if err := WriteTags(path, info); err != nil {
			t.Errorf("%s: %v", ext, err)
			continue
		}
		probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
			"format_tags:stream=codec_type", "-of", "json", path).Output()
		if err != nil {
			t.Errorf("%s is unreadable after tagging: %v", ext, err)
			continue
		}
		wantAll(t, ext+" tags", string(probe), "Железный человек", "боевик", `"video"`, `"audio"`)
		if left, _ := filepath.Glob(filepath.Join(dir, ".mk-tmp-*")); len(left) > 0 {
			t.Errorf("temporary files left: %v", left)
		}
	}
}
