# MediaKeeper

An interactive command-line tool that turns a folder of downloaded movies and
series into a tidy media library, and a media server for that library: a web
interface to browse, watch and download, a Jellyfin-compatible API for
Jellyfin apps, and DLNA for TVs.

- Finds each title in online catalogues and renames files and folders the way
  Jellyfin, Kodi and Plex expect.
- Sorts movies into `Movies/` and episodes into `Shows/<Series>/Season NN/`,
  or keeps a library spread over several folders of movies and of series.
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
- [Organizing a library](#organizing-a-library): [several folders](#several-folders)
- [Sources and API keys](#sources-and-api-keys)
- [Media server](#media-server): [users](#users), [web interface](#web-interface), [downloads](#downloads), [Jellyfin apps](#jellyfin-apps), [DLNA](#dlna)
- [Docker](#docker)
  - [Identifying files with Docker](#identifying-files-with-docker)
- [Options](#options)
- [Settings](#settings)
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
- **Described files are left alone.** A video that already has an `.nfo` —
  written by MediaKeeper, Jellyfin, Kodi or by hand, named like the video or
  `movie.nfo` in its folder — is not renamed, looked up or tagged, so a second
  run changes nothing and only new files are organized. A new episode of a
  described series is filed next to the others without rewriting the
  series' `tvshow.nfo`. A release's text "nfo" (ASCII art) does not count.
  `-refresh` redoes everything, identifying from scratch.
- **Search is forgiving.** Release junk in names is ignored, transliterated
  titles (`Myatezh`) are also tried in Cyrillic, long localized titles are
  searched by their parts, and a single file is looked up among series too
  (a miniseries can be kept as a movie).
- **Subtitles** and other files with the same base name follow the video.
- **`-undo`** restores names and folders and removes what the run created.
  Tags written into files cannot be undone — use `-no-tags` while
  experimenting.

### Several folders

A library can be spread over several folders — disks, shares — each holding
movies only, series only, or both:

```sh
mediakeeper /srv/media                                     # both: Movies/ and Shows/ inside
mediakeeper -movies /srv/movies -movies /mnt/disk2/films   # movies only
mediakeeper -movies /srv/movies -shows /srv/series /srv/media
```

The same folders can be chosen once in the web interface, under
**Settings → Library** (see [Settings](#settings)); they are used whenever no
folder is given on the command line, and a folder given by its path alone
keeps the kind chosen there.

- A folder of **movies** gets `Title (Year)/` right inside, a folder of
  **series** `Series (Year)/Season NN/`; neither gets `Movies/` or `Shows/`.
  In a folder of movies, a name that looks like an episode is still a movie.
- **Titles never move between folders.** Each folder is organized on its
  own, with its own undo journal: `-undo` with the same folders reverts the
  last run in each of them.
- The server shows all folders together. New downloads go to the first
  folder of their kind (or the first holding both).
- **The first folder is special:** the downloads in progress and (unless
  [chosen otherwise](#settings)) the screenshots are kept in it. On the
  command line the folders given by their paths come first, then `-movies`
  and `-shows`, so `-serve /media -movies /mnt/disk2` keeps `/media` first.
- Titles keep their identifiers — and with them the watch progress — when
  folders chosen in the settings are added, removed or reordered.

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

Enter them in the web interface (**Settings → Descriptions**), with
`mediakeeper -setup`, or set `TMDB_API_KEY`, `OMDB_API_KEY`,
`KINOPOISK_API_KEY`.

The table is in priority order: the first source with a confident match names
the file. Change the order, or leave sources out, under **Settings →
Descriptions** or with `-sources omdb,tvmaze,wikidata`. A source that has no key, is unreachable or
rejects the key is simply left out of the lists.

When the data is not in Russian, the Russian title is looked up in Wikidata
and stored in the `.nfo` (`<localizedtitle lang="ru">`); the DLNA server shows
it next to the main one.

With `-serve`, `-no-tags`, `-sources` and `-lang` apply to the downloads the
server organizes.

## Media server

```sh
mediakeeper -serve                          # the folders chosen under Settings
mediakeeper -serve /path/to/media
mediakeeper -serve -port 8200 -name "Living room" /path/to/media
mediakeeper -serve /path/to/media -movies /mnt/disk2/films -shows /mnt/disk2/series
```

With [several folders](#several-folders) the catalogue, the Jellyfin apps and
DLNA show them all together. Without a folder on the command line the server
uses the folders chosen in the web interface under **Settings → Library**;
they can be changed there while it runs. Folders given on the command line
win, and the settings page shows them locked.

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

**The first start.** While there is no account, the web interface opens with
a short setup: the administrator's name and password, the library folders
(chosen from the server's folders), the server's name, the language and the
catalogue keys. Everything but the account can be left for later. Whoever
finishes it first becomes the administrator, so do it right after starting
the server. To make the account without the browser, or to reset a forgotten
password, start the server with `MEDIAKEEPER_ADMIN_PASSWORD` (and optionally
`MEDIAKEEPER_ADMIN` for the name, `admin` by default).

More users are added under **Settings → Users**. Accounts, login tokens,
watch progress, ratings, watchlists and the history are kept in a SQLite
database, `mediakeeper.db` (see [Settings](#settings)); passwords are stored
as salted PBKDF2 hashes. A `server.json` of an earlier version is imported
into it on the first start and kept as `server.json.old`.

The server speaks plain HTTP. To reach it from the internet, put it behind a
reverse proxy with TLS.

### Web interface

Open the address printed at start. **Movies** and **Shows** are the catalogue;
the lists above it narrow it down by genre, year, country, actor, director,
writer, studio, collection or your own marks, and put it in another order:
by title, **oldest first** or newest first (by the release date, so a film
series can be watched in order), recently added, best rated or by your
rating. The search in the header narrows the posters further and stays while
the list is sorted or filtered anew, and after a visit to a title.

A movie's **collection** — the film series it belongs to, like *Pirates of the
Caribbean Collection* — comes from TMDB or is set by hand in the edit sheet;
the collection's page lists its parts in the order they came out. Pages of
an actor, director or genre can be sorted the same way.

A title page shows the description in groups — plot, genres, director,
writers, cast, studio, country — and every value is a link: click an actor or
a genre to see everything else in the library that shares it. Its **← Movies**
(or **← Shows**) button — and the browser's Back — returns to the list it was
opened from, with its filters and order, scrolled to that title.

Everyone signed in keeps their own marks, which nobody else sees: a rating
in stars (a click on a star gives the whole star, on its left edge a half;
**×** removes it), **Watch later** for the watchlist, **Favorite**, and a
note, on each title's page. The **My** page gathers them: **Continue** (what
was started), **Watchlist**, **History** (every sitting by day, with the
time really watched — pauses and skipping ahead do not count — and totals
for the month and all time; entries can be removed), **Rated** and
**Favorites**. The catalogue can be narrowed to these (**Mine**) and sorted
by **My rating**; posters show the watchlist bookmark and your rating. A
movie watched to the end leaves the watchlist by itself. When the server
renames a title's files (a fix, filling in from a catalogue), all of this
moves with it. Guests keep nothing.

The player takes the file as it is when the browser can play it
(MP4/WebM/Matroska with H.264, VP9 or AV1 video and AAC, MP3 or Opus audio).
Anything else — AVI, HEVC, AC3 or DTS sound — is converted by `ffmpeg` on the
fly; this costs the server CPU, and at most two conversions run at a time.
Safari (macOS, iPad, iPhone) gets the converted video as HLS, the only
streamed form it plays without downloading everything first; other browsers
get a plain stream. A converted stream is made while it plays, so the
browser does not know its length and its own controls cannot seek (Safari's
full screen calls it a live broadcast): converted video gets the player's
own controls instead — play, the seek slider, sound, picture in picture and
full screen (also a double click, or F), which keeps the controls. They lie
over the picture and fade while it plays; a move of the mouse, a tap or a
key brings them back. A seek ends the viewer's previous
conversion at once, so it never counts against the limit of two.
Progress is remembered per user, and a series continues with the next
episode.

**Subtitles** are chosen in the player's top bar, like the audio track:
text tracks inside the file (SubRip, ASS) and `.srt` files next to it are
shown by the browser over any stream — the server takes the tracks out of
the file once and keeps them in the cache. Picture subtitles (Blu-ray PGS,
DVD) cannot be drawn by browsers: choosing one switches to a converted
stream with the subtitles burned into the picture. The player remembers the
language chosen and picks it again for the next video that has it as text.

#### Hardware conversion

Converting HEVC, 10-bit, interlaced or old formats is the heaviest work the
server does. With `hwaccel` it is done on a graphics card:

| Value | Card |
|---|---|
| `none` (default) | the processor (libx264) |
| `auto` | the first of the below that works |
| `vaapi` (or `vaapi:/dev/dri/renderD129`) | Intel or AMD, through VA-API |
| `qsv` | Intel Quick Sync |
| `nvenc` | NVIDIA (needs an ffmpeg built with NVENC) |

Choose it under **Settings → Server**, with `-hwaccel` or with
`MEDIAKEEPER_HWACCEL`. At the start the server converts a second of a test
picture on the card; only if that works is the card used, and the log says
which. Decoding, deinterlacing, scaling and encoding all stay on the card; a
file the card fails on is converted on the processor from then on. Copied
H.264 needs no conversion at all, card or not.

The Docker image contains the Intel drivers (x86-64). Pass the card to the
container (`devices: ["/dev/dri:/dev/dri"]` and the host's `render` group,
see [docker-compose.yaml](docker-compose.yaml)). For AMD build the image with
`--build-arg EXTRA_PACKAGES=mesa-va-gallium`; Alpine's ffmpeg has no NVENC, so
for NVIDIA run the binary on the host with an ffmpeg that has it. In a
virtual machine the card has to be passed through to it.

A movie's page shows eight screenshots, taken by `ffmpeg` in the
background between 10% and 90% of the film (so not the logos or the
credits), each the most characteristic of the frames around its moment.
They are kept in the hidden folder `.cache/screenshots` of the library,
which media centers ignore; background work pauses while somebody watches
a converted video.

An episode without a still of its own (`-thumb.jpg` next to it, which
catalogues like TVMaze usually provide) gets a frame taken a third of the
way in, kept in `.cache/stills`; the Jellyfin apps get it too. Until it is
taken, the list shows the series backdrop, or the whole poster when there
is no backdrop. A still uploaded in the edit sheet replaces the frame.

An administrator edits a movie with the small **✎ Edit** button in the
corner of its page:

- **Description** — title, original and Russian titles, year, release date,
  age rating, rating, tagline, plot, genres, directors, writers, studios,
  countries and cast. Only these elements of the `.nfo` are replaced;
  everything else in it stays. The title, date, plot and genres are also
  written into the tags of the video file (in the background: an AVI or MP4
  is remuxed, which takes a while). **Fill in from a catalogue…** searches
  all sources (or takes an IMDb number or a link); the chosen entry fills
  in the fields, which can still be changed before saving. Saving then also
  records the catalogue numbers, can take its poster and backdrop, and —
  with **Rename the files after the title and year** — files the movie
  under its new name (revertible with `mediakeeper -undo`).
- **Images** — a new poster or backdrop (JPEG or PNG). The poster is also
  embedded into MKV and MP4 files as their cover.

The **Images** tab also has **Regenerate screenshots**: new frames from
other moments, when the first ones came out badly.

A series is edited the same way:

- **Description** — title, original and Russian titles, year, first aired
  date, status, age rating, rating, plot, genres, studios and cast, written
  into `tvshow.nfo`. A new title, genres or catalogue numbers also go into
  the tags of every episode's file. **Fill in from a catalogue…** fills in
  the fields from the chosen entry; with **Rename the files, and take the
  titles, descriptions and stills of the episodes** the series is filed
  anew: the folder and every episode are renamed, the episodes get their
  descriptions from the catalogue (revertible with `mediakeeper -undo`).
  Episodes lying among other files, with no folder of their own, can only
  be described this way: filing them puts them into one.
- **Images** — the poster, the backdrop, and a poster for each season.
- **Episodes** — the title, date and plot of each episode, season by
  season, and a new still. Each episode is saved on its own, into its
  `.nfo` and the tags of its file.

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
moved into the library with its `.nfo` and artwork: into the first folder of
movies or of series, or into `Movies/` or `Shows/` of the first folder that
holds both. When the
program is not sure, the download is marked **Needs you**: the page shows the
candidates from all sources, lets you search by another title or paste an
IMDb number or a link, say that a series file is a single episode or the
whole series, or delete the files. Downloads are kept in the hidden folder
`.incoming` inside the (first) library folder until they are filed.

While a download is still running, **Say what it is…** lets you pick the
title in advance (search, or an IMDb number or link): when the download
finishes it is filed as that, without guessing from the file name. A
finished download shows what it became, **In the library**, with links to
the titles' pages — they stay right after a title is corrected and
renamed — and **Wrong? Fix…**, which opens the edit sheet already searching
the catalogues.

### Jellyfin apps

Add the server's address (`http://host:8200`) in the app and sign in with a
MediaKeeper user. What is implemented: sign-in, the Movies and Shows
libraries, search, series/seasons/episodes, artwork, direct play with
seeking or converted playback (HLS) for players that need it, subtitles,
watched state, resume and "next up".

What to expect:

- **Subtitles.** Text tracks inside the file and `.srt` files next to it are
  also offered as files of their own (players of a converted stream need
  them that way). Picture subtitles (PGS) that the app's player cannot draw
  itself — its device profile says `Encode`, like the Apple TV's own player —
  are burned into a converted stream when chosen.
- **Played as it is, or converted.** An app tells the server which
  containers and codecs its player takes (its device profile). A file it
  takes is sent as it is; any other — Matroska for the Apple TV's own player,
  say — is converted on the fly to HLS (H.264, copied when it already is,
  and AAC). The playlist lists the whole film from the start, so the player
  can seek anywhere; segments are made as they are asked for, and a jump
  starts the conversion anew from there. A copied track can only be cut at
  its key frames, which Matroska files list in their index (read in a few
  milliseconds); a file without one gets a playlist that grows as the
  conversion goes, with seeking only within what is done. The players an app hands
  the address to (the system player, VLC) bring no login: the play session
  the server gave the app is in the address, and opens that video only.
  `-debug` logs every request of the apps and what the server decided.
- **Native apps only.** Apps that are a shell around the server's own web
  page — the official *Jellyfin for Android/iOS* and *Jellyfin Media Player*
  — do not work: MediaKeeper does not ship Jellyfin's web client. Apps with
  their own interface (Jellyfin for Android TV, Swiftfin, Findroid, Infuse,
  Jellyfin for Kodi and the like) are the ones this is meant for.
- **A subset of the API.** The server reports itself as Jellyfin 12.0.0 —
  Swiftfin and other current apps refuse anything older (Jellyfin dropped the
  "10." from its version numbers with 12.0). Answers were compared field by
  field with a real Jellyfin 10.10 server; login tokens are accepted in every
  way clients send them, including those Jellyfin 12 switched off by
  default, which Swiftfin on Apple TV still uses. A request the server does
  not know is written to its log once as `Jellyfin API: not supported: …` —
  that line is what to report.

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
runs the server; the settings and accounts are kept in `/config`.

[docker-compose.yaml](docker-compose.yaml) is a ready example. Replace `OWNER`
in the image name, check `user` (the owner of your media folder: `id -u`,
`id -g`) and the path to the library, then:

```sh
mkdir -p config && sudo chown 1000:1000 config   # once: writable by the container's user
docker compose up -d        # start the server
docker compose logs -f      # see who signs in and what is being played
```

Then open `http://<host>:8200/`: the [first start](#users) asks for the
administrator and the library folders — the folders as the container sees
them, `/media` in the example.

**Upgrading** from a version that kept its settings in `config.yaml`: they
are taken into the database at the first start, and so are the library
folders named in the command (`["-serve", "/media"]` of older compose files).
Folders in the command stay locked in the settings page; once the server has
started with them, they can be taken out of the command (`["-serve"]`) and
changed in the web interface.

The container runs as `user`, and everything it writes — settings, the
database, downloads, the cache, renamed files — must be writable by that
user. A mounted folder that does not exist yet is created by Docker as
root, which the container then cannot write to; the server says so at the
start (`cannot keep the database … must be writable by the user the server
runs as (uid 1000…)`). Create the folders first, or `chown` them.

The example uses `network_mode: host`. DLNA needs it: players discover the
server by multicast, which does not pass through Docker's bridge network
(this works on Linux; Docker Desktop on macOS and Windows cannot do it).
Without DLNA (`-dlna=false`) an ordinary port mapping `8200:8200` is enough.

Several library folders are mounted as several volumes and then chosen under
**Settings → Library**, by their paths inside the container:

```yaml
    volumes:
      - ./media:/media
      - /mnt/disk2/films:/films
      - /mnt/disk2/series:/series
      - ./config:/config
```

Organizing them by hand is then `docker compose run --rm mediakeeper /media
/films /series` (a folder named by its path keeps the kind chosen for it in
the settings); `-dry-run` and `-undo` without folders take all the folders of
the settings.

### Identifying files with Docker

New titles are easiest to add on the Downloads page of the web interface.
For files that are already on disk, organizing is done on the command line;
it is interactive, so it runs in a one-off container with a terminal
attached, not in the background service.

**With the compose file** — the one-off container uses the same volumes, user
and settings as the service:

```sh
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
- `/config` keeps the settings — the API keys among them — between runs.
  The keys can also be passed as variables: `-e OMDB_API_KEY=...`.
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
mediakeeper [options] [directory ...]
```

Without folders, the library folders of the [settings](#settings) are used,
else the current directory.

| Option          | Meaning                                                                 |
|-----------------|-------------------------------------------------------------------------|
| `-dry-run`      | Show the plan and change nothing                                        |
| `-yes`          | Ask nothing: skip unclear files and apply the plan                      |
| `-movies DIR`   | A library folder of movies only; may be repeated                        |
| `-shows DIR`    | A library folder of series only; may be repeated                        |
| `-undo`         | Revert the last run in each folder; repeat to go further back           |
| `-out DIR`      | Build the library in `DIR` instead of where the files are               |
| `-no-tags`      | Do not write tags into the files                                        |
| `-refresh`      | Also redo videos that already have an `.nfo`, identifying them from scratch |
| `-sources LIST` | Comma-separated sources in priority order                               |
| `-lang CODE`    | Language of TMDB titles and descriptions, e.g. `ru-RU` (default `en-US`) |
| `-setup`        | Enter API keys in the terminal, save them and exit                      |
| `-debug`        | With `-serve`: log every request of the Jellyfin apps, and in full (device profiles, answers, video ranges; no tokens) in `jellyfin-debug.log` next to `config.yaml` |
| `-hwaccel WAY`  | With `-serve`: convert video on a graphics card: `auto`, `vaapi`, `qsv`, `nvenc` or `none` (default) |
| `-cache DIR`    | With `-serve`: the folder of screenshots and episode stills (default: `.cache` in the first library folder) |
| `-db FILE`      | The database of settings, accounts, ratings, watchlists and history (default: `mediakeeper.db` next to `config.yaml`) |
| `-config FILE`  | Where `config.yaml` is, or a folder for it (default: next to the program) |
| `-serve`        | Run the media server for the folders instead of organizing them         |
| `-port N`       | With `-serve`: HTTP port (default 8200)                                 |
| `-name NAME`    | With `-serve`: the name clients show (default: the host name)          |
| `-dlna=false`   | With `-serve`: do not be a DLNA server (DLNA has no login)              |
| `-guests=false` | With `-serve`: require a login to watch in the web interface            |
| `-version`      | Print the version                                                       |

## Settings

The settings are kept in the database, `mediakeeper.db`, together with the
accounts, and are changed in the web interface under **Settings**
(administrators only):

| Tab | What |
|---|---|
| Library | the library folders, chosen from the server's folders, and what each holds |
| Descriptions | the API keys, the language, the sources and their order, a TMDB mirror |
| Server | the name, the port, watching without signing in, DLNA, tags in downloaded files, hardware conversion, the folder of screenshots |
| Users | the accounts and their roles |

Most changes take effect at once — new library folders show up in the
catalogue right away. The port and switching DLNA on or off take a restart;
the page says so. `mediakeeper -setup` enters the API keys in a terminal.

**Flags and variables win.** A setting given on the command line (`-port`,
`-name`, folders, `-lang`, `-sources`, …) or in the environment
(`TMDB_API_KEY`, `MEDIAKEEPER_HWACCEL`, …) is used instead of the saved one,
and the settings page shows it locked, with what sets it.

**Where things are.** The database is `mediakeeper.db` next to `config.yaml`,
which is next to the program. Where the program's folder cannot be written
to (`/usr/local/bin`, a package manager's folder), they are kept in
`~/.config/mediakeeper/` instead; in Docker in `/config/mediakeeper/`.

| What | Flag | Environment variable | In `config.yaml` | Default |
|---|---|---|---|---|
| `config.yaml` | `-config` | `MEDIAKEEPER_CONFIG` | — | next to the program |
| the database | `-db` | `MEDIAKEEPER_DB` | `server.database` | `mediakeeper.db` next to `config.yaml` |
| screenshots and episode stills | `-cache` | `MEDIAKEEPER_CACHE` | — (Settings → Server) | `.cache` in the first library folder |

Each can be given as a folder too (`config.yaml`, `mediakeeper.db` in it).
Relative paths are relative to the folder of `config.yaml`. Moving the cache
is harmless: the images are simply taken again.

**`config.yaml`** now only says where the database is. Settings written into
it — by hand, or by an earlier version — are taken into the database at the
next start; the file is then cleared and the old one kept as
`config.yaml.old`. The keys are the ones earlier versions used:

```yaml
tmdb_api_key: "..."
omdb_api_key: "..."
kinopoisk_api_key: "..."
language: ru-RU
sources: [tmdb, tvmaze, omdb, wikidata, imdb]
tmdb_api_url: https://api.themoviedb.org/3
tmdb_image_url: https://image.tmdb.org/t/p/original

libraries:
  - /srv/media       # a plain path: both, in Movies/ and Shows/ inside
  - path: /mnt/disk2/films
    kind: movies     # movies only
  - path: /mnt/disk2/series
    kind: shows      # series only

server:
  name: Living room
  port: 8200
  dlna: true
  guests: true
  no_tags: false
  hwaccel: auto
  cache: /var/cache/mediakeeper
  database: /var/lib/mediakeeper/mediakeeper.db   # stays in the file
```

A `config.json` or a `server.json` of older versions, and settings an
earlier version kept in `~/.config/mediakeeper/`, are taken over on the
first start as well (stop a running server before that).

`tmdb_api_url` and `tmdb_image_url` point MediaKeeper at a mirror where TMDB
itself is not reachable; a proxy from `HTTPS_PROXY` is honoured as well.

The database is written as things happen, so nothing is lost when the server
stops abruptly. To back it up while the server runs, use
`sqlite3 mediakeeper.db ".backup copy.db"` rather than copying the file.

## Building from source

Go 1.24 or newer; no C compiler is needed (SQLite comes as a pure Go package).
How the program is put together is described in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

```sh
go build -o mediakeeper .      # the web interface (web/) is embedded into the binary
go test .
docker build -t mediakeeper .
```
