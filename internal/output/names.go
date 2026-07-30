package output

import "strings"

// TrackTitle returns a human-readable track name for a locale, falling back to
// the raw locale when it isn't in the known list.
func TrackTitle(locale string) string {
	if name, ok := LanguageNames[locale]; ok {
		return name
	}
	return locale
}

// Sanitize replaces characters that are illegal in Windows filenames (or break
// the final path) with underscores, collapses repeated underscores, and trims
// trailing spaces/dots. An empty string becomes "Unknown".
func Sanitize(s string) string {
	if s == "" {
		return "Unknown"
	}

	// Characters that are illegal in Windows filenames or break the final path.
	// The curly quotes are written as \u escapes so the source stays pure ASCII
	// and the literals can't be mangled by editors that "normalize" quotes.
	illegal := []string{
		"\\", "/", ":", "*", "?", "\"", "<", ">", "|",
		"'", "’", "`", "“", "”",
	}
	res := s
	for _, char := range illegal {
		res = strings.ReplaceAll(res, char, "_")
	}
	for strings.Contains(res, "__") {
		res = strings.ReplaceAll(res, "__", "_")
	}
	return strings.TrimRight(res, " .")
}
