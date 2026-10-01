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
- API keys (OMDb, TMDB, Kinopoisk) live only in the user's `config.yaml`
  (gitignored); never put them into code, tests or docs.
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
| CLI, settings, library folders | `main.go`, `config.go`, `roots.go`, `ui.go` |
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
- **Settings**: every new setting goes into `Config`/`ServerConfig`
  (`config.go`) *and* into `saveConfig`, which rewrites the whole commented
  file — a field missing there is silently dropped by `-setup`. Loading uses
  `KnownFields(true)`: a misspelt key is an error.
- **Web UI**: `h(tag, attrs, ...children)` builds DOM and skips
  `false`/`null` children; plain DOM methods (`replaceChildren`, `append`) do
  **not** — `cond && el` there prints "undefined". Use `cond ? el : ""`.
- **Never change the item-ID scheme** casually: IDs are
  `md5(kind \0 rootKey + relPath)` (`catalogID`, `rootKey`); watch states,
  ratings, history and Jellyfin clients depend on them. The first library
  folder has an empty root key on purpose (its IDs predate multiple folders).
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
  — scratch libraries only. The sample library `media/` (gitignored) is the
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
- **Paths**: database `-db` > `MEDIAKEEPER_DB` > `server.database` >
  `mediakeeper.db` next to the settings; cache (screenshots, stills) `-cache`
  > `MEDIAKEEPER_CACHE` > `server.cache` > `<first folder>/.cache`
  (`chosenPath`: flag/env relative to the working directory, settings
  relative to the settings file's folder).
- **Settings location**: `-config` > `MEDIAKEEPER_CONFIG` > `config.yaml`
  next to the binary (if writable and not a `go run`/`go test` build) >
  `~/.config/mediakeeper/`; old settings move next to the binary once.
  Docker sets `MEDIAKEEPER_CONFIG=/config/mediakeeper/config.yaml`.
- **Files that already have an `.nfo` are left alone** by the organizer
  unless `-refresh`.
