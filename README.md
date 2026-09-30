# MediaKeeper

An interactive command-line tool that turns a folder of downloaded movies and
series into a tidy media library, and a DLNA server that shares that library
with TVs and players on the local network.

- Finds each title in online catalogues and renames files and folders the way
  Jellyfin, Kodi and Plex expect.
- Sorts movies into `Movies/` and episodes into `Shows/<Series>/Season NN/`.
- Writes `.nfo` files, downloads posters and backdrops, and writes tags into
  the files themselves.
- Asks when a file name is not enough: pick from a list, type another title,
  or paste an IMDb number or a link.
- Every run can be reverted with `-undo`.

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
- [DLNA server](#dlna-server)
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
and `ffmpeg` (other containers); the DLNA server measures durations with
`ffprobe`. Without them everything else still works.

```sh
sudo apt install mkvtoolnix ffmpeg      # Debian, Ubuntu
brew install mkvtoolnix ffmpeg          # macOS
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

## DLNA server

```sh
mediakeeper -serve /path/to/media
mediakeeper -serve -port 8200 -name "Living room" /path/to/media
```

TVs and players on the same network find the server by themselves. It offers:

- **Movies** — every movie by title, e.g. `Iron Man / Железный человек (2008)`
- **Shows** → series → season → episodes in order
- **Folders** — the directory as it is on disk

Titles, descriptions and artwork come from the `.nfo` and image files, so an
organized library looks best; files that were never organized are served too.
New files appear without a restart. `.srt` subtitles next to a video are
offered to the player. The address printed at start also opens in a browser.

There is no transcoding: files are sent as they are, and a player must
understand the format itself. The firewall has to allow the HTTP port
(8200/tcp by default) and 1900/udp.

## Docker

The image contains MediaKeeper, ffmpeg and mkvtoolnix. By default it runs the
DLNA server for `/media`; settings are kept in `/config`.

[docker-compose.yaml](docker-compose.yaml) is a ready example. Replace `OWNER`
in the image name, check `user` (the owner of your media folder: `id -u`,
`id -g`) and the path to the library, then:

```sh
docker compose up -d        # start the DLNA server
docker compose logs -f      # see what is being played
```

The server needs `network_mode: host`: players discover it by multicast,
which does not pass through Docker's bridge network. This works on Linux;
Docker Desktop on macOS and Windows cannot do it.

### Identifying files with Docker

Organizing is interactive, so it runs in a one-off container with a terminal
attached, not in the background service.

**With the compose file** — the one-off container uses the same volumes, user
and settings as the service:

```sh
docker compose run --rm mediakeeper -setup              # API keys, once (optional)
docker compose run --rm mediakeeper -dry-run /media     # show the plan only
docker compose run --rm mediakeeper /media              # identify and organize
docker compose run --rm mediakeeper -undo /media        # revert the last run
```

The DLNA server may keep running meanwhile; it picks up the new names within
half a minute.

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
- Host networking is not needed for organizing, only for the DLNA server.

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
| `-serve`        | Run the DLNA server for the directory instead of organizing it          |
| `-port N`       | With `-serve`: HTTP port (default 8200)                                 |
| `-name NAME`    | With `-serve`: the name players show                                    |
| `-version`      | Print the version                                                       |

## Settings file

`~/.config/mediakeeper/config.json` (in Docker: `/config/mediakeeper/config.json`).
All fields are optional.

```json
{
  "tmdb_api_key": "...",
  "omdb_api_key": "...",
  "kinopoisk_api_key": "...",
  "language": "ru-RU",
  "sources": ["tmdb", "tvmaze", "omdb", "wikidata", "imdb"],
  "tmdb_api_url": "https://api.themoviedb.org/3",
  "tmdb_image_url": "https://image.tmdb.org/t/p/original"
}
```

`tmdb_api_url` and `tmdb_image_url` point MediaKeeper at a mirror where TMDB
itself is not reachable; a proxy from `HTTPS_PROXY` is honoured as well.

## Building from source

Go 1.22 or newer, no other dependencies.

```sh
go build -o mediakeeper .
go test .
docker build -t mediakeeper .
```
