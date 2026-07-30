package crunchy

import (
	"fmt"
	"io"
	"net/http"

	"github.com/unki2aut/go-mpd"

	"crunchyroll-downloader/internal/manifest"
)

// ParseManifest fetches and decodes the DASH manifest at url.
func (c *Client) ParseManifest(url string) (*mpd.MPD, error) {
	req, err := c.CrunchyRequest(http.MethodGet, url, nil, true)
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
	m, err := manifest.ParseMPD(body)
	if err != nil {
		return nil, err
	}

	if c.Debug {
		fmt.Printf("\n%s\n", string(body))
	}

	return m, nil
}
