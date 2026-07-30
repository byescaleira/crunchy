package output

import (
	"fmt"
	"strings"
)

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

// BuildEpisodeFilename builds the output filename for one episode in the form
// "<season>.<episode> <title> (<quality>).<ext>", e.g. "01.05 Pilot (1080p).mkv".
// season and episode are zero-padded to two digits; title and quality are
// sanitized for the filesystem; ext must include the leading dot. The series
// title is the parent directory, not part of this filename.
func BuildEpisodeFilename(season, episode int, title, quality, ext string) string {
	return fmt.Sprintf("%02d.%02d %s (%s)%s", season, episode, Sanitize(title), Sanitize(quality), ext)
}
