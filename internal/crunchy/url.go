package crunchy

import (
	"fmt"
	"strings"
)

// ParseContentURL extracts the content type ("watch" or "series") and the
// content id from a Crunchyroll URL of the form
// "https://www.crunchyroll.com/<type>/<id>[/<slug>]". It validates the id
// length and the content type, returning the same messages processUrl used to
// print. Extracted from processUrl so the server can reuse it and so the
// validation (including the former &&-vs-|| bug) is unit-testable.
func ParseContentURL(url string) (contentType, contentId string, err error) {
	parts := strings.Split(url, "/")
	if len(parts) < 5 {
		return "", "", fmt.Errorf("Invalid URL format: %s", url)
	}
	contentType = parts[3]
	contentId = parts[4]
	if len(contentId) < 9 || len(contentId) > 14 {
		return "", "", fmt.Errorf("Invalid URL format: %s", url)
	}
	if contentType != "watch" && contentType != "series" {
		return "", "", fmt.Errorf("Invalid URL (must be /watch/ or /series/): %s", url)
	}
	return contentType, contentId, nil
}
