// Package media holds the data-transfer objects shared across the crunchy,
// download, and server packages. It has no dependencies on other internal
// packages so the wire-format JSON tags stay in one place.
package media

// Image is one entry in a CMS image collection. The wire key for the MIME type
// is "type" (a JSON keyword in some languages, but a plain tag in Go). Image
// collections are nested arrays [[Image, ...], ...]; sizes are sorted ascending
// within each inner array, so the last element of the first inner array is the
// highest-resolution variant (see BestImage).
type Image struct {
	Source string `json:"source"`
	Type   string `json:"type"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Images bundles the CMS image collections. poster_tall is portrait (2:3) cover
// art, poster_wide is landscape (16:9) key art, thumbnail is the episode still.
// Not every object populates every collection; callers must nil-check.
type Images struct {
	Thumbnail  [][]Image `json:"thumbnail"`
	PosterTall [][]Image `json:"poster_tall"`
	PosterWide [][]Image `json:"poster_wide"`
}

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

// EpisodeInfo is one entry from the CMS objects endpoint. The top-level fields
// (Title, Description, Slug) live directly on the data item; the rest are nested
// under episode_metadata.
type EpisodeInfo struct {
	EpisodeMetadata EpisodeMetadata `json:"episode_metadata"`
	// Episode title
	Title string `json:"title"`
	// Episode synopsis (top-level on the data item, not under episode_metadata)
	Description string `json:"description"`
	// URL slug of the episode
	Slug string `json:"slug"`
}

// EpisodeMetadata carries the per-episode metadata used for the output path,
// mux tags, dub-version resolution, and UI display.
type EpisodeMetadata struct {
	AudioLocale   string `json:"audio_locale"`
	EpisodeNumber int    `json:"episode_number"`
	SeasonNumber  int    `json:"season_number"`
	SeriesTitle   string `json:"series_title"`
	// AvailabilityStarts represents the date when the episode was released on Crunchyroll
	AvailabilityStarts string        `json:"availability_starts"`
	Versions           []*DubVersion `json:"versions"`

	// Display + mux fields (populated from the CMS objects response).
	DurationMS       int      `json:"duration_ms"`
	EpisodeAirDate   string   `json:"episode_air_date"`
	AvailabilityEnds string   `json:"availability_ends"`
	IsPremiumOnly    bool     `json:"is_premium_only"`
	MaturityRatings  []string `json:"maturity_ratings"`
	SubtitleLocales  []string `json:"subtitle_locales"`
	SeasonID         string   `json:"season_id"`
	SeasonTitle      string   `json:"season_title"`
	SeriesID         string   `json:"series_id"`
	SequenceNumber   float64  `json:"sequence_number"`
	Images           Images   `json:"images"`
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

// SeasonEpisode is one episode within a season listing. It carries the same
// display-relevant metadata shape as EpisodeMetadata so the UI can render rich
// rows without a second objects lookup.
type SeasonEpisode struct {
	ID                 string        `json:"id"`
	Versions           []*DubVersion `json:"versions"`
	SeasonNumber       int           `json:"season_number"`
	EpisodeNumber      int           `json:"episode_number"`
	SeriesTitle        string        `json:"series_title"`
	AudioLocale        string        `json:"audio_locale"`
	Title              string        `json:"title"`
	AvailabilityStarts string        `json:"availability_starts"`

	Description     string   `json:"description"`
	DurationMS      int      `json:"duration_ms"`
	EpisodeAirDate  string   `json:"episode_air_date"`
	IsPremiumOnly   bool     `json:"is_premium_only"`
	MaturityRatings []string `json:"maturity_ratings"`
	SubtitleLocales []string `json:"subtitle_locales"`
	SeasonTitle     string   `json:"season_title"`
	SeriesID        string   `json:"series_id"`
	Images          Images   `json:"images"`
}

// Seasons wraps the list of seasons returned for a series.
type Seasons struct {
	Data []Season `json:"data"`
}

// Season is one season of a series. Season-level images are effectively empty
// on the wire, so art is fetched from the series (see Series) rather than here.
type Season struct {
	ID           string `json:"id"`
	SeasonNumber int    `json:"season_number"`

	Title            string   `json:"title"`
	SlugTitle        string   `json:"slug_title"`
	Description      string   `json:"description"`
	IsComplete       bool     `json:"is_complete"`
	NumberOfEpisodes int      `json:"number_of_episodes"`
	AudioLocales     []string `json:"audio_locales"`
	SubtitleLocales  []string `json:"subtitle_locales"`
	Keywords         []string `json:"keywords"`
}

// SeriesResponse wraps the list of series returned by the CMS series endpoint.
type SeriesResponse struct {
	Data []Series `json:"data"`
}

// Award is one award entry on a series (e.g. "Anime of the Year").
type Award struct {
	IconURL        string `json:"icon_url"`
	Text           string `json:"text"`
	IsCurrentAward bool   `json:"is_current_award"`
	IsWinner       bool   `json:"is_winner"`
}

// Series is one series from the CMS series endpoint. It carries the synopsis,
// launch year, genre-ish categories (tenant_categories — there is no "genres"
// key, and no studio field), audio/subtitle locales, and the poster_tall /
// poster_wide art used for the UI hero and the attached MKV/MP4 cover.
type Series struct {
	ID                  string   `json:"id"`
	ChannelID           string   `json:"channel_id"`
	ContentProvider     string   `json:"content_provider"`
	Slug                string   `json:"slug"`
	Title               string   `json:"title"`
	SlugTitle           string   `json:"slug_title"`
	Description         string   `json:"description"`
	ExtendedDescription string   `json:"extended_description"`
	SeriesLaunchYear    int      `json:"series_launch_year"`
	EpisodeCount        int      `json:"episode_count"`
	SeasonCount         int      `json:"season_count"`
	MediaCount          int      `json:"media_count"`
	Keywords            []string `json:"keywords"`
	// tenant_categories is the closest thing to genres (Drama, Action, ...).
	TenantCategories []string `json:"tenant_categories"`
	AudioLocales     []string `json:"audio_locales"`
	SubtitleLocales  []string `json:"subtitle_locales"`
	Images           Images   `json:"images"`
	MaturityRatings  []string `json:"maturity_ratings"`
	Awards           []Award  `json:"awards"`
}

// BestImage returns the highest-resolution image from a nested CMS image
// collection (the last element of the first inner array), or (Image{}, false)
// if the collection is empty. Callers should treat a false result as "no art"
// and skip cover attachment / hero rendering rather than failing.
func BestImage(imgs [][]Image) (Image, bool) {
	if len(imgs) == 0 || len(imgs[0]) == 0 {
		return Image{}, false
	}
	row := imgs[0]
	return row[len(row)-1], true
}
