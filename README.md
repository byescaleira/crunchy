# Crunchy Downloader

A Crunchyroll downloader with a built-in web control panel. Download anime as
`.mkv` or `.mp4` with multiple audio and subtitle languages, rich metadata, cover
art, and live progress — all from a browser UI. One command, one binary you build
yourself.

The web UI is embedded in the binary; nothing is served from disk.

![Crunchy Downloader web panel](.github/screenshots/web-panel.png)

> This project is a fork of
> [CuteTenshii/crunchyroll-downloader](https://github.com/CuteTenshii/crunchyroll-downloader).
> Credit for the Crunchyroll client and Widevine decryption machinery goes to
> the upstream authors. The web control panel, job queue, MP4 muxing,
> structured logging, and `/api/*` surface were added in this fork.

## Build

You need [Go](https://go.dev/dl/) (1.25+) and [FFmpeg](https://www.ffmpeg.org/download.html#get-packages)
on your `PATH`. Then:

```sh
git clone https://github.com/byescaleira/crunchy.git
cd crunchy
go build -o crunchy ./cmd/crunchy
```

If you edit UI `*.templ` files, regenerate the committed Go output **from inside
`internal/web/`** (so paths match the committed convention) then rebuild:

```sh
( cd internal/web && templ generate )
go build -o crunchy ./cmd/crunchy
```

Pure Go / CGO-free: `CGO_ENABLED=0 go build -o crunchy ./cmd/crunchy` works, so
you can cross-compile for any OS/arch from one machine if you want.

## Requirements

- **[Go](https://go.dev/dl/)** 1.25+ — to build.
- **[FFmpeg](https://www.ffmpeg.org/download.html#get-packages)** — muxing
  (ffmpeg + ffprobe on your `PATH`).
- **A Crunchyroll account** — Premium-only content needs a Premium account (this
  can't be bypassed; a free trial is enough).
- **A Widevine CDM** — a `.wvd` file (or `client_id.bin` + `private_key.pem`),
  placed in your data-dir (`~/.crunchy-data` on macOS/Linux,
  `%LOCALAPPDATA%\crunchy-data` on Windows) or the directory you run `crunchy`
  from. (Search "ready to use cdms" if you don't have one.)

## Running

```sh
crunchy            # starts the server and opens the panel in your browser
```

By default `crunchy` binds **all interfaces** and prints the machine's LAN IP
(`http://192.168.x.x:8080`) so a phone on the same network can drive downloads
too. To restrict it to localhost, pass `-addr 127.0.0.1:8080`.

The `etp_rt` cookie is kept in memory and persisted to `data-dir/config.json`
with mode `0600` — it is never logged. In **Settings** you can paste `etp_rt`
manually or let the panel auto-detect it from your Chromium / Firefox / Safari
cookie store. Then **Browse**: paste a `https://www.crunchyroll.com/series/...`
URL or search by title, drill into a season, pick episodes, and download. The
**Jobs** page streams progress live.

Flags:

```sh
crunchy -h
  -addr string         listen address (default "0.0.0.0:8080" = all interfaces / LAN; "127.0.0.1:8080" = localhost only)
  -etp-rt string       the "etp_rt" cookie value of your account (optional; set it in the UI instead)
  -data-dir string     where the persisted config + .wvd live (default "$HOME/.crunchy-data")
  -debug-manifest      log raw episode playback JSON and manifest XML
  -no-browser          don't open a browser on start
```

## API

The server exposes a small JSON surface for programmatic use (CORS is scoped to
`/api/*` and restricted to same-origin / no-origin, so drive-by web pages can't
enqueue downloads):

- `GET  /api/health` — `{ "ok": true, "version": "..." }`
- `POST /api/download` — body `{ kind: "episode"|"season"|"series", id, audio[], subs[], quality, location, format }` → `{ jobId, jobs[] }`
- `GET  /api/jobs/{id}` — current job state (`status`, `phase`, `progress`, `error`, …)

```sh
curl -s http://127.0.0.1:8080/api/health
curl -sX POST http://127.0.0.1:8080/api/download \
  -H 'Content-Type: application/json' \
  -d '{"kind":"episode","id":"GE00198973JAJP","audio":["ja-JP"],"subs":["en-US"],"format":"mp4"}'
```

## Optional: terminal CLI

The same downloader is available as a headless CLI:

```sh
go build -o crunchyroll-downloader ./cmd/crunchyroll-downloader
./crunchyroll-downloader -h
  -url string          URL of the episode/season to download
  -file string        path to a text file with one URL per line
  -season int         season number (ignored for an episode link)
  -format string      output container: "mkv" or "mp4" (default "mkv")
  -audio-lang string   audio language(s), comma-separated; first is default (default "ja-JP")
  -subs-lang string    subtitle language(s), comma-separated; first is default (default "en-US")
  -video-quality string  video quality (default "1080p")
  -audio-quality string  audio quality (default "192k")
  -etp-rt string       the "etp_rt" cookie value of your account
  -debug-manifest      log raw episode playback JSON and manifest XML
```

First season of *Hell's Paradise* as MP4:
```sh
./crunchyroll-downloader \
  --url https://www.crunchyroll.com/series/GJ0H7Q5ZJ/hells-paradise \
  --season 1 --format mp4 --etp-rt REPLACE_THIS
```

Batch from a file (one URL per line):
```sh
./crunchyroll-downloader --file list.txt --etp-rt REPLACE_THIS --subs-lang pt-BR
```

## Help

### How do I get my `etp_rt` cookie?

- Go to https://crunchyroll.com
- Open Developer Tools → Firefox: *Storage* → *Cookies*; Chrome: *Application* → *Cookies*
- Select the Crunchyroll domain, then copy the `etp_rt` cookie value

The web panel can also auto-detect `etp_rt` from your local browser cookie
store, so you usually don't need to copy it manually.

![](.github/screenshots/etp-rt-cookie.png)

### What is a `.wvd` file and do I need one?

Yes. Crunchyroll serves DRM-protected content; the `.wvd` (Widevine device) file
is used to obtain a Widevine license, which yields the keys to decrypt the
media. Drop it in your data-dir (`~/.crunchy-data`) or the directory you run
`crunchy` from. If you don't have a rooted Android device, search "ready to use
cdms" for sources of these files.

## Credits

- Crunchyroll client, Widevine handling, and the download foundation come from
  [CuteTenshii/crunchyroll-downloader](https://github.com/CuteTenshii/crunchyroll-downloader)
  and its contributors.
- Web control panel, job queue, format-aware muxing, structured logging, and
  the `/api/*` surface are added in this fork.

## License

MIT — see [LICENSE.txt](LICENSE.txt).