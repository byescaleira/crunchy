package main

import "testing"

func TestLanguageMaps(t *testing.T) {
	t.Run("every name locale has a code", func(t *testing.T) {
		for locale := range languageNames {
			code, ok := languageCodes[locale]
			if !ok {
				t.Errorf("languageNames has %q but languageCodes does not", locale)
			}
			if code == "" {
				t.Errorf("languageCodes[%q] is empty", locale)
			}
		}
	})

	t.Run("every code locale has a name", func(t *testing.T) {
		for locale := range languageCodes {
			if _, ok := languageNames[locale]; !ok {
				t.Errorf("languageCodes has %q but languageNames does not", locale)
			}
		}
	})

	t.Run("known locales resolve to non-empty names", func(t *testing.T) {
		for _, locale := range []string{"ja-JP", "en-US", "pt-BR", "es-419"} {
			if languageNames[locale] == "" {
				t.Errorf("languageNames[%q] is empty", locale)
			}
		}
	})
}
