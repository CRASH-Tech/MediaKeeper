# MediaKeeper

An interactive command-line tool that turns a folder of downloaded movies and
series into a tidy media library, and a media server for that library: a web
interface to browse, watch and download, a Jellyfin-compatible API for
Jellyfin apps, and DLNA for TVs.

- Finds each title in online catalogues and renames files and folders the way
  Jellyfin, Kodi and Plex expect.
- Sorts movies into `Movies/` and episodes into `Shows/<Series>/Season NN/`.
- Writes `.nfo` files, downloads posters and backdrops, and writes tags into
  the files themselves.
- Asks when a file name is not enough: pick from a list, type another title,
  or paste an IMDb number or a link.
- Every run can be reverted with `-undo`.
- `-serve` turns the library into a server: watch in a browser, in Jellyfin
  apps or on a TV; give it a link, a magnet or a torrent and it downloads,
  identifies and files the result.

```
media/                                      media/
├── Iron Man (2008) IMAX.mkv                ├── Movies/
├── Myatezh.2025.AMZN.WEB-DLRip.AVC.mkv     │   ├── Iron Man (2008)/
├── На линии огня.mkv                 →     │   │   ├── Iron Man (2008).mkv
└── Startrek/                               │   │   ├── Iron Man (2008).nfo
    ├── Star.Trek.Enterprise.s1e01-02….mkv  │   │   ├── poster.jpg
    └── Star.Trek.Enterprise.s2e01….mkv     │   │   └── backdrop.jpg
                                            │   ├── In the Line of Fire (1993)/
                                            │   └── Mutiny (2026)/
                                            └── Shows/
                                                └── Star Trek - Enterprise (2001)/
                                                    ├── tvshow.nfo, poster.jpg
                                                    ├── Season 01/
                                                    │   └── Star Trek - Enterprise S01E01-E02 - Broken Bow.mkv
                                                    └── Season 02/
```

## Contents

- [Install](#install)
- [Organizing a library](#organizing-a-library)
- [Sources and API keys](#sources-and-api-keys)
- [Media server](#media-server): [users](#users), [web interface](#web-interface), [downloads](#downloads), [Jellyfin apps](#jellyfin-apps), [DLNA](#dlna)
- [Docker](#docker)
  - [Identifying files with Docker](#identifying-files-with-docker)
- [Options](#options)
- [Settings file](#settings-file)
- [Building from source](#building-from-source)

## Install

**Binary.** Download the archive for your system from the
[Releases](../../releases) page and unpack it. There are builds for Linux
(amd64, arm64, armv7), macOS (Intel and Apple silicon) and Windows.

**Docker.** `OWNER/mediakeeper` on Docker Hub — see [Docker](#docker).

**Optional tools.** Tags are written into the files with `mkvpropedit` (MKV)
and `ffmpeg` (other containers). The server reads durations and codecs with
`ffprobe`, converts video for the web player with `ffmpeg`, and downloads
torrents with `aria2c`. Without them everything else still works.

```sh
sudo apt install mkvtoolnix ffmpeg aria2      # Debian, Ubuntu
brew install mkvtoolnix ffmpeg aria2          # macOS
```

The Docker image already contains them.

## Organizing a library

```sh
mediakeeper -dry-run /path/to/media     # show the plan, change nothing
mediakeeper /path/to/media              # identify, confirm, apply
mediakeeper -undo /path/to/media        # put everything back
```

The directory is scanned recursively. For every movie and series MediaKeeper
searches the catalogues by the title and year taken from the file name. A
confident match is accepted silently; otherwise a window appears:

```
╭─ What is this? ────────────────────────────────────────────────────────────╮
│ File: Myatezh.2025.AMZN.WEB-DLRip.AVC.mkv                                  │
│ Guessed: movie "Myatezh" (2025)                                            │
├────────────────────────────────────────────────────────────────────────────┤
│ OMDb:                                                                      │
│    1) Myatezh (1929)                                                       │
│ Wikidata:                                                                  │
│    2) Мятеж (2026) — Mutiny                                                │
│ IMDb:                                                                      │
│    3) Mutiny (2026)                                                        │
├────────────────────────────────────────────────────────────────────────────┤
│ a number     pick an entry from the list                                   │
│ a title      search all sources; any language, a year helps: Форсаж 2026   │
│ tt0371746    IMDb number;  also tmdb:1726, kp:61237, tvmaze:714            │
│ a link       to IMDb, TMDB, Kinopoisk, Letterboxd or TVMaze                │
│ s  skip      q  quit                                                       │
╰────────────────────────────────────────────────────────────────────────────╯
>
```

After all files are identified the full plan is shown and nothing is touched
until you confirm it.

Good to know:

- **Nothing is overwritten.** If the target name is taken, the file stays
  where it is and the plan says so.
- **Titles stay where they are.** `Movies/` and `Shows/` are created in the
  folder the files were found in, whichever parent folder you scan. A title
  that already has its own folder is moved as a whole, with everything in it.
  `-out DIR` gathers the library in one place instead.
- **Runs are repeatable.** A second run recognizes organized titles by their
  `.nfo` and changes nothing.
- **Search is forgiving.** Release junk in names is ignored, transliterated
  titles (`Myatezh`) are also tried in Cyrillic, long localized titles are
  searched by their parts, and a single file is looked up among series too
  (a miniseries can be kept as a movie).
- **Subtitles** and other files with the same base name follow the video.
- **`-undo`** restores names and folders and removes what the run created.
  Tags written into files cannot be undone — use `-no-tags` while
  experimenting.

## Sources and API keys

| Source       | Key  | Movies | Series | Language      | Notes                                        |
|--------------|------|:------:|:------:|---------------|----------------------------------------------|
| `tmdb`       | yes  |   ✓    |   ✓    | any (`-lang`) | The richest data                             |
| `tvmaze`     | no   |        |   ✓    | English       | Episode titles, stills, season posters       |
| `omdb`       | yes  |   ✓    |   ✓    | English       | IMDb data                                    |
| `kinopoisk`  | yes  |   ✓    |   ✓    | Russian       | Via kinopoiskapiunofficial.tech              |
| `wikidata`   | no   |   ✓    |   ✓    | search only   | Finds titles by their Russian name           |
| `imdb`       | no   |   ✓    |   ✓    | search only   | Finds titles; details come from other sources |
| `letterboxd` | no   |   ✓    |        | English       | Film pages by link, IMDb number or exact title |

MediaKeeper works with no keys at all. Keys are free and add better data:

- TMDB — <https://www.themoviedb.org/settings/api>
- OMDb — <https://www.omdbapi.com/apikey.aspx>
- Kinopoisk — <https://kinopoiskapiunofficial.tech>

Enter them with `mediakeeper -setup`, or set `TMDB_API_KEY`, `OMDB_API_KEY`,
`KINOPOISK_API_KEY`.

The table is in priority order: the first source with a confident match names
the file. Change the order, or leave sources out, with
`-sources omdb,tvmaze,wikidata`. A source that has no key, is unreachable or
rejects the key is simply left out of the lists.

When the data is not in Russian, the Russian title is looked up in Wikidata
and stored in the `.nfo` (`<localizedtitle lang="ru">`); the DLNA server shows
it next to the main one.

With `-serve`, `-no-tags`, `-sources` and `-lang` apply to the downloads the
server organizes.

## Media server

```sh
mediakeeper -serve /path/to/media
mediakeeper -serve -port 8200 -name "Living room" /path/to/media
```

One port serves three things:

- a **web interface** to browse the catalogue, watch in the browser and —
  for administrators — download new titles;
- a **Jellyfin-compatible API**, so that Jellyfin apps can use MediaKeeper as
  their server;
- **DLNA** for TVs and players on the local network.

Titles, descriptions and artwork come from the `.nfo` and image files, so an
organized library looks best; files that were never organized are listed too.
New files appear without a restart.

### Users

Browsing and watching in the web interface needs no login; anything that
changes the library does. Start the server with `-guests=false` to require a
login for watching too. Accounts have one of two roles:

- **administrator** — downloads, decides what unclear files are, corrects
  titles, manages users;
- **viewer** — browses and watches, and has their watch progress remembered
  (a guest's is not).

The Jellyfin API always needs an account; DLNA never does.

On the first start an administrator `admin` is created and its password is
printed once. To choose your own, or to reset a forgotten one, start the
server with `MEDIAKEEPER_ADMIN_PASSWORD` (and optionally `MEDIAKEEPER_ADMIN`
for the name). More users are added on the **Users** page. Accounts, login
tokens and watch progress are kept in `server.json` next to the settings
file (see below); passwords are stored as salted PBKDF2 hashes.

The server speaks plain HTTP. To reach it from the internet, put it behind a
reverse proxy with TLS.

### Web interface

Open the address printed at start. **Movies** and **Shows** are the catalogue;
the lists above it narrow it down by genre, year, country, actor, director,
writer or studio, and put it in another order.

A title page shows the description in groups — plot, genres, director,
writers, cast, studio, country — and every value is a link: click an actor or
a genre to see everything else in the library that shares it.

The player takes the file as it is when the browser can play it
(MP4/WebM/Matroska with H.264, VP9 or AV1 video and AAC, MP3 or Opus audio).
Anything else — AVI, HEVC, AC3 or DTS sound — is converted by `ffmpeg` on the
fly; this costs the server CPU, and at most two conversions run at a time.
Safari (macOS, iPad, iPhone) gets the converted video as HLS, the only
streamed form it plays without downloading everything first; other browsers
get a plain stream. Converted video is sought with the slider under the
picture. Progress is remembered per user, and a series continues with the
next episode.

An administrator can correct a title that was identified wrongly: the small
**✎ Edit** button in the corner of its page offers the candidates from all sources, a search by
another title and an IMDb number or link. The files are renamed and moved as
for a new title and the description and artwork are replaced; the change can
be reverted with `mediakeeper -undo`.

### Downloads

Administrators have a **Downloads** page that accepts:

- a direct `http(s)` link to a video file;
- a magnet link;
- a `.torrent` file, uploaded or by link.

Links to files are fetched by MediaKeeper itself. Torrents and magnets need
[`aria2c`](https://aria2.github.io/) installed (it is in the Docker image);
MediaKeeper starts and drives it, nothing has to be configured. Nothing is
seeded after the download completes.

A finished download is identified the same way as on the command line and
moved into `Movies/` or `Shows/` with its `.nfo` and artwork. When the
program is not sure, the download is marked **Needs you**: the page shows the
candidates from all sources, lets you search by another title or paste an
IMDb number or a link, say that a series file is a single episode or the
whole series, or delete the files. Downloads are kept in the hidden folder
`.incoming` inside the library until they are filed.

### Jellyfin apps

Add the server's address (`http://host:8200`) in the app and sign in with a
MediaKeeper user. What is implemented: sign-in, the Movies and Shows
libraries, search, series/seasons/episodes, artwork, direct play with
seeking, external `.srt` subtitles, watched state, resume and "next up".

What to expect:

- **Only direct play.** The server does not transcode for Jellyfin clients;
  the app's player has to handle the file itself (most native apps do).
- **Native apps only.** Apps that are a shell around the server's own web
  page — the official *Jellyfin for Android/iOS* and *Jellyfin Media Player*
  — do not work: MediaKeeper does not ship Jellyfin's web client. Apps with
  their own interface (Jellyfin for Android TV, Swiftfin, Findroid, Infuse,
  Jellyfin for Kodi and the like) are the ones this is meant for.
- **A subset of the API.** Answers were compared field by field with a real
  Jellyfin 10.10 server, but no app was actually run against it yet. A
  request the server does not know is written to its log once as
  `Jellyfin API: not supported: …` — that line is what to report.

### DLNA

TVs and players on the same network find the server by themselves. It offers
**Movies**, **Shows** → series → season → episodes, and **Folders** (the
directory as it is on disk); `.srt` subtitles next to a video are offered to
the player. There is no transcoding.

DLNA has no notion of users: anyone on the local network can browse and
watch. Switch it off with `-dlna=false` if that is not wanted. The firewall
has to allow the HTTP port (8200/tcp by default) and, for DLNA, 1900/udp.

## Docker

The image contains MediaKeeper, ffmpeg, mkvtoolnix and aria2. By default it
runs the server for `/media`; settings and accounts are kept in `/config`.

[docker-compose.yaml](docker-compose.yaml) is a ready example. Replace `OWNER`
in the image name, set the administrator's password, check `user` (the owner
of your media folder: `id -u`, `id -g`) and the path to the library, then:

```sh
docker compose up -d        # start the server
docker compose logs -f      # see who signs in and what is being played
```

Then open `http://<host>:8200/`.

The example uses `network_mode: host`. DLNA needs it: players discover the
server by multicast, which does not pass through Docker's bridge network
(this works on Linux; Docker Desktop on macOS and Windows cannot do it).
Without DLNA (`-dlna=false`) an ordinary port mapping `8200:8200` is enough.

### Identifying files with Docker

New titles are easiest to add on the Downloads page of the web interface.
For files that are already on disk, organizing is done on the command line;
it is interactive, so it runs in a one-off container with a terminal
attached, not in the background service.

**With the compose file** — the one-off container uses the same volumes, user
and settings as the service:

```sh
docker compose run --rm mediakeeper -setup              # API keys, once (optional)
docker compose run --rm mediakeeper -dry-run /media     # show the plan only
docker compose run --rm mediakeeper /media              # identify and organize
docker compose run --rm mediakeeper -undo /media        # revert the last run
```

The server may keep running meanwhile; it picks up the new names within half
a minute.

**With plain `docker run`:**

```sh
docker run --rm -it \
  --user "$(id -u):$(id -g)" \
  -v /path/to/media:/media \
  -v /path/to/config:/config \
  OWNER/mediakeeper /media
```

- `-it` is required: without a terminal the dialogs cannot be answered.
- `--user` makes the renamed files and new folders belong to you, not to
  root. The config folder must be writable by that user.
- `/config` keeps the API keys between runs. Instead of `-setup` the keys can
  be passed as variables: `-e OMDB_API_KEY=...`.
- Everything after the image name is MediaKeeper's own arguments, the same as
  without Docker: `-dry-run /media`, `-no-tags /media`, `-undo /media`.
- Host networking is not needed for organizing, only for DLNA.

For scripts and cron there is a mode without questions: unclear files are
skipped and the plan is applied at once.

```sh
docker run --rm --user "$(id -u):$(id -g)" \
  -v /path/to/media:/media -v /path/to/config:/config \
  OWNER/mediakeeper -yes /media
```

## Options

```
mediakeeper [options] [directory]        (directory: the current one by default)
```

| Option          | Meaning                                                                 |
|-----------------|-------------------------------------------------------------------------|
| `-dry-run`      | Show the plan and change nothing                                        |
| `-yes`          | Ask nothing: skip unclear files and apply the plan                      |
| `-undo`         | Revert the last run in this directory; repeat to go further back        |
| `-out DIR`      | Build the library in `DIR` instead of where the files are               |
| `-no-tags`      | Do not write tags into the files                                        |
| `-refresh`      | Identify again even if an `.nfo` is already there                       |
| `-sources LIST` | Comma-separated sources in priority order                               |
| `-lang CODE`    | Language of TMDB titles and descriptions, e.g. `ru-RU` (default `en-US`) |
| `-setup`        | Enter API keys and exit                                                 |
| `-serve`        | Run the media server for the directory instead of organizing it         |
| `-port N`       | With `-serve`: HTTP port (default 8200)                                 |
| `-name NAME`    | With `-serve`: the name clients show (default: the host name)          |
| `-dlna=false`   | With `-serve`: do not be a DLNA server (DLNA has no login)              |
| `-guests=false` | With `-serve`: require a login to watch in the web interface            |
| `-version`      | Print the version                                                       |

## Settings file

`~/.config/mediakeeper/config.yaml` (in Docker: `/config/mediakeeper/config.yaml`).
Every setting is optional, and a flag on the command line wins over the file.
`mediakeeper -setup` writes the file with a comment for every setting;
it can be edited by hand afterwards. A misspelt key is reported instead of
silently ignored. A `config.json` from an earlier version is converted on
the first start and kept as `config.json.old`.

```yaml
tmdb_api_key: "..."
omdb_api_key: "..."
kinopoisk_api_key: "..."
language: ru-RU
sources: [tmdb, tvmaze, omdb, wikidata, imdb]
tmdb_api_url: https://api.themoviedb.org/3
tmdb_image_url: https://image.tmdb.org/t/p/original

server:              # mediakeeper -serve
  name: Living room  # the name clients show; the host name by default
  port: 8200
  dlna: true         # DLNA has no login: the whole local network can watch
  guests: true       # the web interface can be watched without signing in
  no_tags: false     # do not write tags into the files of downloads
```

`tmdb_api_url` and `tmdb_image_url` point MediaKeeper at a mirror where TMDB
itself is not reachable; a proxy from `HTTPS_PROXY` is honoured as well.

Accounts, login tokens and watch progress are not settings: the server keeps
them in `server.json` next to this file.

## Building from source

Go 1.24 or newer, no other dependencies.

```sh
go build -o mediakeeper .      # the web interface (web/) is embedded into the binary
go test .
docker build -t mediakeeper .
```
