package crunchy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"crunchyroll-downloader/internal/media"
)

// BrowsePopular fetches the most popular series from the discover/browse
// endpoint. n caps the page size; start is the pagination offset (0 for the
// first page). The endpoint and its response shape are community-documented (not
// official), so this is best-effort: a decode miss returns an error and the
// caller shows a friendly message rather than crashing.
func (c *Client) BrowsePopular(n, start int) ([]media.BrowsePanel, error) {
	u := fmt.Sprintf(
		"https://www.crunchyroll.com/content/v2/discover/browse?n=%d&start=%d&sort_by=popularity&preferred_audio_language=ja-JP&locale=en-US",
		n, start,
	)
	req, err := c.CrunchyRequest(http.MethodGet, u, nil, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var br media.BrowseResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return nil, err
	}
	return br.Data, nil
}

// SearchSeries searches series by title via the discover/search endpoint. The
// response shape is community-documented and varies: Data may be a list of
// typed groups (each with a Type and Items) or a flat list of hits. decodeSearch
// tolerates both, preferring the "series" group.
func (c *Client) SearchSeries(q string, n int) ([]media.SearchHit, error) {
	u := fmt.Sprintf(
		"https://www.crunchyroll.com/content/v2/discover/search?q=%s&type=series&n=%d&start=0&locale=en-US",
		url.QueryEscape(q), n,
	)
	req, err := c.CrunchyRequest(http.MethodGet, u, nil, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return decodeSearch(body)
}

// decodeSearch tolerantly decodes a discover/search body. data is either a list
// of typed groups (each with a Type and Items) or a flat list of hits. It tries
// the flat list first: a groups body's elements lack "id", so they decode to
// hits with empty IDs — the signal to fall back to the groups interpretation,
// where the "series" bucket wins (or the first non-empty group as best-effort).
// An empty array is no results (nil, no error); a non-array shape is an error.
func decodeSearch(body []byte) ([]media.SearchHit, error) {
	var raw struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if len(raw.Data) == 0 || string(raw.Data) == "null" {
		return nil, nil
	}
	// data must be a JSON array (groups or flat hits); a string/object is an
	// unrecognized shape.
	if raw.Data[0] != '[' {
		return nil, fmt.Errorf("search: unrecognized response shape")
	}

	// Flat hit list: a real hit carries an id; a group object does not.
	var hits []media.SearchHit
	if err := json.Unmarshal(raw.Data, &hits); err == nil && len(hits) > 0 && hits[0].ID != "" {
		return hits, nil
	}

	// Typed groups: pick the "series" bucket, else the first non-empty group.
	var groups []media.SearchGroup
	if err := json.Unmarshal(raw.Data, &groups); err == nil {
		for _, g := range groups {
			if g.Type == "series" && len(g.Items) > 0 {
				return g.Items, nil
			}
		}
		for _, g := range groups {
			if len(g.Items) > 0 {
				return g.Items, nil
			}
		}
	}

	// Empty array → no results.
	return nil, nil
}