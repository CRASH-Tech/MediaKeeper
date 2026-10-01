package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A subset of the Jellyfin server API, enough for native Jellyfin clients
// to sign in, browse the library and play files directly: the server never
// transcodes, it tells the clients so and hands out the files as they are.
//
// Jellyfin matches paths and query parameters without regard to case, and
// clients rely on it, so both are compared in lower case here.

const (
	jfVersion  = "10.10.7" // the API level the answers imitate
	jfDateTime = "2006-01-02T15:04:05.0000000Z"
)

func jfTicks(d time.Duration) int64 { return d.Nanoseconds() / 100 }

func jfDate(t time.Time) string { return t.UTC().Format(jfDateTime) }

var (
	jfMoviesView = catalogID("view", "movies")
	jfShowsView  = catalogID("view", "shows")
)

// jfQuery is the query string with lower-case keys.
type jfQuery map[string]string

func newJFQuery(r *http.Request) jfQuery {
	q := jfQuery{}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			q[strings.ToLower(k)] = strings.Join(v, ",")
		}
	}
	return q
}

func (q jfQuery) list(key string) []string {
	var out []string
	for _, v := range strings.FieldsFunc(q[key], func(r rune) bool { return r == ',' || r == '|' }) {
		out = append(out, strings.ToLower(strings.TrimSpace(v)))
	}
	return out
}

func (q jfQuery) has(key, value string) bool {
	for _, v := range q.list(key) {
		if v == value {
			return true
		}
	}
	return false
}

func (q jfQuery) bool(key string) bool { return strings.EqualFold(q[key], "true") }

func (q jfQuery) int(key string) int { n, _ := strconv.Atoi(q[key]); return n }

// jfID strips the dashes clients sometimes put into identifiers.
func jfID(s string) string { return strings.ToLower(strings.ReplaceAll(s, "-", "")) }

// jfEntry is anything the API lists: a video, a series, a season or one of
// the two top-level folders.
type jfEntry struct {
	item   *CatItem
	show   *CatShow
	season *CatSeason
	view   string // jfMoviesView or jfShowsView
}

func (e jfEntry) typ() string {
	switch {
	case e.item != nil && e.item.Kind == kindMovie:
		return "Movie"
	case e.item != nil:
		return "Episode"
	case e.show != nil:
		return "Series"
	case e.season != nil:
		return "Season"
	}
	return "CollectionFolder"
}

func (e jfEntry) id() string {
	switch {
	case e.item != nil:
		return e.item.ID
	case e.show != nil:
		return e.show.ID
	case e.season != nil:
		return e.season.ID
	}
	return e.view
}

func (e jfEntry) name() string {
	switch {
	case e.item != nil:
		return e.item.Title
	case e.show != nil:
		return e.show.Title
	case e.season != nil:
		if e.season.Number == 0 {
			return "Specials"
		}
		return fmt.Sprintf("Season %d", e.season.Number)
	case e.view == jfMoviesView:
		return moviesFolder
	}
	return showsFolder
}

func (e jfEntry) added() time.Time {
	switch {
	case e.item != nil:
		return e.item.ModTime
	case e.show != nil:
		return e.show.ModTime
	case e.season != nil:
		return e.season.Show.ModTime
	}
	return time.Time{}
}

func (e jfEntry) premiere() string {
	switch {
	case e.item != nil:
		return e.item.Date
	case e.show != nil:
		return e.show.Date
	}
	return ""
}

func (e jfEntry) year() int {
	switch {
	case e.item != nil:
		return e.item.Year
	case e.show != nil:
		return e.show.Year
	}
	return 0
}

// sortKey orders episodes within a series and everything else by name.
func (e jfEntry) sortKey() string {
	switch {
	case e.item != nil && e.item.Kind == kindEpisode:
		return fmt.Sprintf("%s %04d %04d", strings.ToLower(e.item.Show.Title), e.item.Season.Number, e.item.Episode)
	case e.season != nil:
		return fmt.Sprintf("%s %04d", strings.ToLower(e.season.Show.Title), e.season.Number)
	}
	return strings.ToLower(e.name())
}

func (e jfEntry) matches(term string) bool {
	texts := []string{e.name()}
	switch {
	case e.item != nil:
		texts = append(texts, e.item.OriginalTitle, e.item.LocalTitle)
	case e.show != nil:
		texts = append(texts, e.show.OriginalTitle, e.show.LocalTitle)
	}
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), term) {
			return true
		}
	}
	return false
}

// jfLookup finds an entry by identifier.
func jfLookup(cat *Catalog, id string) (jfEntry, bool) {
	switch id = jfID(id); {
	case cat.items[id] != nil:
		return jfEntry{item: cat.items[id]}, true
	case cat.shows[id] != nil:
		return jfEntry{show: cat.shows[id]}, true
	case cat.seasons[id] != nil:
		return jfEntry{season: cat.seasons[id]}, true
	case id == jfMoviesView || id == jfShowsView:
		return jfEntry{view: id}, true
	}
	return jfEntry{}, false
}

// jfChildren lists what is inside an entry: directly, or all the way down.
// Without a parent it is the top of the library.
func jfChildren(cat *Catalog, parent *jfEntry, recursive bool) []jfEntry {
	var out []jfEntry
	movies := func() {
		for _, m := range cat.Movies {
			out = append(out, jfEntry{item: m})
		}
	}
	season := func(s *CatSeason) {
		for _, ep := range s.Episodes {
			out = append(out, jfEntry{item: ep})
		}
	}
	show := func(s *CatShow) {
		for _, sn := range s.Seasons {
			out = append(out, jfEntry{season: sn})
			if recursive {
				season(sn)
			}
		}
	}
	shows := func() {
		for _, s := range cat.Shows {
			out = append(out, jfEntry{show: s})
			if recursive {
				show(s)
			}
		}
	}
	switch {
	case parent == nil && recursive:
		movies()
		shows()
	case parent == nil:
		out = []jfEntry{{view: jfMoviesView}, {view: jfShowsView}}
	case parent.view == jfMoviesView:
		movies()
	case parent.view == jfShowsView:
		shows()
	case parent.show != nil:
		show(parent.show)
	case parent.season != nil:
		season(parent.season)
	}
	return out
}

// jfContext is one request being answered.
type jfContext struct {
	s   *Server
	w   http.ResponseWriter
	r   *http.Request
	q   jfQuery
	u   *User // nil for the few endpoints open to everyone
	cat *Catalog
	arg map[string]string
}

func (c *jfContext) json(v any) {
	c.w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(c.w).Encode(v)
}

func (c *jfContext) noContent() { c.w.WriteHeader(http.StatusNoContent) }

func (c *jfContext) notFound() { http.Error(c.w, "Not Found", http.StatusNotFound) }

func (c *jfContext) items(list []map[string]any, total, start int) {
	if list == nil {
		list = []map[string]any{}
	}
	c.json(map[string]any{"Items": list, "TotalRecordCount": total, "StartIndex": start})
}

func (c *jfContext) empty() { c.items(nil, 0, 0) }

func (c *jfContext) body(v any) {
	json.NewDecoder(io.LimitReader(c.r.Body, 1<<20)).Decode(v)
}

// entry finds the item named in the path.
func (c *jfContext) entry() (jfEntry, bool) {
	e, ok := jfLookup(c.cat, c.arg["id"])
	if !ok {
		c.notFound()
	}
	return e, ok
}

type jfRoute struct {
	method  string
	pattern []string
	public  bool
	handle  func(*jfContext)
}

func jfR(method, pattern string, handle func(*jfContext)) jfRoute {
	return jfRoute{method: method, pattern: strings.Split(strings.Trim(strings.ToLower(pattern), "/"), "/"), handle: handle}
}

func jfPublic(method, pattern string, handle func(*jfContext)) jfRoute {
	r := jfR(method, pattern, handle)
	r.public = true
	return r
}

// match compares a lower-case path with the pattern; {name} takes any one
// segment.
func (rt jfRoute) match(segments []string) (map[string]string, bool) {
	if len(segments) != len(rt.pattern) {
		return nil, false
	}
	var arg map[string]string
	for i, p := range rt.pattern {
		if strings.HasPrefix(p, "{") {
			if arg == nil {
				arg = map[string]string{}
			}
			arg[p[1:len(p)-1]] = segments[i]
		} else if p != segments[i] {
			return nil, false
		}
	}
	return arg, true
}

var (
	jfRoutes     []jfRoute
	jfRoutesOnce sync.Once
	jfUnknown    sync.Map // paths already reported in the log
)

func jfBuildRoutes() {
	empty := (*jfContext).empty
	emptyList := func(c *jfContext) { c.json([]any{}) }
	ok := (*jfContext).noContent
	jfRoutes = []jfRoute{
		// Server and sign-in: open to everyone.
		jfPublic("GET", "/System/Info/Public", (*jfContext).systemInfo),
		jfPublic("GET", "/System/Ping", func(c *jfContext) { c.json("Jellyfin Server") }),
		jfPublic("POST", "/System/Ping", func(c *jfContext) { c.json("Jellyfin Server") }),
		jfPublic("GET", "/Users/Public", emptyList),
		jfPublic("POST", "/Users/AuthenticateByName", (*jfContext).authenticate),
		jfPublic("GET", "/Branding/Configuration", func(c *jfContext) {
			c.json(map[string]any{"LoginDisclaimer": "", "CustomCss": "", "SplashscreenEnabled": false})
		}),
		jfPublic("GET", "/Branding/Css", func(c *jfContext) { c.w.Header().Set("Content-Type", "text/css") }),
		jfPublic("GET", "/QuickConnect/Enabled", func(c *jfContext) { c.json(false) }),
		// Jellyfin serves images without a login, and clients count on it.
		jfPublic("GET", "/Items/{id}/Images/{type}", (*jfContext).image),
		jfPublic("GET", "/Items/{id}/Images/{type}/{index}", (*jfContext).image),
		jfPublic("HEAD", "/Items/{id}/Images/{type}", (*jfContext).image),

		jfR("GET", "/System/Info", (*jfContext).systemInfo),
		jfR("GET", "/System/Endpoint", func(c *jfContext) { c.json(map[string]bool{"IsLocal": true, "IsInNetwork": true}) }),
		jfR("GET", "/Users", func(c *jfContext) { c.json([]any{c.s.jfUser(c.u)}) }),
		jfR("GET", "/Users/Me", func(c *jfContext) { c.json(c.s.jfUser(c.u)) }),
		jfR("GET", "/Users/{user}", func(c *jfContext) { c.json(c.s.jfUser(c.u)) }),
		jfR("POST", "/Sessions/Logout", func(c *jfContext) { c.s.auth.Logout(c.s.token(c.r)); c.noContent() }),
		jfR("POST", "/Sessions/Capabilities", ok),
		jfR("POST", "/Sessions/Capabilities/Full", ok),
		jfR("GET", "/Sessions", emptyList),
		jfR("GET", "/DisplayPreferences/{id}", (*jfContext).displayPreferences),
		jfR("POST", "/DisplayPreferences/{id}", ok),
		jfR("GET", "/Playback/BitrateTest", (*jfContext).bitrateTest),

		// Browsing.
		jfR("GET", "/UserViews", (*jfContext).views),
		jfR("GET", "/Users/{user}/Views", (*jfContext).views),
		jfR("GET", "/UserViews/GroupingOptions", emptyList),
		jfR("GET", "/Users/{user}/GroupingOptions", emptyList),
		jfR("GET", "/Library/MediaFolders", (*jfContext).views),
		jfR("GET", "/Items", (*jfContext).query),
		jfR("GET", "/Users/{user}/Items", (*jfContext).query),
		jfR("GET", "/Items/Latest", (*jfContext).latest),
		jfR("GET", "/Users/{user}/Items/Latest", (*jfContext).latest),
		jfR("GET", "/UserItems/Resume", (*jfContext).resume),
		jfR("GET", "/Users/{user}/Items/Resume", (*jfContext).resume),
		jfR("GET", "/Items/{id}", (*jfContext).one),
		jfR("GET", "/Users/{user}/Items/{id}", (*jfContext).one),
		jfR("GET", "/Shows/NextUp", (*jfContext).nextUp),
		jfR("GET", "/Shows/{id}/Seasons", (*jfContext).seasons),
		jfR("GET", "/Shows/{id}/Episodes", (*jfContext).episodes),

		// Playback.
		jfR("GET", "/Items/{id}/PlaybackInfo", (*jfContext).playbackInfo),
		jfR("POST", "/Items/{id}/PlaybackInfo", (*jfContext).playbackInfo),
		jfR("GET", "/Videos/{id}/{file}", (*jfContext).stream),
		jfR("HEAD", "/Videos/{id}/{file}", (*jfContext).stream),
		jfR("GET", "/Items/{id}/Download", (*jfContext).stream),
		jfR("GET", "/Items/{id}/File", (*jfContext).stream),
		jfR("GET", "/Videos/{id}/{source}/Subtitles/{index}/{file}", (*jfContext).subtitle),
		jfR("GET", "/Videos/{id}/{source}/Subtitles/{index}/{start}/{file}", (*jfContext).subtitle),
		jfR("POST", "/Sessions/Playing", (*jfContext).playing),
		jfR("POST", "/Sessions/Playing/Progress", (*jfContext).playing),
		jfR("POST", "/Sessions/Playing/Stopped", (*jfContext).playing),
		jfR("POST", "/Sessions/Playing/Ping", ok),

		// Watched and favourite marks, under their old and new addresses.
		jfR("POST", "/Users/{user}/PlayedItems/{id}", func(c *jfContext) { c.mark(func(p *Progress) { p.Played, p.Position = true, 0 }) }),
		jfR("DELETE", "/Users/{user}/PlayedItems/{id}", func(c *jfContext) { c.mark(func(p *Progress) { p.Played, p.Position = false, 0 }) }),
		jfR("POST", "/UserPlayedItems/{id}", func(c *jfContext) { c.mark(func(p *Progress) { p.Played, p.Position = true, 0 }) }),
		jfR("DELETE", "/UserPlayedItems/{id}", func(c *jfContext) { c.mark(func(p *Progress) { p.Played, p.Position = false, 0 }) }),
		jfR("POST", "/Users/{user}/FavoriteItems/{id}", func(c *jfContext) { c.mark(func(p *Progress) { p.Favorite = true }) }),
		jfR("DELETE", "/Users/{user}/FavoriteItems/{id}", func(c *jfContext) { c.mark(func(p *Progress) { p.Favorite = false }) }),
		jfR("POST", "/UserFavoriteItems/{id}", func(c *jfContext) { c.mark(func(p *Progress) { p.Favorite = true }) }),
		jfR("DELETE", "/UserFavoriteItems/{id}", func(c *jfContext) { c.mark(func(p *Progress) { p.Favorite = false }) }),
		jfR("GET", "/UserItems/{id}/UserData", func(c *jfContext) { c.mark(func(*Progress) {}) }),

		// Things this server does not have; clients ask anyway.
		jfR("GET", "/Items/{id}/Similar", empty),
		jfR("GET", "/Items/{id}/Intros", empty),
		jfR("GET", "/Users/{user}/Items/{id}/Intros", empty),
		jfR("GET", "/Items/{id}/LocalTrailers", emptyList),
		jfR("GET", "/Users/{user}/Items/{id}/LocalTrailers", emptyList),
		jfR("GET", "/Items/{id}/SpecialFeatures", emptyList),
		jfR("GET", "/Users/{user}/Items/{id}/SpecialFeatures", emptyList),
		jfR("GET", "/Items/{id}/ThemeSongs", empty),
		jfR("GET", "/Items/{id}/ThemeVideos", empty),
		jfR("GET", "/Items/{id}/ThemeMedia", func(c *jfContext) {
			none := map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0, "OwnerId": jfID(c.arg["id"])}
			c.json(map[string]any{"ThemeVideosResult": none, "ThemeSongsResult": none, "SoundtrackSongsResult": none})
		}),
		jfR("GET", "/MediaSegments/{id}", empty),
		jfR("GET", "/Persons", empty),
		jfR("GET", "/Genres", empty),
		jfR("GET", "/Studios", empty),
		jfR("GET", "/Artists", empty),
		jfR("GET", "/Movies/Recommendations", emptyList),
		jfR("GET", "/Items/Filters", func(c *jfContext) {
			c.json(map[string]any{"Genres": []any{}, "Tags": []any{}, "OfficialRatings": []any{}, "Years": []any{}})
		}),
		jfR("GET", "/Items/Filters2", func(c *jfContext) { c.json(map[string]any{"Genres": []any{}, "Tags": []any{}}) }),
		jfR("GET", "/LiveTv/Programs/Recommended", empty),
		jfR("GET", "/Plugins", emptyList),
		jfR("GET", "/Packages", emptyList),
		jfR("GET", "/SyncPlay/List", emptyList),
	}
	// "/Items/Latest" must win over "/Items/{id}": fixed paths go first.
	params := func(rt jfRoute) (n int) {
		for _, p := range rt.pattern {
			if strings.HasPrefix(p, "{") {
				n++
			}
		}
		return n
	}
	sort.SliceStable(jfRoutes, func(a, b int) bool { return params(jfRoutes[a]) < params(jfRoutes[b]) })
}

// jellyfin serves everything that is neither the web interface nor DLNA.
func (s *Server) jellyfin(w http.ResponseWriter, r *http.Request) {
	jfRoutesOnce.Do(jfBuildRoutes)
	path := strings.ToLower(strings.Trim(r.URL.Path, "/"))
	path = strings.TrimPrefix(path, "emby/") // the address old clients use
	segments := strings.Split(path, "/")
	for _, rt := range jfRoutes {
		if rt.method != r.Method {
			continue
		}
		arg, ok := rt.match(segments)
		if !ok {
			continue
		}
		c := &jfContext{s: s, w: w, r: r, q: newJFQuery(r), u: s.user(r), arg: arg}
		if c.u == nil && !rt.public {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var err error
		if c.cat, err = s.lib.Catalog(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rt.handle(c)
		return
	}
	// Reported once: this is how a missing piece of the API shows itself.
	if _, seen := jfUnknown.LoadOrStore(r.Method+" "+path, true); !seen && s.user(r) != nil {
		s.log("Jellyfin API: not supported: %s /%s", r.Method, path)
	}
	http.Error(w, "Not Found", http.StatusNotFound)
}

func (c *jfContext) systemInfo() {
	info := map[string]any{
		"LocalAddress": "http://" + c.r.Host, "ServerName": c.s.name, "Version": jfVersion,
		"ProductName": "Jellyfin Server", "OperatingSystem": "", "Id": c.s.auth.ServerID,
		"StartupWizardCompleted": true,
	}
	if c.u != nil { // the full form, for signed-in clients
		for k, v := range map[string]any{
			"OperatingSystemDisplayName": "", "HasPendingRestart": false, "IsShuttingDown": false,
			"SupportsLibraryMonitor": true, "WebSocketPortNumber": c.s.port, "CanSelfRestart": false,
			"CanLaunchWebBrowser": false, "HasUpdateAvailable": false, "CompletedInstallations": []any{},
			"ProgramDataPath": "", "WebPath": "", "ItemsByNamePath": "", "CachePath": "", "LogPath": "",
			"InternalMetadataPath": "", "TranscodingTempPath": "", "CastReceiverApplications": []any{},
			"EncoderLocation": "System", "SystemArchitecture": "X64", "PackageName": "mediakeeper",
		} {
			info[k] = v
		}
	}
	c.json(info)
}

// jfUser describes an account the way clients expect: they read many of
// these flags without checking that they are present.
func (s *Server) jfUser(u *User) map[string]any {
	now := jfDate(time.Now())
	return map[string]any{
		"Name": u.Name, "ServerId": s.auth.ServerID, "Id": u.ID,
		"HasPassword": true, "HasConfiguredPassword": true, "HasConfiguredEasyPassword": false,
		"EnableAutoLogin": false, "LastLoginDate": now, "LastActivityDate": now,
		"Configuration": map[string]any{
			"PlayDefaultAudioTrack": true, "SubtitleLanguagePreference": "", "DisplayMissingEpisodes": false,
			"GroupedFolders": []any{}, "SubtitleMode": "Default", "DisplayCollectionsView": false,
			"EnableLocalPassword": false, "OrderedViews": []any{}, "LatestItemsExcludes": []any{}, "MyMediaExcludes": []any{},
			"HidePlayedInLatest": true, "RememberAudioSelections": true, "RememberSubtitleSelections": true,
			"EnableNextEpisodeAutoPlay": true, "CastReceiverId": "",
		},
		"Policy": map[string]any{
			"IsAdministrator": u.Admin, "IsHidden": true, "EnableCollectionManagement": false,
			"EnableSubtitleManagement": false, "EnableLyricManagement": false, "IsDisabled": false,
			"BlockedTags": []any{}, "AllowedTags": []any{}, "EnableUserPreferenceAccess": true,
			"AccessSchedules": []any{}, "BlockUnratedItems": []any{},
			"EnableRemoteControlOfOtherUsers": false, "EnableSharedDeviceControl": false, "EnableRemoteAccess": true,
			"EnableLiveTvManagement": false, "EnableLiveTvAccess": false, "EnableMediaPlayback": true,
			"EnableAudioPlaybackTranscoding": false, "EnableVideoPlaybackTranscoding": false, "EnablePlaybackRemuxing": false,
			"ForceRemoteSourceTranscoding": false, "EnableContentDeletion": false, "EnableContentDeletionFromFolders": []any{},
			"EnableContentDownloading": true, "EnableSyncTranscoding": false, "EnableMediaConversion": false,
			"EnabledDevices": []any{}, "EnableAllDevices": true, "EnabledChannels": []any{}, "EnableAllChannels": true,
			"EnabledFolders": []any{}, "EnableAllFolders": true, "InvalidLoginAttemptCount": 0,
			"LoginAttemptsBeforeLockout": -1, "MaxActiveSessions": 0, "EnablePublicSharing": false,
			"BlockedMediaFolders": []any{}, "BlockedChannels": []any{}, "RemoteClientBitrateLimit": 0,
			"AuthenticationProviderId": "Jellyfin.Server.Implementations.Users.DefaultAuthenticationProvider",
			"PasswordResetProviderId":  "Jellyfin.Server.Implementations.Users.DefaultPasswordResetProvider",
			"SyncPlayAccess":           "None",
		},
	}
}

// jfClient reads a field like Device="..." from the authorization header.
func jfClient(r *http.Request, field string) string {
	for _, h := range []string{"Authorization", "X-Emby-Authorization"} {
		for _, part := range strings.Split(r.Header.Get(h), ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "MediaBrowser ")), "=")
			if ok && strings.EqualFold(k, field) {
				return strings.Trim(v, `"`)
			}
		}
	}
	return ""
}

func (c *jfContext) authenticate() {
	var req struct{ Username, Pw, Password string }
	c.body(&req)
	device := firstNonEmpty(jfClient(c.r, "Device"), "Jellyfin client")
	u, token, err := c.s.login(c.r, req.Username, firstNonEmpty(req.Pw, req.Password), device)
	if err != nil {
		status := http.StatusUnauthorized
		if err == errTooManyLogins {
			status = http.StatusTooManyRequests
		}
		http.Error(c.w, err.Error(), status)
		return
	}
	c.s.log("%s signed in from %s (%s)", u.Name, clientIP(c.r), firstNonEmpty(jfClient(c.r, "Client"), device))
	now := jfDate(time.Now())
	c.json(map[string]any{
		"User": c.s.jfUser(u), "AccessToken": token, "ServerId": c.s.auth.ServerID,
		"SessionInfo": map[string]any{
			"PlayState":       map[string]any{"CanSeek": false, "IsPaused": false, "IsMuted": false, "RepeatMode": "RepeatNone", "PlaybackOrder": "Default"},
			"AdditionalUsers": []any{},
			"Capabilities": map[string]any{"PlayableMediaTypes": []any{}, "SupportedCommands": []any{}, "SupportsMediaControl": false,
				"SupportsPersistentIdentifier": true},
			"RemoteEndPoint": clientIP(c.r), "PlayableMediaTypes": []any{}, "Id": token[:32], "UserId": u.ID, "UserName": u.Name,
			"Client": jfClient(c.r, "Client"), "LastActivityDate": now, "LastPlaybackCheckIn": "0001-01-01T00:00:00.0000000Z",
			"DeviceName": device, "DeviceId": jfClient(c.r, "DeviceId"), "ApplicationVersion": jfClient(c.r, "Version"),
			"IsActive": true, "SupportsMediaControl": false, "SupportsRemoteControl": false,
			"NowPlayingQueue": []any{}, "NowPlayingQueueFullItems": []any{}, "HasCustomDeviceName": false,
			"ServerId": c.s.auth.ServerID, "SupportedCommands": []any{},
		},
	})
}

func (c *jfContext) displayPreferences() {
	c.json(map[string]any{
		"Id": "3ce5b65d-e116-d731-65d1-efc4a30ec35c", "SortBy": "SortName", "RememberIndexing": false,
		"PrimaryImageHeight": 250, "PrimaryImageWidth": 250,
		"CustomPrefs": map[string]string{"chromecastVersion": "stable", "skipForwardLength": "30000",
			"skipBackLength": "10000", "enableNextVideoInfoOverlay": "False"},
		"ScrollDirection": "Horizontal", "ShowBackdrop": true, "RememberSorting": false,
		"SortOrder": "Ascending", "ShowSidebar": false, "Client": "emby",
	})
}

// bitrateTest sends the amount of junk a client asks for to measure the
// connection.
func (c *jfContext) bitrateTest() {
	size := min(max(c.q.int("size"), 0), 100<<20)
	c.w.Header().Set("Content-Type", "application/octet-stream")
	c.w.Header().Set("Content-Length", strconv.Itoa(size))
	chunk := make([]byte, 64<<10)
	for size > 0 {
		n := min(size, len(chunk))
		if _, err := c.w.Write(chunk[:n]); err != nil {
			return
		}
		size -= n
	}
}

func jfImageTag(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	sum := md5.Sum([]byte(fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())))
	return hex.EncodeToString(sum[:])
}

func (c *jfContext) image() {
	kind := map[string]string{"primary": "poster", "backdrop": "backdrop", "thumb": "thumb"}[c.arg["type"]]
	file := ""
	if kind != "" {
		file = c.s.imagePath(c.cat, jfID(c.arg["id"]), kind)
	}
	if file == "" {
		c.notFound()
		return
	}
	c.w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(c.w, c.r, file)
}

// userData is the watch state of an entry for the signed-in user. A series
// or a season counts as watched when all its episodes are.
func (c *jfContext) userData(e jfEntry) map[string]any {
	d := map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": false, "Played": false,
		"Key": e.id(), "ItemId": e.id()}
	var episodes []*CatItem
	switch {
	case e.item != nil:
		p := c.s.auth.Progress(c.u.ID, e.item.ID)
		d["PlaybackPositionTicks"] = jfTicks(time.Duration(p.Position * float64(time.Second)))
		d["PlayCount"], d["IsFavorite"], d["Played"] = p.PlayCount, p.Favorite, p.Played
		if total := c.s.lib.Duration(e.item).Seconds(); total > 0 && p.Position > 0 {
			d["PlayedPercentage"] = p.Position / total * 100
		}
		if !p.LastPlayed.IsZero() {
			d["LastPlayedDate"] = jfDate(p.LastPlayed)
		}
		return d
	case e.show != nil:
		episodes = e.show.Episodes()
		d["IsFavorite"] = c.s.auth.Progress(c.u.ID, e.show.ID).Favorite
	case e.season != nil:
		episodes = e.season.Episodes
	default:
		return d
	}
	unplayed := 0
	for _, ep := range episodes {
		if !c.s.auth.Progress(c.u.ID, ep.ID).Played {
			unplayed++
		}
	}
	d["UnplayedItemCount"], d["Played"] = unplayed, unplayed == 0 && len(episodes) > 0
	return d
}

func jfProviderIDs(imdb, tmdb string) map[string]string {
	ids := map[string]string{}
	if imdb != "" {
		ids["Imdb"] = imdb
	}
	if tmdb != "" {
		ids["Tmdb"] = tmdb
	}
	return ids
}

func jfGenres(genres []string) ([]string, []map[string]string) {
	names, items := []string{}, []map[string]string{}
	for _, g := range genres {
		names = append(names, g)
		items = append(items, map[string]string{"Name": g, "Id": catalogID("genre", g)})
	}
	return names, items
}

// dto describes an entry as a Jellyfin BaseItemDto. With full it carries
// the media sources, which lists leave out.
func (c *jfContext) dto(e jfEntry, full bool) map[string]any {
	s := c.s
	d := map[string]any{
		"Name": e.name(), "ServerId": s.auth.ServerID, "Id": e.id(), "Type": e.typ(),
		"SortName": strings.ToLower(e.name()), "IsFolder": e.item == nil, "LocationType": "FileSystem",
		"UserData": c.userData(e), "ImageTags": map[string]string{}, "BackdropImageTags": []string{},
		"ImageBlurHashes": map[string]any{}, "CanDelete": false, "CanDownload": e.item != nil,
		"ExternalUrls": []any{}, "Taglines": []string{}, "People": []any{}, "Studios": []any{},
		"Tags": []any{}, "LockedFields": []any{}, "LockData": false, "PlayAccess": "Full",
		"EnableMediaSourceDisplay": true, "LocalTrailerCount": 0, "RemoteTrailers": []any{},
		"Etag": catalogID("etag", e.id()+e.added().String()), "DisplayPreferencesId": e.id(), "SpecialFeatureCount": 0,
		"MediaType": "Unknown", "Genres": []string{}, "GenreItems": []any{}, "ProviderIds": map[string]string{},
		"ProductionLocations": []any{}, "Chapters": []any{}, "Trickplay": map[string]any{},
	}
	if t := e.added(); !t.IsZero() {
		d["DateCreated"] = jfDate(t)
	}
	if date := e.premiere(); len(date) == 10 {
		d["PremiereDate"] = date + "T00:00:00.0000000Z"
	}
	if y := e.year(); y > 0 {
		d["ProductionYear"] = y
	}
	images := d["ImageTags"].(map[string]string)
	image := func(key, file string) {
		if tag := jfImageTag(file); tag != "" {
			images[key] = tag
			d["PrimaryImageAspectRatio"] = 0.6666666666666666 // a poster; stills are set below
		}
	}
	backdrop := func(file string) {
		if tag := jfImageTag(file); tag != "" {
			d["BackdropImageTags"] = []string{tag}
		}
	}
	seriesFields := func(show *CatShow) {
		d["SeriesName"], d["SeriesId"] = show.Title, show.ID
		if tag := jfImageTag(show.Poster); tag != "" {
			d["SeriesPrimaryImageTag"] = tag
		}
		if tag := jfImageTag(show.Backdrop); tag != "" {
			d["ParentBackdropItemId"], d["ParentBackdropImageTags"] = show.ID, []string{tag}
		}
	}

	switch {
	case e.item != nil:
		it := e.item
		d["MediaType"], d["VideoType"] = "Video", "VideoFile"
		d["Overview"], d["OriginalTitle"], d["OfficialRating"] = it.Plot, it.OriginalTitle, it.MPAA
		d["Genres"], d["GenreItems"] = jfGenres(it.Genres)
		d["ProviderIds"] = jfProviderIDs(it.IMDb, it.TMDB)
		d["Container"] = strings.TrimPrefix(strings.ToLower(filepath.Ext(it.Path)), ".")
		d["Path"] = "/" + filepath.ToSlash(it.Rel)
		if it.Rating > 0 {
			d["CommunityRating"] = it.Rating
		}
		if it.Tagline != "" {
			d["Taglines"] = []string{it.Tagline}
		}
		if dur := s.lib.Duration(it); dur > 0 {
			d["RunTimeTicks"] = jfTicks(dur)
		}
		info := s.lib.Probe(it)
		if info.Width > 0 {
			d["Width"], d["Height"], d["IsHD"] = info.Width, info.Height, info.Height >= 700
		}
		d["HasSubtitles"] = len(it.Subs) > 0 || info.stream("subtitle") != nil
		people := []map[string]string{}
		for _, name := range it.Directors {
			people = append(people, map[string]string{"Name": name, "Id": catalogID("person", name), "Type": "Director", "Role": "Director"})
		}
		for _, p := range it.Cast {
			people = append(people, map[string]string{"Name": p.Name, "Id": catalogID("person", p.Name), "Type": "Actor", "Role": p.Role})
		}
		studios := []map[string]string{}
		for _, name := range it.Studios {
			studios = append(studios, map[string]string{"Name": name, "Id": catalogID("studio", name)})
		}
		d["People"], d["Studios"], d["ProductionLocations"] = people, studios, append([]string{}, it.Countries...)
		if it.Kind == kindMovie {
			d["ParentId"] = jfMoviesView
			image("Primary", it.Poster)
			backdrop(it.Backdrop)
		} else {
			d["ParentId"], d["SeasonId"] = it.Season.ID, it.Season.ID
			d["SeasonName"] = jfEntry{season: it.Season}.name()
			d["IndexNumber"], d["ParentIndexNumber"] = it.Episode, it.Season.Number
			if it.EpisodeEnd > it.Episode {
				d["IndexNumberEnd"] = it.EpisodeEnd
			}
			seriesFields(it.Show)
			if still := s.episodeStill(it); still != "" {
				image("Primary", still)
				d["PrimaryImageAspectRatio"] = 1.7777777777777777
			}
		}
		if full {
			source := c.mediaSource(it)
			d["MediaSources"], d["MediaStreams"] = []any{source}, source["MediaStreams"]
		}
	case e.show != nil:
		show := e.show
		d["Overview"], d["OriginalTitle"], d["OfficialRating"] = show.Plot, show.OriginalTitle, show.MPAA
		d["Genres"], d["GenreItems"] = jfGenres(show.Genres)
		d["ProviderIds"] = jfProviderIDs(show.IMDb, show.TMDB)
		d["ParentId"], d["ChildCount"], d["RecursiveItemCount"] = jfShowsView, len(show.Seasons), len(show.Episodes())
		d["Status"], d["AirDays"] = firstNonEmpty(show.Status, "Ended"), []any{}
		d["DateLastMediaAdded"], d["Path"] = jfDate(show.ModTime), "/"+showsFolder+"/"+show.Title
		if show.Rating > 0 {
			d["CommunityRating"] = show.Rating
		}
		image("Primary", show.Poster)
		backdrop(show.Backdrop)
	case e.season != nil:
		season := e.season
		d["ParentId"], d["IndexNumber"], d["ChildCount"] = season.Show.ID, season.Number, len(season.Episodes)
		seriesFields(season.Show)
		image("Primary", firstNonEmpty(season.Poster, season.Show.Poster))
	default:
		d["CollectionType"] = map[string]string{jfMoviesView: "movies", jfShowsView: "tvshows"}[e.view]
		d["ChildCount"] = len(jfChildren(c.cat, &e, false))
		d["Path"], d["DateCreated"] = "/"+e.name(), "2020-01-01T00:00:00.0000000Z"
		d["ParentId"] = catalogID("view", "root")
	}
	return d
}

var jfTextSubtitles = map[string]bool{"subrip": true, "srt": true, "ass": true, "ssa": true, "webvtt": true, "mov_text": true}

// mediaSource tells a client what is inside the file, so that it can
// decide to play it directly; this server offers nothing else.
func (c *jfContext) mediaSource(it *CatItem) map[string]any {
	info := c.s.lib.Probe(it)
	token := c.s.token(c.r)
	streams := []map[string]any{}
	next, defaultAudio := 0, -1
	for _, st := range info.Streams {
		m := map[string]any{
			"Codec": st.Codec, "Index": st.Index, "IsDefault": st.Default, "IsForced": false, "IsInterlaced": false,
			"IsHearingImpaired": false, "IsExternal": false, "IsTextSubtitleStream": false, "SupportsExternalStream": false,
			"VideoRange": "Unknown", "VideoRangeType": "Unknown", "AudioSpatialFormat": "None", "Level": 0,
			"Language": st.Language, "Title": st.Title, "Profile": st.Profile,
			"DisplayTitle": strings.TrimSpace(strings.ToUpper(st.Codec) + " " + st.Language),
		}
		if st.TimeBase != "" {
			m["TimeBase"] = st.TimeBase
		}
		if st.BitRate > 0 {
			m["BitRate"] = st.BitRate
		}
		if st.Level > 0 {
			m["Level"] = st.Level
		}
		switch st.Type {
		case "video":
			m["Type"], m["Width"], m["Height"] = "Video", st.Width, st.Height
			m["VideoRange"], m["VideoRangeType"] = "SDR", "SDR"
			m["IsAVC"], m["IsAnamorphic"], m["IsInterlaced"] = st.Codec == "h264", false, st.Interlaced()
			m["PixelFormat"], m["AspectRatio"], m["RefFrames"] = st.PixelFormat, st.AspectRatio, st.Refs
			m["BitDepth"] = max(st.BitDepth, 8)
			if strings.Contains(st.PixelFormat, "10") {
				m["BitDepth"] = 10
			}
			if st.FrameRate > 0 {
				m["AverageFrameRate"], m["RealFrameRate"], m["ReferenceFrameRate"] = st.FrameRate, st.FrameRate, st.FrameRate
			}
			m["DisplayTitle"] = strings.TrimSpace(fmt.Sprintf("%dp %s", st.Height, strings.ToUpper(st.Codec)))
		case "audio":
			m["Type"], m["Channels"] = "Audio", st.Channels
			m["ChannelLayout"], m["SampleRate"] = st.ChannelLayout, st.SampleRate
			if defaultAudio < 0 || st.Default {
				defaultAudio = st.Index
			}
		default:
			m["Type"], m["IsTextSubtitleStream"] = "Subtitle", jfTextSubtitles[st.Codec]
			m["DeliveryMethod"] = "Embed"
		}
		streams = append(streams, m)
		next = max(next, st.Index+1)
	}
	for n, sub := range it.Subs {
		index := next + n
		streams = append(streams, map[string]any{
			"Codec": "subrip", "Index": index, "Type": "Subtitle", "IsDefault": false, "IsForced": false,
			"IsInterlaced": false, "IsHearingImpaired": false, "IsExternal": true, "IsTextSubtitleStream": true,
			"SupportsExternalStream": true, "VideoRange": "Unknown", "VideoRangeType": "Unknown",
			"AudioSpatialFormat": "None", "Level": 0, "DeliveryMethod": "External",
			"DisplayTitle": "External " + strings.TrimSuffix(filepath.Base(sub), ".srt"), "Path": filepath.Base(sub),
			"DeliveryUrl": fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/0/Stream.srt?api_key=%s", it.ID, it.ID, index, token),
		})
	}
	source := map[string]any{
		"Protocol": "File", "Id": it.ID, "Type": "Default", "Name": it.Title,
		"Path":      "/" + filepath.ToSlash(it.Rel),
		"Container": strings.TrimPrefix(strings.ToLower(filepath.Ext(it.Path)), "."),
		"Size":      it.Size, "ETag": catalogID("etag", fmt.Sprintf("%s|%d", it.Rel, it.ModTime.UnixNano())),
		"IsRemote": false, "ReadAtNativeFramerate": false, "IgnoreDts": false, "IgnoreIndex": false, "GenPtsInput": false,
		"SupportsTranscoding": false, "SupportsDirectStream": true, "SupportsDirectPlay": true,
		"IsInfiniteStream": false, "UseMostCompatibleTranscodingProfile": false, "RequiresOpening": false,
		"RequiresClosing": false, "RequiresLooping": false, "SupportsProbing": true, "HasSegments": false,
		"VideoType": "VideoFile", "MediaStreams": streams, "MediaAttachments": []any{}, "Formats": []any{},
		"RequiredHttpHeaders": map[string]string{}, "TranscodingSubProtocol": "http",
	}
	if dur := c.s.lib.Duration(it); dur > 0 {
		source["RunTimeTicks"] = jfTicks(dur)
	}
	if info.Bitrate > 0 {
		source["Bitrate"] = info.Bitrate
	}
	if defaultAudio >= 0 {
		source["DefaultAudioStreamIndex"] = defaultAudio
	}
	return source
}

func (c *jfContext) dtos(list []jfEntry, full bool) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		out = append(out, c.dto(e, full))
	}
	return out
}

func (c *jfContext) views() {
	list := jfChildren(c.cat, nil, false)
	c.items(c.dtos(list, false), len(list), 0)
}

func (c *jfContext) one() {
	if e, ok := c.entry(); ok {
		c.json(c.dto(e, true))
	}
}

// page cuts the requested window out of a list.
func (c *jfContext) page(list []jfEntry) ([]jfEntry, int) {
	start := min(max(c.q.int("startindex"), 0), len(list))
	list = list[start:]
	if limit := c.q.int("limit"); limit > 0 && limit < len(list) {
		list = list[:limit]
	}
	return list, start
}

// query is the general listing: children of a folder or a search over the
// whole library, filtered, sorted and paged.
func (c *jfContext) query() {
	var list []jfEntry
	recursive := c.q.bool("recursive")
	switch {
	case c.q["ids"] != "":
		for _, id := range c.q.list("ids") {
			if e, ok := jfLookup(c.cat, id); ok {
				list = append(list, e)
			}
		}
	case c.q["parentid"] != "":
		parent, ok := jfLookup(c.cat, c.q["parentid"])
		if !ok {
			c.empty()
			return
		}
		list = jfChildren(c.cat, &parent, recursive)
	default:
		// A search without a folder means the whole library.
		list = jfChildren(c.cat, nil, recursive || c.q["searchterm"] != "" || c.q["includeitemtypes"] != "")
	}

	term := strings.ToLower(c.q["searchterm"])
	prefix := strings.ToLower(c.q["namestartswith"])
	include, exclude := c.q.list("includeitemtypes"), c.q.list("excludeitemtypes")
	videoOnly := c.q.has("mediatypes", "video")
	kept := list[:0:0]
	for _, e := range list {
		typ := strings.ToLower(e.typ())
		switch {
		case len(include) > 0 && !contains(include, typ), contains(exclude, typ),
			videoOnly && e.item == nil,
			term != "" && !e.matches(term),
			prefix != "" && !strings.HasPrefix(strings.ToLower(e.name()), prefix):
			continue
		}
		if filters := c.q.list("filters"); len(filters) > 0 || c.q["isplayed"] != "" || c.q["isfavorite"] != "" {
			data := c.userData(e)
			played, favorite := data["Played"].(bool), data["IsFavorite"].(bool)
			resumable := e.item != nil && c.s.auth.Progress(c.u.ID, e.item.ID).Position > 0
			switch {
			case contains(filters, "isplayed") && !played, contains(filters, "isunplayed") && played,
				contains(filters, "isfavorite") && !favorite, contains(filters, "isresumable") && !resumable,
				c.q["isplayed"] != "" && c.q.bool("isplayed") != played,
				c.q["isfavorite"] != "" && c.q.bool("isfavorite") != favorite:
				continue
			}
		}
		if genres := c.q.list("genres"); len(genres) > 0 {
			var have []string
			if e.item != nil {
				have = e.item.Genres
			} else if e.show != nil {
				have = e.show.Genres
			}
			match := false
			for _, g := range have {
				match = match || contains(genres, strings.ToLower(g))
			}
			if !match {
				continue
			}
		}
		kept = append(kept, e)
	}
	c.sort(kept)
	page, start := c.page(kept)
	c.items(c.dtos(page, c.q.has("fields", "mediasources")), len(kept), start)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (c *jfContext) sort(list []jfEntry) {
	by := firstNonEmpty(c.q["sortby"], "sortname")
	by = strings.ToLower(strings.Split(by, ",")[0])
	if by == "random" {
		rand.Shuffle(len(list), func(a, b int) { list[a], list[b] = list[b], list[a] })
		return
	}
	less := func(a, b jfEntry) bool { return a.sortKey() < b.sortKey() }
	switch by {
	case "datecreated", "datelastcontentadded":
		less = func(a, b jfEntry) bool { return a.added().Before(b.added()) }
	case "premieredate", "productionyear":
		less = func(a, b jfEntry) bool {
			return firstNonEmpty(a.premiere(), strconv.Itoa(a.year())) < firstNonEmpty(b.premiere(), strconv.Itoa(b.year()))
		}
	case "communityrating":
		rating := func(e jfEntry) float64 {
			if e.item != nil {
				return e.item.Rating
			} else if e.show != nil {
				return e.show.Rating
			}
			return 0
		}
		less = func(a, b jfEntry) bool { return rating(a) < rating(b) }
	case "dateplayed":
		played := func(e jfEntry) time.Time {
			if e.item == nil {
				return time.Time{}
			}
			return c.s.auth.Progress(c.u.ID, e.item.ID).LastPlayed
		}
		less = func(a, b jfEntry) bool { return played(a).Before(played(b)) }
	}
	if strings.HasPrefix(strings.ToLower(c.q["sortorder"]), "desc") {
		asc := less
		less = func(a, b jfEntry) bool { return asc(b, a) }
	}
	sort.SliceStable(list, func(a, b int) bool { return less(list[a], list[b]) })
}

// latest lists what was added most recently: movies, and series by their
// newest episode.
func (c *jfContext) latest() {
	var list []jfEntry
	parent, hasParent := jfLookup(c.cat, c.q["parentid"])
	if !hasParent || parent.view == jfMoviesView {
		list = append(list, jfChildren(c.cat, &jfEntry{view: jfMoviesView}, false)...)
	}
	if !hasParent || parent.view == jfShowsView {
		list = append(list, jfChildren(c.cat, &jfEntry{view: jfShowsView}, false)...)
	}
	sort.SliceStable(list, func(a, b int) bool { return list[a].added().After(list[b].added()) })
	if limit := c.q.int("limit"); limit > 0 && limit < len(list) {
		list = list[:limit]
	}
	c.json(c.dtos(list, false))
}

// resume lists the videos the user stopped in the middle of.
func (c *jfContext) resume() {
	var list []jfEntry
	for _, it := range c.cat.items {
		if c.s.auth.Progress(c.u.ID, it.ID).Position > 0 {
			list = append(list, jfEntry{item: it})
		}
	}
	last := func(e jfEntry) time.Time { return c.s.auth.Progress(c.u.ID, e.item.ID).LastPlayed }
	sort.Slice(list, func(a, b int) bool { return last(list[a]).After(last(list[b])) })
	page, start := c.page(list)
	c.items(c.dtos(page, false), len(list), start)
}

// nextUp offers, for every series the user is watching, the episode after
// the last one watched.
func (c *jfContext) nextUp() {
	type next struct {
		e    jfEntry
		when time.Time
	}
	var found []next
	for _, show := range c.cat.Shows {
		if id := c.q["seriesid"]; id != "" && jfID(id) != show.ID {
			continue
		}
		episodes := show.Episodes()
		lastPlayed, when := -1, time.Time{}
		for i, ep := range episodes {
			if p := c.s.auth.Progress(c.u.ID, ep.ID); p.Played {
				lastPlayed, when = i, p.LastPlayed
			}
		}
		if lastPlayed >= 0 && lastPlayed+1 < len(episodes) {
			found = append(found, next{jfEntry{item: episodes[lastPlayed+1]}, when})
		}
	}
	sort.Slice(found, func(a, b int) bool { return found[a].when.After(found[b].when) })
	list := make([]jfEntry, len(found))
	for i, f := range found {
		list[i] = f.e
	}
	page, start := c.page(list)
	c.items(c.dtos(page, false), len(list), start)
}

func (c *jfContext) seasons() {
	e, ok := c.entry()
	if !ok || e.show == nil {
		if ok {
			c.notFound()
		}
		return
	}
	list := jfChildren(c.cat, &e, false)
	c.items(c.dtos(list, false), len(list), 0)
}

func (c *jfContext) episodes() {
	e, ok := c.entry()
	if !ok || e.show == nil {
		if ok {
			c.notFound()
		}
		return
	}
	var list []jfEntry
	for _, season := range e.show.Seasons {
		if id := c.q["seasonid"]; id != "" && jfID(id) != season.ID {
			continue
		}
		if n := c.q["season"]; n != "" && c.q.int("season") != season.Number {
			continue
		}
		list = append(list, jfChildren(c.cat, &jfEntry{season: season}, false)...)
	}
	page, start := c.page(list)
	c.items(c.dtos(page, c.q.has("fields", "mediasources")), len(list), start)
}

func (c *jfContext) playbackInfo() {
	e, ok := c.entry()
	if !ok || e.item == nil {
		if ok {
			c.notFound()
		}
		return
	}
	c.json(map[string]any{"MediaSources": []any{c.mediaSource(e.item)}, "PlaySessionId": randomHex(16)})
}

// stream hands out the file itself. Converted streams (HLS playlists,
// other containers) are not offered: the media sources say so.
func (c *jfContext) stream() {
	e, ok := c.entry()
	file := c.arg["file"]
	if !ok || e.item == nil || (file != "" && !strings.HasPrefix(file, "stream")) {
		if ok {
			c.notFound()
		}
		return
	}
	c.s.serveVideo(c.w, c.r, e.item, c.u.Name)
}

func (c *jfContext) subtitle() {
	e, ok := c.entry()
	if !ok || e.item == nil {
		if ok {
			c.notFound()
		}
		return
	}
	// External subtitles are numbered after the tracks inside the file.
	first := 0
	for _, st := range c.s.lib.Probe(e.item).Streams {
		first = max(first, st.Index+1)
	}
	n := atoi(c.arg["index"]) - first
	if n < 0 || n >= len(e.item.Subs) {
		c.notFound()
		return
	}
	if strings.HasSuffix(c.arg["file"], ".vtt") {
		c.s.subtitles(c.w, c.r, e.item, n, 0)
		return
	}
	text, err := subtitleText(e.item.Subs[n])
	if err != nil {
		c.notFound()
		return
	}
	c.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(c.w, text)
}

// playing receives the progress reports of a player.
func (c *jfContext) playing() {
	var req struct {
		ItemId        string
		PositionTicks int64
	}
	c.body(&req)
	if it := c.cat.items[jfID(req.ItemId)]; it != nil {
		position := time.Duration(req.PositionTicks * 100).Seconds()
		if position > 0 || strings.HasSuffix(strings.ToLower(c.r.URL.Path), "/stopped") {
			c.s.auth.Watch(c.u.ID, it.ID, position, c.s.lib.Duration(it).Seconds())
		}
	}
	c.noContent()
}

// mark changes the watched or favourite state and answers with the result.
// Marking a series or a season marks its episodes.
func (c *jfContext) mark(change func(*Progress)) {
	e, ok := c.entry()
	if !ok {
		return
	}
	targets := []string{e.id()}
	switch {
	case e.show != nil:
		for _, ep := range e.show.Episodes() {
			targets = append(targets, ep.ID)
		}
	case e.season != nil:
		for _, ep := range e.season.Episodes {
			targets = append(targets, ep.ID)
		}
	}
	for _, id := range targets {
		c.s.auth.Update(c.u.ID, id, func(p *Progress) {
			before := p.Played
			change(p)
			if p.Played && !before {
				p.PlayCount, p.LastPlayed = p.PlayCount+1, time.Now()
			}
		})
	}
	c.json(c.userData(e))
}
