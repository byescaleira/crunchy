package crunchy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"crunchyroll-downloader/internal/media"
)

// GetEpisode fetches the playback-v3 stream metadata (manifest URL, subtitles,
// Widevine token) for an episode id.
func (c *Client) GetEpisode(id string) media.Episode {
	req, err := c.CrunchyRequest(http.MethodGet, fmt.Sprintf("https://www.crunchyroll.com/playback/v3/%s/web/firefox/play", id), nil, true)
	if err != nil {
		panic(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	var episode media.Episode
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(body, &episode); err != nil {
		panic(err)
	}
	if episode.Error != nil {
		fmt.Println("Error:", *episode.Error)
		os.Exit(1)
	}

	if c.Debug {
		fmt.Printf("\n%s\n", string(body))
	}

	return episode
}

// GetEpisodeInfo fetches the CMS object metadata (title, season/episode
// numbers, audio locale, dub versions) for an episode id.
func (c *Client) GetEpisodeInfo(id string) media.EpisodeInfo {
	req, err := c.CrunchyRequest(http.MethodGet, fmt.Sprintf("https://www.crunchyroll.com/content/v2/cms/objects/%s?ratings=true&preferred_audio_language=ja-JP&locale=en-US", id), nil, true)
	if err != nil {
		panic(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	var info media.EpisodeMetadataResponse
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(body, &info); err != nil {
		panic(err)
	}

	return info.Data[0]
}

// DeleteStream removes the stream to make Crunchyroll think we "left" the playback.
func (c *Client) DeleteStream(contentId, sToken string) bool {
	req, err := c.CrunchyRequest(http.MethodDelete, fmt.Sprintf("https://www.crunchyroll.com/playback/v1/token/%s/%s", contentId, sToken), nil, true)
	if err != nil {
		panic(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		panic(err)
	}

	return resp.StatusCode == http.StatusNoContent
}
