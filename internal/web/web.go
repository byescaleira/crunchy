// Package web holds the embedded control-panel UI: templ pages and components
// (code-generated into *_templ.go and committed, so `go build` needs no codegen
// at build time) plus the compiled static assets (app.css, htmx.min.js) served
// via go:embed so the binary is a single file. The HTTP wiring that drives these
// lives in internal/server.
package web

import (
	"embed"
	"fmt"

	"crunchyroll-downloader/internal/media"
)

// Static is the embedded UI asset tree (app.css, htmx.min.js). It is served by
// the server under /static/.
//
//go:embed static
var Static embed.FS

// statusBadgeClass maps a jobs.Status to the DaisyUI badge class that conveys it.
func statusBadgeClass(s string) string {
	switch s {
	case "queued":
		return "badge-ghost"
	case "downloading":
		return "badge-warning"
	case "muxing":
		return "badge-info"
	case "done":
		return "badge-success"
	case "error":
		return "badge-error"
	case "cancelled":
		return "badge-ghost"
	default:
		return "badge-ghost"
	}
}

// alertClass maps a logical alert kind to a DaisyUI alert class.
func alertClass(kind string) string {
	switch kind {
	case "error":
		return "alert-error"
	case "success":
		return "alert-success"
	case "warning":
		return "alert-warning"
	case "info":
		return "alert-info"
	default:
		return "alert"
	}
}

// DownloadFormOpts is the view-model for the download-options modal. It carries
// the granularity (kind/id), a human summary, the current option values, and the
// pre-checked audio/subtitle locales. It lives in web (not server) to avoid an
// import cycle: the server maps its own DownloadOpts onto this for rendering.
type DownloadFormOpts struct {
	Kind          string   // "episode" | "season" | "series"
	ID            string   // content id of the target
	Summary       string   // headline shown at the top of the modal
	VideoQuality  string   // selected video quality
	AudioQuality  string   // selected audio quality
	Format        string   // "mkv" | "mp4"
	SelectedAudio []string // checked audio locales
	SelectedSubs  []string // checked subtitle locales
	OutputDir     string
}

// commonAudioLocales is the fixed set offered as audio checkboxes in the modal.
// These are the most common Crunchyroll dub locales; the user can check several
// to mux multiple audio tracks into one file.
var commonAudioLocales = []string{
	"ja-JP", "en-US", "en-GB", "pt-BR", "es-ES", "es-419", "de-DE", "fr-FR", "it-IT", "ko-KR", "zh-CN",
}

// commonSubLocales is the fixed set offered as subtitle checkboxes.
var commonSubLocales = []string{
	"en-US", "en-GB", "pt-BR", "es-ES", "es-419", "de-DE", "fr-FR", "it-IT", "ko-KR", "zh-CN",
}

// containsString reports whether ss contains s. Used by the modal to mark a
// locale checkbox checked.
func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// downloadButtonLabel returns the button text for a download granularity.
func downloadButtonLabel(kind string) string {
	switch kind {
	case "season":
		return "Download season"
	case "series":
		return "Download series"
	default:
		return "Download"
	}
}

// formatDuration turns a millisecond duration into a broadcast-log style
// timestamp (M:SS, or H:MM:SS over an hour). 0 → "".
func formatDuration(ms int) string {
	if ms <= 0 {
		return ""
	}
	total := ms / 1000
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// airDate returns the YYYY-MM-DD prefix of an ISO date string, or "".
func airDate(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return ""
}

// audioLocales returns the distinct audio locales available for an episode: the
// primary audio locale first, then each dub version's locale (deduped, order
// preserved).
func audioLocales(ep media.SeasonEpisode) []string {
	seen := map[string]bool{}
	var out []string
	add := func(l string) {
		if l == "" || seen[l] {
			return
		}
		seen[l] = true
		out = append(out, l)
	}
	add(ep.AudioLocale)
	for _, v := range ep.Versions {
		add(v.AudioLocale)
	}
	return out
}
