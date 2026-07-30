// Package web holds the embedded control-panel UI: templ pages and components
// (code-generated into *_templ.go and committed, so `go build` needs no codegen
// at build time) plus the compiled static assets (app.css, htmx.min.js) served
// via go:embed so the binary is a single file. The HTTP wiring that drives these
// lives in internal/server.
package web

import (
	"embed"

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
