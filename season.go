package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type SeasonEpisodes struct {
	Data []SeasonEpisode `json:"data"`
}

type SeasonEpisode struct {
	ID                 string        `json:"id"`
	Versions           []*DubVersion `json:"versions"`
	SeasonNumber       int           `json:"season_number"`
	EpisodeNumber      int           `json:"episode_number"`
	SeriesTitle        string        `json:"series_title"`
	AudioLocale        string        `json:"audio_locale"`
	Title              string        `json:"title"`
	AvailabilityStarts string        `json:"availability_starts"`
}

func (c *CrunchyClient) getSeasonEpisodes(contentId string, audio_locale string, sub_locale string) []SeasonEpisode {
	if audio_locale == "" {
		audio_locale = "ja-JP"
	}

	if sub_locale == "" {
		sub_locale = "en-US"
	}

	req, err := c.crunchyRequest(http.MethodGet, fmt.Sprintf("https://www.crunchyroll.com/content/v2/cms/seasons/%s/episodes?preferred_audio_language=%s&locale=%s", contentId, audio_locale, sub_locale), nil, true)
	if err != nil {
		panic(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	var episodes SeasonEpisodes
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(body, &episodes); err != nil {
		panic(err)
	}

	return episodes.Data
}

type Seasons struct {
	Data []Season `json:"data"`
}

type Season struct {
	ID           string `json:"id"`
	SeasonNumber int    `json:"season_number"`
}

func (c *CrunchyClient) getSeasons(contentId string, audioLocale string, subLocale string) []Season {
	if audioLocale == "" {
		audioLocale = "ja-JP"
	}

	if subLocale == "" {
		subLocale = "en-US"
	}

	req, err := c.crunchyRequest(http.MethodGet, fmt.Sprintf("https://www.crunchyroll.com/content/v2/cms/series/%s/seasons?force_locale=&preferred_audio_language=%s&locale=%s", contentId, audioLocale, subLocale), nil, true)
	if err != nil {
		panic(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	var seasons Seasons
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(body, &seasons); err != nil {
		panic(err)
	}

	return seasons.Data
}
