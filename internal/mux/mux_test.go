package mux

import (
	"reflect"
	"strings"
	"testing"

	"crunchyroll-downloader/internal/media"
)

func epInfo(series, title string, season, episode int) media.EpisodeInfo {
	return media.EpisodeInfo{
		Title: title,
		EpisodeMetadata: media.EpisodeMetadata{
			SeriesTitle:   series,
			SeasonNumber:  season,
			EpisodeNumber: episode,
		},
	}
}

func TestBuildMergeArgs_MKV(t *testing.T) {
	t.Run("one audio one sub", func(t *testing.T) {
		audio := []MediaTrack{{File: "a.m4a", Locale: "ja-JP"}}
		subs := []MediaTrack{{File: "s.ass", Locale: "en-US"}}
		info := epInfo("Series", "Ep", 1, 2)

		got := BuildMergeArgs("v.mp4", audio, subs, "out.mkv", "", "mkv", info)
		want := []string{
			"-i", "v.mp4",
			"-i", "a.m4a",
			"-i", "s.ass",
			"-map", "0:v:0",
			"-map", "1:a:0",
			"-map", "2",
			"-c:v", "copy", "-c:a", "copy", "-c:s", "copy",
			"-metadata:s:a:0", "language=jpn", "-metadata:s:a:0", "title=日本語",
			"-metadata:s:s:0", "language=eng", "-metadata:s:s:0", "title=English",
			"-disposition:a:0", "default",
			"-disposition:s:0", "default",
			"-metadata:g", "title=S01E02 - Ep",
			"-metadata:g", "track=2",
			"-metadata:g", "ARTIST=Series",
			"-metadata:g", "GENRE=Anime",
			"out.mkv",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("BuildMergeArgs mismatch:\ngot:  %v\nwant: %v", got, want)
		}
	})

	t.Run("two audios no subs", func(t *testing.T) {
		audio := []MediaTrack{
			{File: "a1.m4a", Locale: "ja-JP"},
			{File: "a2.m4a", Locale: "en-US"},
		}
		info := epInfo("Series", "Ep", 3, 4)

		got := BuildMergeArgs("v.mp4", audio, nil, "out.mkv", "", "mkv", info)
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
			"-disposition:a:1", "0",
			"-metadata:g", "title=S03E04 - Ep",
			"-metadata:g", "track=4",
			"-metadata:g", "ARTIST=Series",
			"-metadata:g", "GENRE=Anime",
			"out.mkv",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("BuildMergeArgs mismatch:\ngot:  %v\nwant: %v", got, want)
		}
	})
}

func TestBuildMergeArgs_MP4(t *testing.T) {
	audio := []MediaTrack{{File: "a.m4a", Locale: "ja-JP"}}
	subs := []MediaTrack{{File: "s.ass", Locale: "en-US"}}
	info := epInfo("Series", "Ep", 1, 2)

	got := BuildMergeArgs("v.mp4", audio, subs, "out.mp4", "", "mp4", info)
	want := []string{
		"-i", "v.mp4",
		"-i", "a.m4a",
		"-i", "s.ass",
		"-map", "0:v:0",
		"-map", "1:a:0",
		"-map", "2",
		"-c:v", "copy", "-c:a", "copy", "-c:s", "mov_text", // ASS -> mov_text for MP4
		"-metadata:s:a:0", "language=jpn", "-metadata:s:a:0", "title=日本語",
		"-metadata:s:s:0", "language=eng", "-metadata:s:s:0", "title=English",
		"-disposition:a:0", "default",
		"-disposition:s:0", "default",
		"-metadata:g", "title=S01E02 - Ep",
		"-metadata:g", "track=2",
		"-metadata:g", "show=Series",
		"-metadata:g", "season_number=1",
		"-metadata:g", "episode_id=S01E02",
		"-metadata:g", "artist=Series",
		"-metadata:g", "genre=Anime",
		"out.mp4",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildMergeArgs MP4 mismatch:\ngot:  %v\nwant: %v", got, want)
	}
}

// contains reports whether args contains the exact consecutive pair (flag, val).
func containsPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

func TestBuildMergeArgs_MKV_Cover(t *testing.T) {
	audio := []MediaTrack{{File: "a.m4a", Locale: "ja-JP"}}
	subs := []MediaTrack{{File: "s.ass", Locale: "en-US"}}
	info := epInfo("Series", "Ep", 1, 2)

	got := BuildMergeArgs("v.mp4", audio, subs, "out.mkv", "cover.jpg", "mkv", info)
	// The cover is an attachment, never a mapped stream.
	if !containsPair(got, "-attach", "cover.jpg") {
		t.Error("MKV cover must use -attach cover.jpg")
	}
	if !containsPair(got, "-metadata:s:t:0", "mimetype=image/jpeg") {
		t.Error("MKV cover missing mimetype metadata")
	}
	if !containsPair(got, "-metadata:s:t:0", "filename=cover.jpg") {
		t.Error("MKV cover missing filename=cover.jpg (ffmpeg would store the full path)")
	}
	for _, a := range got {
		if a == "-map" {
			// no cover input is mapped in MKV; -attach handles it
		}
	}
	// -attach block must precede the output file.
	attachIdx, outIdx := -1, -1
	for i, a := range got {
		if a == "-attach" {
			attachIdx = i
		}
		if a == "out.mkv" {
			outIdx = i
		}
	}
	if attachIdx < 0 || outIdx < 0 || attachIdx > outIdx {
		t.Errorf("-attach (%d) must precede output (%d)", attachIdx, outIdx)
	}
	// MKV uses spec tags, not the inert show/season_number.
	if strings.Contains(strings.Join(got, "\n"), "show=") || strings.Contains(strings.Join(got, "\n"), "season_number=") {
		t.Error("MKV must not emit the MP4-only show/season_number keys")
	}
}

func TestBuildMergeArgs_MP4_Cover(t *testing.T) {
	audio := []MediaTrack{{File: "a.m4a", Locale: "ja-JP"}}
	subs := []MediaTrack{{File: "s.ass", Locale: "en-US"}}
	info := epInfo("Series", "Ep", 1, 2)

	got := BuildMergeArgs("v.mp4", audio, subs, "out.mp4", "cover.jpg", "mp4", info)
	// MP4 cover is a mapped mjpeg video stream (input index 1 + #audio + #subs = 3).
	if !containsPair(got, "-i", "cover.jpg") {
		t.Error("MP4 cover must add -i cover.jpg")
	}
	if !containsPair(got, "-map", "3:v:0") {
		t.Error("MP4 cover must be mapped as the cover input's video stream")
	}
	if !containsPair(got, "-disposition:v:1", "attached_pic") {
		t.Error("MP4 cover stream must be marked attached_pic")
	}
	// MP4 must NOT use -attach (that's the MKV path).
	if containsPair(got, "-attach", "cover.jpg") {
		t.Error("MP4 must not use -attach")
	}
	// Subs are mov_text, not copy.
	if !containsPair(got, "-c:s", "mov_text") {
		t.Error("MP4 subs must be mov_text")
	}
}
