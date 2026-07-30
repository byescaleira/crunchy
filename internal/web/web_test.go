package web

import (
	"strings"
	"testing"

	"crunchyroll-downloader/internal/media"
)

func TestStatusBadgeClass(t *testing.T) {
	cases := map[string]string{
		"queued":      "badge-ghost",
		"downloading": "badge-warning",
		"muxing":      "badge-info",
		"done":        "badge-success",
		"error":       "badge-error",
		"unknown":     "badge-ghost",
	}
	for in, want := range cases {
		if got := statusBadgeClass(in); got != want {
			t.Errorf("statusBadgeClass(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAlertClass(t *testing.T) {
	if got := alertClass("error"); got != "alert-error" {
		t.Errorf("alertClass(error) = %q, want alert-error", got)
	}
	if got := alertClass("success"); got != "alert-success" {
		t.Errorf("alertClass(success) = %q, want alert-success", got)
	}
	if got := alertClass("nonsense"); got != "alert" {
		t.Errorf("alertClass(nonsense) = %q, want alert", got)
	}
}

func TestAudioLocales(t *testing.T) {
	ep := media.SeasonEpisode{
		AudioLocale: "ja-JP",
		Versions: []*media.DubVersion{
			{AudioLocale: "en-US", GUID: "g1"},
			{AudioLocale: "ja-JP", GUID: "g2"}, // dup of primary
			{AudioLocale: "es-419", GUID: "g3"},
		},
	}
	got := audioLocales(ep)
	want := []string{"ja-JP", "en-US", "es-419"}
	if len(got) != len(want) {
		t.Fatalf("audioLocales = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("audioLocales[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestAudioLocales_Empty(t *testing.T) {
	if got := audioLocales(media.SeasonEpisode{}); len(got) != 0 {
		t.Errorf("audioLocales(empty) = %v, want empty", got)
	}
}

// TestStaticEmbedded pins that the compiled assets are embedded under static/ so
// the single-binary server ships with its CSS and htmx.
func TestStaticEmbedded(t *testing.T) {
	for _, name := range []string{"static/app.css", "static/htmx.min.js"} {
		b, err := Static.ReadFile(name)
		if err != nil {
			t.Errorf("Static.ReadFile(%q) error: %v", name, err)
			continue
		}
		if len(b) == 0 {
			t.Errorf("Static.ReadFile(%q) returned empty file", name)
		}
		if name == "static/app.css" && !strings.Contains(string(b), "daisyui") {
			t.Errorf("app.css does not contain daisyUI output; build pipeline may have changed")
		}
		if name == "static/htmx.min.js" && !strings.Contains(string(b), "htmx") {
			t.Errorf("htmx.min.js does not contain htmx; wrong file?")
		}
	}
}
