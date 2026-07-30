package crunchy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"crunchyroll-downloader/internal/media"
)

// GetSeries fetches the CMS series metadata (synopsis, launch year, categories,
// audio/subtitle locales, poster_tall/poster_wide art) for a series id. The
// series call is the source of art and genre-ish categories, since season-level
// images are empty on the wire.
func (c *Client) GetSeries(id string) (media.Series, error) {
	req, err := c.CrunchyRequest(http.MethodGet, fmt.Sprintf("https://www.crunchyroll.com/content/v2/cms/series/%s?preferred_audio_language=ja-JP&locale=en-US", id), nil, true)
	if err != nil {
		return media.Series{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return media.Series{}, err
	}
	defer resp.Body.Close()

	var sr media.SeriesResponse
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return media.Series{}, err
	}
	if err = json.Unmarshal(body, &sr); err != nil {
		return media.Series{}, err
	}
	if len(sr.Data) == 0 {
		return media.Series{}, fmt.Errorf("no series returned for %s", id)
	}
	return sr.Data[0], nil
}

// DownloadImage fetches an arbitrary image URL (a CMS poster/thumbnail source)
// into a temp .jpg and returns its path. The caller removes the file after
// muxing. Any failure returns ("", nil): a cover is best-effort and must never
// fail a download.
func (c *Client) DownloadImage(url string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", nil
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.Doer.Do(req)
	if err != nil {
		return "", nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil
	}

	f, err := os.CreateTemp("", "crunchy-cover-*.jpg")
	if err != nil {
		return "", nil
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(f.Name())
		return "", nil
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", nil
	}
	return f.Name(), nil
}
