// Package media holds the data-transfer objects shared across the crunchy,
// download, and server packages. It has no dependencies on other internal
// packages so the wire-format JSON tags stay in one place.
package media

// Episode is the playback-v3 response: the DASH manifest URL, the available
// subtitle files, the Widevine challenge token, and an error field.
type Episode struct {
	// Dash manifest file URL
	ManifestURL string `json:"url"`
	// List of .ass files
	Subtitles map[string]*Subtitle `json:"subtitles"`
	// Token to give to the Widevine CDM challenge
	Token string `json:"token"`
	// Error, `nil` if there's no error
	Error *string `json:"error"`
}

// Subtitle pairs a subtitle language (e.g. "en-US") with its direct .ass URL.
type Subtitle struct {
	// Language represents a subtitle language in the "en-US" format
	Language string `json:"language"`
	// Direct URL to the .ass file
	URL string `json:"url"`
}

// EpisodeMetadataResponse wraps the list of episode objects returned by the
// CMS objects endpoint.
type EpisodeMetadataResponse struct {
	Data []EpisodeInfo `json:"data"`
}

// EpisodeInfo is one entry from the CMS objects endpoint.
type EpisodeInfo struct {
	EpisodeMetadata EpisodeMetadata `json:"episode_metadata"`
	// Episode title
	Title string `json:"title"`
}

// EpisodeMetadata carries the per-episode metadata used for the output path,
// mux tags, and dub-version resolution.
type EpisodeMetadata struct {
	AudioLocale   string `json:"audio_locale"`
	EpisodeNumber int    `json:"episode_number"`
	SeasonNumber  int    `json:"season_number"`
	SeriesTitle   string `json:"series_title"`
	// AvailabilityStarts represents the date when the episode was released on Crunchyroll
	AvailabilityStarts string        `json:"availability_starts"`
	Versions           []*DubVersion `json:"versions"`
}

// DubVersion is a single audio dub of an episode, identified by locale and GUID.
type DubVersion struct {
	AudioLocale string `json:"audio_locale"`
	GUID        string `json:"guid"`
}

// SeasonEpisodes wraps the list of episodes returned for a season.
type SeasonEpisodes struct {
	Data []SeasonEpisode `json:"data"`
}

// SeasonEpisode is one episode within a season listing.
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

// Seasons wraps the list of seasons returned for a series.
type Seasons struct {
	Data []Season `json:"data"`
}

// Season is one season of a series.
type Season struct {
	ID           string `json:"id"`
	SeasonNumber int    `json:"season_number"`
}
