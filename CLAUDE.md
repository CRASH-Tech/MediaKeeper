# CLAUDE.md

Notes for Claude working on this repository. The architecture is described in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md); the user-facing documentation is
[README.md](README.md). Read the relevant parts of both before larger changes.

## What this is

**MediaKeeper** — one Go binary (module `mediakeeper`, package `main`) that

1. **organizes a media library** from the command line: identifies movies and
   series in online catalogues (TMDB, OMDb, Kinopoisk, TVMaze, Wikidata, IMDb
   suggestions, Letterboxd), renames and files them the Jellyfin way
   (`Movies/Title (Year)/`, `Shows/Show (Year)/Season NN/`), writes `.nfo`,
   artwork and tags into the files, with an undo journal;
2. **serves it** with `-serve` on one port: a web interface (embedded from
   `web/`), a Jellyfin-compatible API for native apps (Swiftfin, Infuse,
   Findroid, Jellyfin for Android TV…), DLNA/UPnP with SSDP discovery, and
   downloads (HTTP links, torrents and magnets via `aria2c`) that are
   identified and filed automatically.

## Working with the user

- The user writes in **Russian**: answer in Russian. Everything in the
  repository — code, comments, UI texts, README, docs — is in **English**.
- **Do not commit or push** unless asked. Do not touch the user's own running
  server or their settings: for live checks run the binary with
  `-config <scratchpad dir>` (see "Checking live" below).
- API keys (OMDb, TMDB, Kinopoisk) live only in the user's settings (the
  `settings` row of `mediakeeper.db`, or a `config.yaml` not yet taken into
  it — both gitignored); never put them into code, tests or docs.
- Never send the user's e-mail or other personal data to outside services
  (e.g. in a User-Agent).
- The UI is a "Liquid Glass" design (glass panels, capsules, blur); keep new
  UI consistent with it, and make it work on phones (a 390 px wide viewport).

## Commands

```sh
go build -o mediakeeper .            # web/ is embedded with go:embed
go test ./...                        # ~15 s; some tests use ffmpeg/ffprobe/mkvtoolnix if installed
test -z "$(gofmt -l .)" && go vet ./... && go test ./...   # exactly what CI runs — run before handing over
GOTOOLCHAIN=go1.24.0 go build .      # go.mod promises Go 1.24: check after touching dependencies
```

- CI (`.github/workflows/build.yml`): gofmt, vet, tests on push/PR; binaries
  for linux amd64/arm64/armv7, darwin amd64/arm64, windows amd64 with
  `CGO_ENABLED=0`; a GitHub release on `v*` tags; a Docker image to Docker
  Hub (`DOCKERHUB_USERNAME` variable, `DOCKERHUB_TOKEN` secret).
- Everything must build **without cgo** — that is why SQLite is
  `modernc.org/sqlite` (pinned at v1.46.1, the newest that needs only Go 1.24;
  newer ones pull `modernc.org/libc` that needs Go 1.26 — rebuild `go.mod`
  from scratch with `go mod tidy` if versions creep up).
- Dependencies: `gopkg.in/yaml.v3`, `modernc.org/sqlite`. Keep it that way
  unless there is a strong reason.

## Layout (all in the root, package main)

| Area | Files |
|---|---|
| CLI, settings, library folders | `main.go`, `config.go` (paths, `config.yaml`), `settings.go` (settings in the DB, live changes, settings/setup API), `roots.go`, `ui.go` |
| Scanning and file-name parsing | `scan.go`, `parse.go` |
| Catalogue sources | `model.go` (Hub, Provider, Movie/Show), `tmdb.go`, `omdb.go`, `kinopoisk.go`, `tvmaze.go`, `wikidata.go`, `imdb.go`, `letterboxd.go` |
| Identification | `identify.go` (search, autoPick, console dialog, refs), `identify_api.go` (web: candidates, `fix`, `fixUnit`) |
| Filing and undo | `plan.go` (AddMovie/AddShow, Apply), `nfo.go`, `tagger.go`, `undo.go` |
| Server core | `server.go` (Server, Handler, roots, slots, `moved`), `web.go` (web API), `users.go` (SQLite: accounts, sessions, watch states, history), `mine.go` (ratings, watchlist, history API) |
| Catalogue for the server | `catalog.go` (CatItem/CatShow, IDs), `probe.go` (ffprobe cache) |
| Editing metadata | `meta.go` (movies, generic `.nfo` XML editing, tag writer), `meta_show.go` (series, episodes) |
| Playback | `convert.go` (`convertArgs`: copy/re-encode, burned-in subtitles, graphics cards), `hls.go` (growing HLS for Safari), `vod.go` (whole-film HLS), `mkvcues.go` (Matroska key frame index), `subtitles.go` (tracks, extraction to WebVTT), `screens.go` (screenshots, episode stills) |
| Jellyfin API | `jellyfin.go` (routes, DTOs), `jellyfin_play.go` (PlaybackInfo, device profiles, play sessions, master playlist), `debug.go` (`-debug` request log) |
| DLNA | `dlna.go`, `dlna_library.go` (its own scan of the folders), `ssdp.go` |
| Downloads | `downloads.go` (HTTP, aria2c, organizing, presets) |
| Web UI | `web/index.html`, `web/static/app.js` (vanilla JS, no build step), `web/static/style.css`, icons |

## Conventions

- **Comment style**: explain *why*, in plain sentences, at the density of the
  surrounding code. Doc comments start with the name. No TODO litter.
- **Naming in UI and code**: "series" for shows in prose, `kindMovie`,
  `kindTV` (catalogue entry), `kindEpisode` (a file of a series); folders are
  `Movies/` and `Shows/` (`moviesFolder`, `showsFolder`).
- **JSON** to the web UI is built as `map[string]any`; include optional
  fields only when set (see `mineJSON`).
- **Errors** shown to users are full English sentences, lower-case start,
  no trailing period (`errors.New("the title cannot be empty")`).
- **Settings** live in the database (`Auth.Settings`/`SaveSettings`: a JSON
  `Config` in `meta`), changed under Settings in the web UI. A new setting
  goes into `Config`/`ServerConfig` (with a JSON tag), `mergeConfig` (import
  from `config.yaml`), `applySettings` (putting it into force, or saying it
  takes a restart), `settingsView`/`settingsChange` (the API) and the
  Settings page in `app.js`; if a flag or variable can set it, `main.go`
  records it in `locked` (the page shows it read-only). Server code reads
  settings only through the accessors (`s.config()`, `s.libRoots()`,
  `s.firstRoot()`, `s.serverName()`, `s.cacheRoot()`, `s.card()`, …) — they
  change while the server runs. `config.yaml` is an inbox: whatever it holds
  besides `server.database` is merged into the DB at start and the file is
  rewritten (old one kept as `config.yaml.old`).
- **Web UI**: `h(tag, attrs, ...children)` builds DOM, flattens arrays and
  skips `false`/`null` children; plain DOM methods (`replaceChildren`,
  `append`) do **not** — an array or `cond && el` there prints as text. Use
  `fill(el, ...children)`, which takes children the way `h()` does.
- **Never change the item-ID scheme** casually: IDs are
  `md5(kind \0 rootKey + relPath)` (`catalogID`, `rootKey`); watch states,
  ratings, history and Jellyfin clients depend on them. A folder's key is
  saved with it (`Root.Key`/`HasKey`, given by `withKeys`), so removing or
  reordering folders keeps IDs. Keys given by place match the old scheme:
  the first folder has an empty key (its IDs predate multiple folders), the
  others their path + `\x00`.
- When the server renames files, call `s.moved(before, map[old]new)` so watch
  states, history and download links follow (`Auth.Moved`).

## Testing

- Tests run offline against `fakeServices` (`main_test.go`), an httptest
  server imitating TMDB, OMDb, Kinopoisk, TVMaze, Wikidata, IMDb, Letterboxd.
  `setup(t, cfg)` points the config at a temp dir, sets
  `MEDIAKEEPER_CONFIG=""` and **empties PATH** (no ffmpeg/mkvpropedit) —
  tests needing tools `exec.LookPath` them *before* calling setup/serverFixture
  and set `s.ffmpeg`, `s.prober.tool` afterwards.
- `serverFixture(t)` (`server_test.go`) gives a Server over
  `dlnaLibraryFiles` (Iron Man movie, Star Trek: Enterprise series, a loose
  AVI) with users `boss` (admin) and `kid`, passwords `<name>-password`.
  `newBrowser(t, srv, user)` is a cookie client with `get/post/do/json`.
- Gotcha: `json.Unmarshal` into a value that was decoded before keeps fields
  the new answer leaves out — decode into a fresh variable.
- Real-tool tests (screenshots, stills, HLS, VOD segment timing, tags, cover
  embedding) skip when ffmpeg/ffprobe/mkvtoolnix are missing; CI installs
  them.

## Checking the web UI and live behaviour

- There is **no node** on the host. Syntax-check the UI after every edit:
  `docker run --rm -v $PWD/web/static:/w --entrypoint node zenika/alpine-chrome:with-puppeteer --check /w/app.js`
- Headless browser: scripts in the scratchpad, run with
  `docker run --rm --network host -v <scratch>/shots:/shots -e NODE_PATH=/usr/src/app/node_modules --entrypoint node zenika/alpine-chrome:with-puppeteer /shots/x.js http://127.0.0.1:8231`
  and look at the screenshots. An `alert()` blocks puppeteer clicks
  ("Input.dispatchMouseEvent timed out").
- Live server for checks: `./mediakeeper -config <scratch>/srvcfg/mediakeeper -serve -port 8231 -dlna=false <scratch>/libN`
  — scratch libraries only. A fresh `-config` folder with no folders on the
  command line starts in setup mode: the web UI shows the first-start wizard
  (`/api/setup`) until an administrator is made. The sample library `media/` (gitignored) is the
  user's: read it, symlink its files into a scratch library, but do not
  serve it directly (the server writes `.cache/` into the first folder).
- The web files are embedded: rebuild and restart after UI edits.
- `-debug` logs every Jellyfin-app request; full bodies go to
  `jellyfin-debug.log` next to the settings (tokens blanked). The user's
  settings and database now live next to the binary in the repo root
  (`config.yaml`, `mediakeeper.db`, `jellyfin-debug.log` — all gitignored),
  so their debug log can be read directly.
- Live-testable sources from this machine: OMDb (key in the user's config),
  TVMaze, IMDb suggestions, Letterboxd pages, Wikidata via its SPARQL
  endpoint (the plain wikidata.org API rate-limits after a few requests).
  TMDB is not reachable from here; Kinopoisk has no key.

## Things that were hard-won (do not undo)

- **Jellyfin version**: the server reports `12.0.0` (`jfVersion`); Swiftfin
  refuses older. Tokens are accepted every way clients send them.
- **Play sessions**: Swiftfin hands the stream address to AVPlayer/VLC,
  which send no token. PlaybackInfo issues a `PlaySessionId`; `/Videos/{id}/stream`
  and the HLS routes accept it instead of a login (for that item only).
- **Device profiles**: PlaybackInfo plays a file as it is only if the app's
  `DirectPlayProfiles` take its container/video/audio; otherwise
  `TranscodingUrl` → `/Videos/{id}/master.m3u8` → whole-film HLS (`vod.go`).
- **Whole-film HLS cutting**: ffmpeg keeps source time stamps (`-copyts`);
  a copied track is cut at Matroska cue key frames, sought with a 0.5 s
  margin (`vodSeekMargin`; seeking exactly to a key frame starts one GOP
  early) and cuts are only key frames with ≥1 s to the next one; a re-encoded
  track uses `-force_key_frames expr:gte(t,n_forced*6)` — `t` is the run's
  own clock even with `-copyts`, and runs start on the 6 s grid.
- **Growing HLS** (Safari web player): fixed `EXT-X-TARGETDURATION` per
  session (Safari stops when it changes), ffmpeg paused with SIGSTOP when far
  ahead. Interlaced H.264 is converted with bwdif, never copied.
- **Subtitles**: text tracks are extracted all at once per file into
  `<cache>/subtitles/` and served as WebVTT with an offset; picture tracks
  (PGS/DVD) are burned in (`convertOptions.burn` = ffmpeg's `0:s:N`, `-1` for
  none — every caller sets it explicitly; 0 means the first track!).
- **Hardware conversion** (`convert.go`): `hwaccel` none/auto/vaapi/qsv/nvenc,
  chosen only if a test encode works at start; a file the card fails on is
  remembered (`hwGaveUp`) and goes to the CPU (VOD retries at once). There is
  no GPU on this machine (a VM): hardware paths are tested by their arguments
  and by a failing fake device only.
- **Conversion slots**: at most two ffmpeg conversions; a viewer's new stream
  ends their previous one first (`viewerStreams`), and a slot is waited for
  up to 10 s.
- **Web player**: converted video gets the app's own controls over the
  picture (the browser's cannot seek a stream of unknown length; Safari calls
  it "Live Broadcast"); full screen is the whole player.
- **Paths**: database `-db` > `MEDIAKEEPER_DB` > `server.database` of
  `config.yaml` > `mediakeeper.db` next to `config.yaml`; cache (screenshots,
  stills) `-cache` > `MEDIAKEEPER_CACHE` > the `cache` setting >
  `<first folder>/.cache` > `cache` next to the database (`chosenPath`:
  flag/env relative to the working directory, settings relative to the
  folder of `config.yaml`).
- **First start**: with no account, `/api/setup` is open (and
  `/api/settings/folders`, to choose folders); its POST validates the
  settings first, then makes the administrator and signs them in, and closes
  for good. `MEDIAKEEPER_ADMIN_PASSWORD` makes/resets the account instead.
  A fresh start keeps the database in memory (`OpenMemoryAuth`) until the
  setup writes it to the place chosen — nothing is created before that.
  The folder picker can make folders (POST `/api/settings/folders`).
  The wizard and Settings can move the database live (`Auth.MoveTo`, VACUUM
  INTO + swapping the pointer): always reach the DB through `a.conn()`,
  never keep a `*sql.DB` around.
  Library folders a server first starts with (command line, or `/media` in
  Docker) are saved once (`firstLibraries`, `LibrariesSet`), so the image's
  command is just `-serve`.
- **Settings location**: `-config` > `MEDIAKEEPER_CONFIG` > `config.yaml`
  next to the binary (if writable and not a `go run`/`go test` build) >
  `~/.config/mediakeeper/`; old settings move next to the binary once.
  A `-config`/`MEDIAKEEPER_CONFIG` path that is not a `.yaml` file is a
  folder, even one not made yet (made only when something is written).
  Docker sets `MEDIAKEEPER_CONFIG=/config` (`earlierLayout` keeps an old
  `/config/mediakeeper` in use). Fresh starts — server or organizer — keep
  the DB in memory until there is something to save.
- **Files that already have an `.nfo` are left alone** by the organizer
  unless `-refresh`.
