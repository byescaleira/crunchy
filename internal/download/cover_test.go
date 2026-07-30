package download

import (
	"testing"

	"crunchyroll-downloader/internal/media"
)

// imgURL builds a one-entry nested image collection at the given URL so the
// cover fallback chain has a single BestImage hit to resolve.
func imgURL(u string) [][]media.Image {
	return [][]media.Image{{{Source: u}}}
}

// TestPickCoverImage_PrefersSeriesPoster verifies the series cover: when both
// the series and the episode carry poster art, the series poster wins (the same
// cover is used across every episode of a series).
func TestPickCoverImage_PrefersSeriesPoster(t *testing.T) {
	info := media.EpisodeInfo{
		EpisodeMetadata: media.EpisodeMetadata{
			Images: media.Images{PosterTall: imgURL("https://x/episode-poster.jpg")},
		},
		Series: media.Series{Images: media.Images{PosterTall: imgURL("https://x/series-poster.jpg")}},
	}
	got, ok := pickCoverImage(info)
	if !ok {
		t.Fatal("expected a cover, got none")
	}
	if got != "https://x/series-poster.jpg" {
		t.Errorf("expected series poster, got %q", got)
	}
}

// TestPickCoverImage_FallsBackToEpisodePoster drops to the episode's own poster
// when the series has no portrait art.
func TestPickCoverImage_FallsBackToEpisodePoster(t *testing.T) {
	info := media.EpisodeInfo{
		EpisodeMetadata: media.EpisodeMetadata{
			Images: media.Images{PosterTall: imgURL("https://x/episode-poster.jpg")},
		},
	}
	got, ok := pickCoverImage(info)
	if !ok {
		t.Fatal("expected a cover, got none")
	}
	if got != "https://x/episode-poster.jpg" {
		t.Errorf("expected episode poster, got %q", got)
	}
}

// TestPickCoverImage_FallsBackToEpisodeThumbnail drops to the episode thumbnail
// when neither the series nor the episode carry poster art.
func TestPickCoverImage_FallsBackToEpisodeThumbnail(t *testing.T) {
	info := media.EpisodeInfo{
		EpisodeMetadata: media.EpisodeMetadata{
			Images: media.Images{Thumbnail: imgURL("https://x/episode-thumb.jpg")},
		},
	}
	got, ok := pickCoverImage(info)
	if !ok {
		t.Fatal("expected a cover, got none")
	}
	if got != "https://x/episode-thumb.jpg" {
		t.Errorf("expected episode thumbnail, got %q", got)
	}
}

// TestPickCoverImage_NoArt returns false when neither the series nor the episode
// carry any image, so no cover attaches.
func TestPickCoverImage_NoArt(t *testing.T) {
	got, ok := pickCoverImage(media.EpisodeInfo{})
	if ok {
		t.Errorf("expected no cover, got %q", got)
	}
}