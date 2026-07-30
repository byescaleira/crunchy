package output

import (
	"testing"
)

func TestTrackTitle(t *testing.T) {
	tests := []struct {
		locale, want string
	}{
		{"ja-JP", "日本語"},
		{"en-US", "English"},
		{"pt-BR", "Português (Brasil)"},
		{"xx-ZZ", "xx-ZZ"}, // unknown locale falls back to raw
		{"", ""},
	}
	for _, tc := range tests {
		got := TrackTitle(tc.locale)
		if got != tc.want {
			t.Errorf("TrackTitle(%q) = %q, want %q", tc.locale, got, tc.want)
		}
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "Unknown"},
		{"plain", "plain"},
		{`a\b/c:d*e?f<g>h|i`, "a_b_c_d_e_f_g_h_i"},
		{`"q"`, "_q_"},         // straight double quotes both replaced
		{`“smart”`, "_smart_"}, // curly double quotes both replaced
		{`'apos'`, "_apos_"},   // straight apostrophes replaced
		{"trailing space  ", "trailing space"},
		{"trailing dot...", "trailing dot"}, // dots are not illegal, but trimmed
		{"double__underscore", "double_underscore"},
		{"many____underscores", "many_underscores"},
		{"a__b___c", "a_b_c"},
		{"  leading kept  ", "  leading kept"}, // TrimRight keeps leading spaces
	}
	for _, tc := range tests {
		got := Sanitize(tc.in)
		if got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLanguageMaps(t *testing.T) {
	t.Run("every name locale has a code", func(t *testing.T) {
		for locale := range LanguageNames {
			code, ok := LanguageCodes[locale]
			if !ok {
				t.Errorf("LanguageNames has %q but LanguageCodes does not", locale)
			}
			if code == "" {
				t.Errorf("LanguageCodes[%q] is empty", locale)
			}
		}
	})

	t.Run("every code locale has a name", func(t *testing.T) {
		for locale := range LanguageCodes {
			if _, ok := LanguageNames[locale]; !ok {
				t.Errorf("LanguageCodes has %q but LanguageNames does not", locale)
			}
		}
	})

	t.Run("known locales resolve to non-empty names", func(t *testing.T) {
		for _, locale := range []string{"ja-JP", "en-US", "pt-BR", "es-419"} {
			if LanguageNames[locale] == "" {
				t.Errorf("LanguageNames[%q] is empty", locale)
			}
		}
	})
}
