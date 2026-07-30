# Crunchy Downloader

Downloads anime from Crunchyroll as `.mkv` or `.mp4`, with multi-language audio
and subtitles, rich metadata, cover art, and a built-in localhost web control
panel.

> **Fork credit:** this project is a heavy fork of
> [CuteTenshii/crunchyroll-downloader](https://github.com/CuteTenshii/crunchyroll-downloader)
> (originally by [Tenshii/CuteTenshii](https://github.com/CuteTenshii) and its
> contributors — see the git history). All of the core Crunchyroll/Widevine
> download machinery originates there. This fork adds a complete embedded web
> UI, a job queue with live progress, format-aware muxing (MKV **and** MP4),
> structured server-side logging, and a programmatic `/api/*` surface. Full
> credit for the downloader foundation goes to the upstream authors; please
> respect their work and Crunchyroll's terms of service.

## Features

- Choose audio and subtitle languages, including multiple of each muxed into a
  single file (first of each is the default track)
- Choose audio and video quality
- Decrypts Widevine DRM (requires: a `.wvd` file, or `client_id.bin` +
  `private_key.pem`)
- Format-aware mux: `.mkv` (ASS subtitles copied, attached cover) or `.mp4`
  (ASS → `mov_text`, embedded cover art) with rich metadata (title, show,
  season/episode, genre, description, rating)
- One job per episode, N concurrent downloads (default 3)
- Per-job cancel / delete / restart, season and series batch downloads
- Parallel segment downloads for speed, retry with backoff on connection errors
- **Web control panel** (`crunchy-server`): a single-binary localhost UI to
  paste your token (auto-detected from your browser cookies), browse a series,
  pick episodes, and watch live download progress over SSE — no Node runtime,
  all assets embedded
- Structured server-side logs covering the full download lifecycle (queued →
  phases → throttled progress → finished, with duration)

## Web control panel

For an easier alternative to the CLI, build and run the server binary:

```sh
go build -o crunchy-server ./cmd/crunchy-server
./crunchy-server            # opens http://127.0.0.1:8080 in your browser
```

It binds to `127.0.0.1` only (single-user; the `etp_rt` cookie is kept in memory
and optionally persisted to `data-dir/config.json` with 0600 — it is never
logged). In Settings you can paste your `etp_rt` manually or let the panel
auto-detect it from your local Chromium/Firefox/Safari cookie store. Browse a
`/series/` URL, pick episodes, and start a download; the Jobs page streams
progress live with a per-phase rail and percentage. Flags: `-addr`,
`-etp-rt`, `-data-dir`, `-debug-manifest`, `-no-browser`. The CLI
(`cmd/crunchyroll-downloader`) remains available and unchanged.

## Requirements

- [FFmpeg](https://www.ffmpeg.org/download.html#get-packages)
- To download Premium-only content, a Crunchyroll Premium account. No, this
  can't be bypassed and a free trial should be enough
- Either a `.wvd` file, or a `client_id.bin` and a `private_key.pem`

## Download

Check the [latest release](https://github.com/byescaleira/crunchy/releases/latest)
and download the binary that matches your OS:

| OS      | Arch  | Server UI                  | CLI                                |
|---------|-------|----------------------------|------------------------------------|
| macOS   | arm64 | `crunchy-server-darwin-arm64`     | `crunchyroll-downloader-darwin-arm64`     |
| macOS   | amd64 | `crunchy-server-darwin-amd64`     | `crunchyroll-downloader-darwin-amd64`     |
| Linux   | amd64 | `crunchy-server-linux-amd64`      | `crunchyroll-downloader-linux-amd64`      |
| Windows | amd64 | `crunchy-server-windows-amd64.exe`| `crunchyroll-downloader-windows-amd64.exe`|
| Windows | arm64 | `crunchy-server-windows-arm64.exe`| `crunchyroll-downloader-windows-arm64.exe`|

## Usage (CLI)

- Open a Terminal/Command prompt in the folder where you downloaded the binary
- Run the program with the options you want:

```shell
Usage of ./crunchyroll-downloader:
  -audio-lang string
        Audio language(s), comma-separated for multiple (e.g. "ja-JP,en-US"). First is the default track (default "ja-JP")
  -audio-quality string
        Audio quality (default "192k")
  -etp-rt string
        The "etp_rt" cookie value of your account
  -format string
        Output container: "mkv" or "mp4" (default "mkv")
  -season int
        Season number. Not used if an episode link is entered
  -subs-lang string
        Subtitle language(s), comma-separated for multiple (e.g. "en-US,es-419"). First is the default track (default "en-US")
  -url string
        URL of the episode/season to download
  -file string
        Path to a text file with one URL per line
  -video-quality string
        Video quality (default "1080p")
```

Ex: to download the first season of *Hell's Paradise* as MP4:
```shell
./crunchyroll-downloader --url https://www.crunchyroll.com/series/GJ0H7Q5ZJ/hells-paradise --season 1 --format mp4 --etp-rt replace_this
```

To download a specific episode:
```shell
./crunchyroll-downloader --url https://www.crunchyroll.com/watch/GE00198973JAJP/dawn-and-confusion --etp-rt replace_this
```

To batch download from a file (one URL per line):
```shell
./crunchyroll-downloader --file list.txt --etp-rt replace_this --subs-lang pt-BR
```

To mux multiple audio tracks and subtitles into one file (first of each is the
default track). If a requested language is missing for an episode, that
episode is skipped:
```shell
./crunchyroll-downloader --url https://www.crunchyroll.com/watch/GE00198973JAJP/dawn-and-confusion --etp-rt replace_this --audio-lang ja-JP,en-US --subs-lang en-US,es-419,de-DE
```

## Building

### Requirements

- [Go](https://go.dev/dl/)
- [templ CLI](https://github.com/a-h/templ) (only if you edit `*.templ` UI files)

### Guide

- Clone this repository
- Open a Terminal/Command prompt in the folder where you cloned the repo
- Run `go build .` (CLI) or `go build ./cmd/crunchy-server` (web panel)
- If you edit UI `*.templ` files, regenerate the committed Go output from
  inside `internal/web/` with `templ generate`, then rebuild

## Help

### How do I get my `etp_rt` cookie?

- Go to https://crunchyroll.com
- Open Developer Tools
- Firefox: *Storage* → *Cookies*; Chrome: *Application* → *Cookies*
- Select the Crunchyroll domain, then copy the `etp_rt` cookie value

The web panel can also auto-detect `etp_rt` from your local browser cookie store
(Chromium, Firefox, Safari) — no need to copy it manually.

![](.github/screenshots/etp-rt-cookie.png)

### What is a `.wvd` file and do I really need one?

Yes, Crunchyroll uses DRM-protected content. This file is used to get a
Widevine license, which gives the keys to decrypt the media.

If you don't have a rooted Android device or are just lazy, search "ready to
use cdms" and you'll find plenty of websites providing those files.

## Credits

- The downloader core, Crunchyroll API client, and Widevine handling come from
  [CuteTenshii/crunchyroll-downloader](https://github.com/CuteTenshii/crunchyroll-downloader)
  and its contributors. This fork builds on their work.
- Web UI, job queue, format-aware mux, structured logging, and the `/api/*`
  surface are added in this fork.

## License

This project is licensed under the MIT License. See [LICENSE.txt](LICENSE.txt)