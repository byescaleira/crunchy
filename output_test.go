package main

import (
	"reflect"
	"testing"
)

func TestTrackTitle(t *testing.T) {
	tests := []struct {
		locale, want string
	}{
		{"ja-JP", "日本語"},
		{"en-US", "English"},
		{"pt-BR", "Português (Brasil)"},
		{"xx-ZZ", "xx-ZZ"}, // unknown locale falls back to raw
		{"", ""},
	}
	for _, tc := range tests {
		got := trackTitle(tc.locale)
		if got != tc.want {
			t.Errorf("trackTitle(%q) = %q, want %q", tc.locale, got, tc.want)
		}
	}
}

func epInfo(series, title string, season, episode int) EpisodeInfo {
	return EpisodeInfo{
		Title: title,
		EpisodeMetadata: EpisodeMetadata{
			SeriesTitle:   series,
			SeasonNumber:  season,
			EpisodeNumber: episode,
		},
	}
}

func TestBuildMergeArgs(t *testing.T) {
	t.Run("one audio one sub", func(t *testing.T) {
		videoFile := "v.mp4"
		audio := []mediaTrack{{file: "a.m4a", locale: "ja-JP"}}
		subs := []mediaTrack{{file: "s.ass", locale: "en-US"}}
		info := epInfo("Series", "Ep", 1, 2)

		got := buildMergeArgs(videoFile, audio, subs, "out.mkv", info)
		want := []string{
			"-i", "v.mp4",
			"-i", "a.m4a",
			"-i", "s.ass",
			"-map", "0:v:0",
			"-map", "1:a:0",
			"-map", "2", // 1 + len(audio) + j = 1 + 1 + 0
			"-c:v", "copy", "-c:a", "copy", "-c:s", "copy",
			"-metadata:s:a:0", "language=jpn", "-metadata:s:a:0", "title=日本語",
			"-metadata:s:s:0", "language=eng", "-metadata:s:s:0", "title=English",
			"-disposition:a:0", "default",
			"-disposition:s:0", "default",
			"-metadata:g", "title=S01E02 - Ep",
			"-metadata:g", "show=Series",
			"-metadata:g", "track=2",
			"-metadata:g", "season_number=1",
			"out.mkv",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("buildMergeArgs mismatch:\ngot:  %v\nwant: %v", got, want)
		}
	})

	t.Run("two audios no subs", func(t *testing.T) {
		videoFile := "v.mp4"
		audio := []mediaTrack{
			{file: "a1.m4a", locale: "ja-JP"},
			{file: "a2.m4a", locale: "en-US"},
		}
		info := epInfo("Series", "Ep", 3, 4)

		got := buildMergeArgs(videoFile, audio, nil, "out.mkv", info)
		want := []string{
			"-i", "v.mp4",
			"-i", "a1.m4a",
			"-i", "a2.m4a",
			"-map", "0:v:0",
			"-map", "1:a:0",
			"-map", "2:a:0",
			"-c:v", "copy", "-c:a", "copy", // no -c:s without subs
			"-metadata:s:a:0", "language=jpn", "-metadata:s:a:0", "title=日本語",
			"-metadata:s:a:1", "language=eng", "-metadata:s:a:1", "title=English",
			"-disposition:a:0", "default",
			"-disposition:a:1", "0", // non-primary cleared
			"-metadata:g", "title=S03E04 - Ep",
			"-metadata:g", "show=Series",
			"-metadata:g", "track=4",
			"-metadata:g", "season_number=3",
			"out.mkv",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("buildMergeArgs mismatch:\ngot:  %v\nwant: %v", got, want)
		}
	})
}
