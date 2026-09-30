package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dlnaFixture is a library the way MediaKeeper leaves it, plus one file
// that was never organized.
func dlnaFixture(t *testing.T) (*DLNAServer, *httptest.Server, string) {
	root := dlnaLibraryFiles(t)
	t.Setenv("PATH", "") // no ffprobe: durations come from .nfo
	s, err := NewDLNAServer(root, "Test & Server", 8200, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv, root
}

// dlnaLibraryFiles creates the files of the fixture library.
func dlnaLibraryFiles(t *testing.T) string {
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Iron Man (2008)/Iron Man (2008).mkv", "0123456789")
	write("Iron Man (2008)/Iron Man (2008).nfo", nfoHeader+"<movie><title>Iron Man &amp; Co</title><year>2008</year>"+
		"<premiered>2008-04-30</premiered><plot>Tony &lt;Stark&gt;.</plot><runtime>126</runtime><genre>Action</genre></movie>")
	write("Iron Man (2008)/Iron Man (2008).en.srt", "1\n00:00:01,000 --> 00:00:02,000\nHi\n")
	write("Iron Man (2008)/poster.jpg", "POSTER")
	ent := "Star Trek - Enterprise (2001)/"
	write(ent+"tvshow.nfo", nfoHeader+"<tvshow><title>Star Trek: Enterprise</title></tvshow>")
	write(ent+"poster.jpg", "SHOWPOSTER")
	write(ent+"season01-poster.jpg", "S1POSTER")
	// Written out of order to check the sorting.
	write(ent+"Season 02/Star Trek - Enterprise S02E01 - Shockwave.mkv", "s2e1")
	write(ent+"Season 02/Star Trek - Enterprise S02E01 - Shockwave.nfo",
		nfoHeader+"<episodedetails><title>Shockwave</title><showtitle>Star Trek: Enterprise</showtitle><season>2</season><episode>1</episode></episodedetails>")
	write(ent+"Season 01/Star Trek - Enterprise S01E03 - Fight or Flight.mkv", "s1e3")
	write(ent+"Season 01/Star Trek - Enterprise S01E01-E02 - Broken Bow.mkv", "s1e1")
	write(ent+"Season 01/Star Trek - Enterprise S01E01-E02 - Broken Bow.nfo",
		nfoHeader+"<episodedetails><title>Broken Bow</title><season>1</season><episode>1</episode></episodedetails>\n"+
			"<episodedetails><title>Broken Bow</title><season>1</season><episode>2</episode></episodedetails>")
	write(ent+"Season 01/Star Trek - Enterprise S01E01-E02 - Broken Bow-thumb.jpg", "THUMB")
	write("Some.Unknown.Movie.2019.WEB-DL.avi", "loose")
	write("notes.txt", "not a video")
	return root
}

type didlEntry struct {
	ID         string `xml:"id,attr"`
	ParentID   string `xml:"parentID,attr"`
	ChildCount int    `xml:"childCount,attr"`
	Title      string `xml:"title"`
	Class      string `xml:"class"`
	Date       string `xml:"date"`
	Plot       string `xml:"description"`
	Art        string `xml:"albumArtURI"`
	Res        []struct {
		ProtocolInfo string `xml:"protocolInfo,attr"`
		Size         int64  `xml:"size,attr"`
		Duration     string `xml:"duration,attr"`
		URL          string `xml:",chardata"`
	} `xml:"res"`
}

type browseResult struct {
	Entries []didlEntry
	Total   int
}

func soapCall(t *testing.T, srv *httptest.Server, service, action string, args ...string) (int, string) {
	t.Helper()
	var body strings.Builder
	body.WriteString(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:` + action + ` xmlns:u="x">`)
	for i := 0; i < len(args); i += 2 {
		fmt.Fprintf(&body, "<%s>%s</%s>", args[i], xmlEsc(args[i+1]), args[i])
	}
	body.WriteString("</u:" + action + "></s:Body></s:Envelope>")
	resp, err := http.Post(srv.URL+"/ctl/"+service, "text/xml", strings.NewReader(body.String()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func browse(t *testing.T, srv *httptest.Server, action, id, flag string, extra ...string) browseResult {
	t.Helper()
	idArg := "ObjectID"
	if action == "Search" {
		idArg = "ContainerID"
	}
	args := append([]string{idArg, id, "BrowseFlag", flag, "Filter", "*"}, extra...)
	status, body := soapCall(t, srv, "cd", action, args...)
	if status != http.StatusOK {
		t.Fatalf("%s %s: HTTP %d\n%s", action, id, status, body)
	}
	var env struct {
		Result string `xml:"Body>BrowseResponse>Result"`
		Search string `xml:"Body>SearchResponse>Result"`
		Total  int    `xml:"Body>BrowseResponse>TotalMatches"`
		STotal int    `xml:"Body>SearchResponse>TotalMatches"`
	}
	if err := xml.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("response is not XML: %v\n%s", err, body)
	}
	var didl struct {
		Entries []didlEntry `xml:",any"`
	}
	if err := xml.Unmarshal([]byte(env.Result+env.Search), &didl); err != nil {
		t.Fatalf("Result is not DIDL-Lite: %v\n%s", err, env.Result)
	}
	return browseResult{didl.Entries, env.Total + env.STotal}
}

func titles(r browseResult) string {
	var out []string
	for _, e := range r.Entries {
		out = append(out, e.Title)
	}
	return strings.Join(out, " | ")
}

func TestDLNABrowse(t *testing.T) {
	_, srv, _ := dlnaFixture(t)

	if got := titles(browse(t, srv, "Browse", "0", "BrowseDirectChildren")); got != "Movies | Shows | Folders" {
		t.Errorf("root: %s", got)
	}
	movies := browse(t, srv, "Browse", dlnaMoviesID, "BrowseDirectChildren")
	if got := titles(movies); got != "Iron Man & Co (2008) | Some Unknown Movie (2019)" {
		t.Fatalf("movies: %s", got)
	}
	iron := movies.Entries[0]
	if iron.Class != "object.item.videoItem" || iron.Date != "2008-04-30" || iron.Plot != "Tony <Stark>." || iron.ParentID != dlnaMoviesID {
		t.Errorf("movie metadata: %+v", iron)
	}
	if len(iron.Res) != 2 || iron.Res[0].Size != 10 || iron.Res[0].Duration != "2:06:00.000" ||
		iron.Res[0].ProtocolInfo != "http-get:*:video/x-matroska:"+dlnaFeatures || !strings.HasPrefix(iron.Res[1].ProtocolInfo, "http-get:*:text/srt") {
		t.Errorf("movie resources: %+v", iron.Res)
	}
	if loose := movies.Entries[1]; loose.Art != "" || loose.Res[0].ProtocolInfo != "http-get:*:video/x-msvideo:"+dlnaFeatures {
		t.Errorf("loose movie: %+v", loose)
	}

	shows := browse(t, srv, "Browse", dlnaSeriesID, "BrowseDirectChildren")
	if titles(shows) != "Star Trek: Enterprise" || shows.Entries[0].ChildCount != 2 || shows.Entries[0].Art == "" {
		t.Fatalf("series: %+v", shows.Entries)
	}
	seasons := browse(t, srv, "Browse", shows.Entries[0].ID, "BrowseDirectChildren")
	if titles(seasons) != "Season 1 | Season 2" {
		t.Fatalf("seasons: %s", titles(seasons))
	}
	eps := browse(t, srv, "Browse", seasons.Entries[0].ID, "BrowseDirectChildren")
	if got := titles(eps); got != "01-02. Broken Bow | Episode 03" {
		t.Errorf("episodes: %s", got)
	}

	// Artwork: the episode still, then the season poster as a fallback.
	for i, want := range []string{"THUMB", "S1POSTER"} {
		resp, err := http.Get(eps.Entries[i].Art)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(data) != want {
			t.Errorf("art of %q: %q, want %q", eps.Entries[i].Title, data, want)
		}
	}

	// Paging and metadata of a single object.
	page := browse(t, srv, "Browse", "0", "BrowseDirectChildren", "StartingIndex", "1", "RequestedCount", "1")
	if titles(page) != "Shows" || page.Total != 3 {
		t.Errorf("page: %s, total %d", titles(page), page.Total)
	}
	meta := browse(t, srv, "Browse", iron.ID, "BrowseMetadata")
	if len(meta.Entries) != 1 || meta.Entries[0].Title != iron.Title {
		t.Errorf("metadata: %+v", meta.Entries)
	}
	if root := browse(t, srv, "Browse", "0", "BrowseMetadata"); root.Entries[0].ParentID != "-1" {
		t.Errorf("root parent: %+v", root.Entries[0])
	}

	// Folders mirror the disk; Search returns every video once.
	folders := browse(t, srv, "Browse", dlnaFoldersID, "BrowseDirectChildren")
	if got := titles(folders); got != "Iron Man (2008) | Some.Unknown.Movie.2019.WEB-DL.avi | Star Trek - Enterprise (2001)" {
		t.Errorf("folders: %s", got)
	}
	if all := browse(t, srv, "Search", "0", "", "SearchCriteria", `upnp:class derivedfrom "object.item.videoItem"`); all.Total != 5 {
		t.Errorf("search found %d videos, want 5: %s", all.Total, titles(all))
	}

	status, body := soapCall(t, srv, "cd", "Browse", "ObjectID", "no-such", "BrowseFlag", "BrowseDirectChildren")
	if status != http.StatusInternalServerError || !strings.Contains(body, "<errorCode>701</errorCode>") {
		t.Errorf("unknown object: HTTP %d\n%s", status, body)
	}
	if status, body = soapCall(t, srv, "cd", "Destroy"); !strings.Contains(body, "<errorCode>401</errorCode>") {
		t.Errorf("unknown action: HTTP %d\n%s", status, body)
	}
}

func TestDLNAStreaming(t *testing.T) {
	_, srv, _ := dlnaFixture(t)
	iron := browse(t, srv, "Browse", dlnaMoviesID, "BrowseDirectChildren").Entries[0]

	req, _ := http.NewRequest(http.MethodGet, iron.Res[0].URL, nil)
	req.Header.Set("Range", "bytes=2-5")
	req.Header.Set("getCaptionInfo.sec", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(data) != "2345" || resp.Header.Get("Content-Range") != "bytes 2-5/10" {
		t.Errorf("range: HTTP %d %q %q", resp.StatusCode, data, resp.Header.Get("Content-Range"))
	}
	if resp.Header.Get("Content-Type") != "video/x-matroska" || resp.Header.Get("contentFeatures.dlna.org") != dlnaFeatures ||
		resp.Header.Get("transferMode.dlna.org") != "Streaming" || !strings.Contains(resp.Header.Get("CaptionInfo.sec"), "/sub/") {
		t.Errorf("headers: %v", resp.Header)
	}

	head, err := http.Head(iron.Res[0].URL)
	if err != nil || head.StatusCode != http.StatusOK || head.ContentLength != 10 || head.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("HEAD: %v %+v", err, head)
	}
	sub, _ := http.Get(iron.Res[1].URL)
	text, _ := io.ReadAll(sub.Body)
	sub.Body.Close()
	if !strings.Contains(string(text), "-->") {
		t.Errorf("subtitles: %q", text)
	}

	// Only library files are reachable, and only by ID.
	for _, path := range []string{"/media/nope/x.mkv", "/media/" + dlnaMoviesID + "/x", "/media/..%2F..%2Fetc%2Fpasswd", "/art/" + dlnaRootID, "/notes.txt"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: HTTP %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestDLNADescription(t *testing.T) {
	s, srv, _ := dlnaFixture(t)
	for _, path := range []string{"/rootDesc.xml", "/scpd/cd.xml", "/scpd/cm.xml", "/scpd/mr.xml"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var any struct {
			Name   string   `xml:"device>friendlyName"`
			UDN    string   `xml:"device>UDN"`
			Action []string `xml:"actionList>action>name"`
		}
		if err := xml.Unmarshal(data, &any); err != nil {
			t.Fatalf("%s is not XML: %v", path, err)
		}
		switch path {
		case "/rootDesc.xml":
			if any.Name != "Test & Server" || any.UDN != "uuid:"+s.uuid || !strings.Contains(string(data), dlnaCDType) {
				t.Errorf("description: %s", data)
			}
		case "/scpd/cd.xml":
			if strings.Join(any.Action, ",") != "GetSearchCapabilities,GetSortCapabilities,GetSystemUpdateID,Browse,Search" {
				t.Errorf("actions: %v", any.Action)
			}
		}
	}

	req, _ := http.NewRequest("SUBSCRIBE", srv.URL+"/evt/cd", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("SID"), "uuid:") {
		t.Errorf("SUBSCRIBE: %v %+v", err, resp)
	}
	if _, body := soapCall(t, srv, "cm", "GetProtocolInfo"); !strings.Contains(body, "http-get:*:video/x-matroska:*") {
		t.Errorf("GetProtocolInfo: %s", body)
	}
}

// New files appear without a restart, and the update counter tells the
// players to refresh.
func TestDLNARescan(t *testing.T) {
	s, srv, root := dlnaFixture(t)
	_, before := soapCall(t, srv, "cd", "GetSystemUpdateID")
	os.WriteFile(filepath.Join(root, "Another Movie (2020).mp4"), []byte("new"), 0o644)
	s.mu.Lock()
	s.scanned = s.scanned.Add(-2 * dlnaRescanAfter) // as if time has passed
	s.mu.Unlock()
	if got := titles(browse(t, srv, "Browse", dlnaMoviesID, "BrowseDirectChildren")); !strings.HasPrefix(got, "Another Movie (2020) | ") {
		t.Errorf("movies after adding a file: %s", got)
	}
	if _, after := soapCall(t, srv, "cd", "GetSystemUpdateID"); after == before {
		t.Errorf("SystemUpdateID did not change: %s", after)
	}
}

func TestSSDPMessages(t *testing.T) {
	search := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: upnp:rootdevice\r\n\r\n"
	if st := ssdpSearchTarget([]byte(search)); st != "upnp:rootdevice" {
		t.Errorf("search target: %q", st)
	}
	for _, other := range []string{
		"NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nNT: upnp:rootdevice\r\nNTS: ssdp:alive\r\n\r\n",
		"M-SEARCH * HTTP/1.1\r\nST: upnp:rootdevice\r\n\r\n", // no MAN header
		"garbage",
	} {
		if st := ssdpSearchTarget([]byte(other)); st != "" {
			t.Errorf("%q must be ignored, got %q", other, st)
		}
	}

	s := newSSDP(&DLNAServer{uuid: "abc", port: 8200}, nil)
	targets := s.targets()
	if len(targets) != 5 || targets[0] != [2]string{"uuid:abc", "uuid:abc"} || targets[1][1] != "uuid:abc::upnp:rootdevice" {
		t.Errorf("targets: %v", targets)
	}
	reply := string(ssdpResponse("upnp:rootdevice", "uuid:abc::upnp:rootdevice", "http://10.0.0.2:8200/rootDesc.xml", "srv"))
	for _, want := range []string{"HTTP/1.1 200 OK\r\n", "\r\nST: upnp:rootdevice\r\n", "\r\nUSN: uuid:abc::upnp:rootdevice\r\n",
		"\r\nLOCATION: http://10.0.0.2:8200/rootDesc.xml\r\n", "\r\nEXT:\r\n", "max-age=1800"} {
		if !strings.Contains(reply, want) || !strings.HasSuffix(reply, "\r\n\r\n") {
			t.Errorf("response lacks %q:\n%s", want, reply)
		}
	}
	bye := string(ssdpNotify("ssdp:byebye", "upnp:rootdevice", "uuid:abc::upnp:rootdevice", "http://x/", "srv"))
	if !strings.HasPrefix(bye, "NOTIFY * HTTP/1.1\r\n") || !strings.Contains(bye, "NTS: ssdp:byebye\r\n") || strings.Contains(bye, "LOCATION") {
		t.Errorf("byebye:\n%s", bye)
	}
}
