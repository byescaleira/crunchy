package crunchy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"crunchyroll-downloader/internal/media"
)

// GetSeasonEpisodes lists the episodes of a season, using the given preferred
// audio and subtitle locales (each defaulting when empty).
func (c *Client) GetSeasonEpisodes(contentId string, audio_locale string, sub_locale string) ([]media.SeasonEpisode, error) {
	if audio_locale == "" {
		audio_locale = "ja-JP"
	}

	if sub_locale == "" {
		sub_locale = "en-US"
	}

	req, err := c.CrunchyRequest(http.MethodGet, fmt.Sprintf("https://www.crunchyroll.com/content/v2/cms/seasons/%s/episodes?preferred_audio_language=%s&locale=%s", contentId, audio_locale, sub_locale), nil, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var episodes media.SeasonEpisodes
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(body, &episodes); err != nil {
		return nil, err
	}

	return episodes.Data, nil
}

// GetSeasons lists the seasons of a series, using the given preferred audio and
// subtitle locales (each defaulting when empty).
func (c *Client) GetSeasons(contentId string, audioLocale string, subLocale string) ([]media.Season, error) {
	if audioLocale == "" {
		audioLocale = "ja-JP"
	}

	if subLocale == "" {
		subLocale = "en-US"
	}

	req, err := c.CrunchyRequest(http.MethodGet, fmt.Sprintf("https://www.crunchyroll.com/content/v2/cms/series/%s/seasons?force_locale=&preferred_audio_language=%s&locale=%s", contentId, audioLocale, subLocale), nil, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var seasons media.Seasons
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(body, &seasons); err != nil {
		return nil, err
	}

	return seasons.Data, nil
}
