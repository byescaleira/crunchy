package media

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// mustLoad reads a testdata fixture relative to the package dir.
func mustLoad(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

func TestUnmarshal_EpisodeObjects(t *testing.T) {
	var resp EpisodeMetadataResponse
	if err := json.Unmarshal(mustLoad(t, "episode_objects.json"), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 data item, got %d", len(resp.Data))
	}
	ep := resp.Data[0]
	if ep.Title != "Dawn and Confusion" {
		t.Errorf("Title = %q", ep.Title)
	}
	// description is top-level on the data item, not under episode_metadata.
	if ep.Description != "Top-level synopsis on the data item." {
		t.Errorf("Description = %q", ep.Description)
	}
	if ep.Slug != "dawn-and-confusion" {
		t.Errorf("Slug = %q", ep.Slug)
	}
	m := ep.EpisodeMetadata
	if m.SeriesID != "GJ0H7Q5ZJ" || m.SeasonID != "GR9PC507V" || m.SeasonTitle != "Hell's Paradise" {
		t.Errorf("series/season ids = %q/%q/%q", m.SeriesID, m.SeasonID, m.SeasonTitle)
	}
	if m.DurationMS != 1425000 {
		t.Errorf("DurationMS = %d", m.DurationMS)
	}
	if !m.IsPremiumOnly {
		t.Error("IsPremiumOnly should be true")
	}
	if len(m.MaturityRatings) != 1 || m.MaturityRatings[0] != "TV-MA" {
		t.Errorf("MaturityRatings = %v", m.MaturityRatings)
	}
	if len(m.SubtitleLocales) != 3 {
		t.Errorf("SubtitleLocales = %v", m.SubtitleLocales)
	}
	if len(m.Versions) != 2 || m.Versions[1].AudioLocale != "en-US" {
		t.Errorf("Versions = %v", m.Versions)
	}
	// Episode thumbnail: last of first inner array = 1920x1080.
	img, ok := BestImage(m.Images.Thumbnail)
	if !ok || img.Width != 1920 || img.Height != 1080 {
		t.Errorf("BestImage(thumbnail) = %+v ok=%v", img, ok)
	}
}

func TestUnmarshal_Series(t *testing.T) {
	var resp SeriesResponse
	if err := json.Unmarshal(mustLoad(t, "series.json"), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 series, got %d", len(resp.Data))
	}
	s := resp.Data[0]
	if s.Title != "Hell's Paradise" || s.SeriesLaunchYear != 2023 {
		t.Errorf("title/year = %q/%d", s.Title, s.SeriesLaunchYear)
	}
	// tenant_categories (not "genres").
	if len(s.TenantCategories) != 2 || s.TenantCategories[0] != "Action" {
		t.Errorf("TenantCategories = %v", s.TenantCategories)
	}
	if len(s.AudioLocales) != 2 {
		t.Errorf("AudioLocales = %v", s.AudioLocales)
	}
	if len(s.Awards) != 1 || !s.Awards[0].IsWinner {
		t.Errorf("Awards = %v", s.Awards)
	}
	// Portrait poster: last of first inner = 1560x2340.
	img, ok := BestImage(s.Images.PosterTall)
	if !ok || img.Width != 1560 || img.Height != 2340 {
		t.Errorf("BestImage(poster_tall) = %+v ok=%v", img, ok)
	}
	// Landscape banner.
	wide, ok := BestImage(s.Images.PosterWide)
	if !ok || wide.Width != 1920 || wide.Height != 1080 {
		t.Errorf("BestImage(poster_wide) = %+v ok=%v", wide, ok)
	}
}

func TestUnmarshal_Seasons(t *testing.T) {
	var resp Seasons
	if err := json.Unmarshal(mustLoad(t, "seasons.json"), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 season, got %d", len(resp.Data))
	}
	s := resp.Data[0]
	if s.Title != "Hell's Paradise" || s.SeasonNumber != 1 {
		t.Errorf("title/number = %q/%d", s.Title, s.SeasonNumber)
	}
	if !s.IsComplete || s.NumberOfEpisodes != 13 {
		t.Errorf("complete/eps = %v/%d", s.IsComplete, s.NumberOfEpisodes)
	}
	if len(s.AudioLocales) != 2 {
		t.Errorf("AudioLocales = %v", s.AudioLocales)
	}
}

func TestUnmarshal_SeasonEpisodes(t *testing.T) {
	var resp SeasonEpisodes
	if err := json.Unmarshal(mustLoad(t, "season_episodes.json"), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 episode, got %d", len(resp.Data))
	}
	ep := resp.Data[0]
	if ep.Title != "Dawn and Confusion" || ep.Description != "Episode synopsis." {
		t.Errorf("title/desc = %q/%q", ep.Title, ep.Description)
	}
	if ep.SeriesID != "GJ0H7Q5ZJ" || ep.SeasonTitle != "Hell's Paradise" {
		t.Errorf("series/season = %q/%q", ep.SeriesID, ep.SeasonTitle)
	}
	if ep.DurationMS != 1425000 || !ep.IsPremiumOnly {
		t.Errorf("duration/premium = %d/%v", ep.DurationMS, ep.IsPremiumOnly)
	}
	if _, ok := BestImage(ep.Images.Thumbnail); !ok {
		t.Error("episode thumbnail missing")
	}
}

func TestBestImage_Empty(t *testing.T) {
	if _, ok := BestImage(nil); ok {
		t.Error("nil collection should be empty")
	}
	if _, ok := BestImage([][]Image{}); ok {
		t.Error("empty outer should be empty")
	}
	if _, ok := BestImage([][]Image{{}}); ok {
		t.Error("empty inner should be empty")
	}
}
