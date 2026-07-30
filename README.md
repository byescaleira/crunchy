# Crunchy Downloader

A Crunchyroll downloader with a built-in localhost web control panel. Download
anime as `.mkv` or `.mp4` with multiple audio and subtitle languages, rich
metadata, cover art, and live progress — either from a browser UI or the CLI.

Single Go binaries, no runtime dependencies (besides FFmpeg). The web UI is
embedded in the server binary; nothing is served from disk.

> This project is a fork of
> [CuteTenshii/crunchyroll-downloader](https://github.com/CuteTenshii/crunchyroll-downloader).
> Credit for the Crunchyroll client and Widevine decryption machinery goes to
> the upstream authors. The web control panel, job queue, MP4 muxing,
> structured logging, and `/api/*` surface were added in this fork.

## What it does

- **Web control panel** (`crunchy-server`): open `http://127.0.0.1:8080`, paste
  your `etp_rt` token (or auto-detect it from your local browser cookies),
  browse or search a series, pick episodes, and watch downloads progress live
  over SSE with a per-phase rail and percentage.
- **CLI** (`crunchyroll-downloader`): the same downloader from the terminal,
  for single episodes, whole seasons, or a file of URLs.
- **Multi-language mux**: several audio tracks and subtitle tracks in one file;
  the first of each is the default track. Output `.mkv` (ASS subtitles copied,
  cover attached) or `.mp4` (ASS → `mov_text`, cover embedded) with full
  metadata (title, show, season/episode, genre, description, rating).
- **Job queue**: one job per episode, N concurrent downloads (default 3),
  per-job cancel / delete / restart, plus season and series batch downloads.
- **Structured server logs**: the full download lifecycle — queued → phases →
  throttled progress → finished — with status, duration, and tags
  (`job`, `title`, `series`, `ep`).
- **Programmatic API**: a scoped, origin-restricted `/api/*` surface to enqueue
  and inspect downloads from your own tooling.

## Requirements

- **[FFmpeg](https://www.ffmpeg.org/download.html#get-packages)** — muxing.
- **A Crunchyroll account** — Premium-only content needs a Premium account
  (this can't be bypassed; a free trial is enough).
- **A Widevine CDM** — either a `.wvd` file, or `client_id.bin` +
  `private_key.pem`. (Search "ready to use cdms" if you don't have one.)

## Download

Grab a binary from the [latest release](https://github.com/byescaleira/crunchy/releases/latest):

| OS      | Arch  | Web panel                          | CLI                                       |
|---------|-------|------------------------------------|-------------------------------------------|
| macOS   | arm64 | `crunchy-server-darwin-arm64`      | `crunchyroll-downloader-darwin-arm64`     |
| macOS   | amd64 | `crunchy-server-darwin-amd64`      | `crunchyroll-downloader-darwin-amd64`     |
| Linux   | amd64 | `crunchy-server-linux-amd64`       | `crunchyroll-downloader-linux-amd64`      |
| Windows | amd64 | `crunchy-server-windows-amd64.exe` | `crunchyroll-downloader-windows-amd64.exe`|
| Windows | arm64 | `crunchy-server-windows-arm64.exe` | `crunchyroll-downloader-windows-arm64.exe`|

## Web panel

```sh
./crunchy-server            # opens http://127.0.0.1:8080 in your browser
```

It binds to `127.0.0.1` only (single-user). The `etp_rt` cookie is kept in
memory and optionally persisted to `data-dir/config.json` with mode `0600` — it
is never logged. In **Settings** you can paste `etp_rt` manually or let the
panel auto-detect it from your Chromium / Firefox / Safari cookie store. Then
**Browse**: paste a `https://www.crunchyroll.com/series/...` URL or search by
title, drill into a season, pick episodes, and download. The **Jobs** page
streams progress live.

Flags:

```sh
./crunchy-server -h
  -addr string         listen address (default "127.0.0.1:8080")
  -etp-rt string       the "etp_rt" cookie value of your account
  -data-dir string     where the persisted config lives (default ".crunchy-data")
  -debug-manifest      log raw episode playback JSON and manifest XML
  -no-browser          don't open a browser on start
```

## CLI

```sh
./crunchyroll-downloader -h
  -url string          URL of the episode/season to download
  -file string         path to a text file with one URL per line
  -season int          season number (ignored for an episode link)
  -format string       output container: "mkv" or "mp4" (default "mkv")
  -audio-lang string   audio language(s), comma-separated; first is default (default "ja-JP")
  -subs-lang string    subtitle language(s), comma-separated; first is default (default "en-US")
  -video-quality string  video quality (default "1080p")
  -audio-quality string  audio quality (default "192k")
  -etp-rt string        the "etp_rt" cookie value of your account
  -debug-manifest       log raw episode playback JSON and manifest XML
```

First season of *Hell's Paradise* as MP4:
```sh
./crunchyroll-downloader \
  --url https://www.crunchyroll.com/series/GJ0H7Q5ZJ/hells-paradise \
  --season 1 --format mp4 --etp-rt REPLACE_THIS
```

One episode:
```sh
./crunchyroll-downloader \
  --url https://www.crunchyroll.com/watch/GE00198973JAJP/dawn-and-confusion \
  --etp-rt REPLACE_THIS
```

Batch from a file (one URL per line):
```sh
./crunchyroll-downloader --file list.txt --etp-rt REPLACE_THIS --subs-lang pt-BR
```

Multiple audio + subtitle tracks in one file (first of each is default; an
episode missing a requested language is skipped):
```sh
./crunchyroll-downloader \
  --url https://www.crunchyroll.com/watch/GE00198973JAJP/dawn-and-confusion \
  --etp-rt REPLACE_THIS \
  --audio-lang ja-JP,en-US --subs-lang en-US,es-419,de-DE
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

## Building from source

Requirements: [Go](https://go.dev/dl/), and the [templ CLI](https://github.com/a-h/templ)
(only if you edit `*.templ` UI files).

```sh
git clone https://github.com/byescaleira/crunchy.git
cd crunchy
go build -o crunchy-server ./cmd/crunchy-server
go build -o crunchyroll-downloader ./cmd/crunchyroll-downloader
```

If you edit UI `*.templ` files, regenerate the committed Go output **from inside
`internal/web/`** (so paths match the committed convention) then rebuild:

```sh
( cd internal/web && templ generate )
go build ./cmd/crunchy-server
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
media. If you don't have a rooted Android device, search "ready to use cdms"
for sources of these files.

## Credits

- Crunchyroll client, Widevine handling, and the download foundation come from
  [CuteTenshii/crunchyroll-downloader](https://github.com/CuteTenshii/crunchyroll-downloader)
  and its contributors.
- Web control panel, job queue, format-aware muxing, structured logging, and
  the `/api/*` surface are added in this fork.

## License

MIT — see [LICENSE.txt](LICENSE.txt).