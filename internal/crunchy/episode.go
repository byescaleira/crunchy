package crunchy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"crunchyroll-downloader/internal/media"
)

// GetEpisode fetches the playback-v3 stream metadata (manifest URL, subtitles,
// Widevine token) for an episode id. A non-nil Error field in the response is
// returned as an error instead of aborting the process, so the server can
// surface it and the CLI can continue to the next URL.
func (c *Client) GetEpisode(id string) (media.Episode, error) {
	req, err := c.CrunchyRequest(http.MethodGet, fmt.Sprintf("https://www.crunchyroll.com/playback/v3/%s/web/firefox/play", id), nil, true)
	if err != nil {
		return media.Episode{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return media.Episode{}, err
	}
	defer resp.Body.Close()

	var episode media.Episode
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return media.Episode{}, err
	}
	if err = json.Unmarshal(body, &episode); err != nil {
		return media.Episode{}, err
	}
	if episode.Error != nil {
		return media.Episode{}, fmt.Errorf("%s", *episode.Error)
	}

	if c.Debug {
		fmt.Printf("\n%s\n", string(body))
	}

	return episode, nil
}

// GetEpisodeInfo fetches the CMS object metadata (title, season/episode
// numbers, audio locale, dub versions) for an episode id. An empty response
// returns an error instead of panicking on info.Data[0].
func (c *Client) GetEpisodeInfo(id string) (media.EpisodeInfo, error) {
	req, err := c.CrunchyRequest(http.MethodGet, fmt.Sprintf("https://www.crunchyroll.com/content/v2/cms/objects/%s?ratings=true&preferred_audio_language=ja-JP&locale=en-US", id), nil, true)
	if err != nil {
		return media.EpisodeInfo{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return media.EpisodeInfo{}, err
	}
	defer resp.Body.Close()

	var info media.EpisodeMetadataResponse
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return media.EpisodeInfo{}, err
	}
	if err = json.Unmarshal(body, &info); err != nil {
		return media.EpisodeInfo{}, err
	}
	if len(info.Data) == 0 {
		return media.EpisodeInfo{}, fmt.Errorf("no episode info returned for %s", id)
	}

	return info.Data[0], nil
}

// DeleteStream removes the stream to make Crunchyroll think we "left" the
// playback. It reports whether Crunchyroll acknowledged (204) and any transport
// error; callers treat it as best-effort cleanup.
func (c *Client) DeleteStream(contentId, sToken string) (bool, error) {
	req, err := c.CrunchyRequest(http.MethodDelete, fmt.Sprintf("https://www.crunchyroll.com/playback/v1/token/%s/%s", contentId, sToken), nil, true)
	if err != nil {
		return false, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return false, err
	}

	return resp.StatusCode == http.StatusNoContent, nil
}
