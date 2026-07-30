// Package mux merges the downloaded video, audio and subtitle tracks into a
// single container via ffmpeg. BuildMergeArgs is extracted so the load-bearing
// argument order/indices/dispositions can be unit-tested without invoking
// ffmpeg. It is format-aware: .mkv attaches the cover and writes spec-conformant
// Matroska tags; .mp4 embeds the cover as an attached_pic video stream and
// converts ASS subtitles to mov_text with iTunes-style tags.
package mux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"crunchyroll-downloader/internal/media"
	"crunchyroll-downloader/internal/output"
)

// MediaTrack pairs a downloaded temporary file with the locale it represents.
type MediaTrack struct {
	File   string
	Locale string
}

// BuildMergeArgs constructs the ffmpeg argument list that merges the video, all
// audio tracks, all subtitle tracks and (optionally) a cover image into a single
// .mkv or .mp4. Extracted from Merge so the load-bearing arg order/indices/
// dispositions can be unit-tested without invoking ffmpeg.
//
// format is "mkv" or "mp4"; coverFile is a path to a .jpg, or "" to skip the
// cover. info carries the episode + (best-effort) series metadata for tags.
func BuildMergeArgs(videoFile string, audioTracks, subTracks []MediaTrack, outputFile, coverFile, format string, info media.EpisodeInfo) []string {
	isMP4 := format == "mp4"
	attachCover := coverFile != "" && !isMP4 // MKV: -attach (an attachment stream)
	mapCover := coverFile != "" && isMP4     // MP4: a mapped mjpeg video stream

	args := []string{"-i", videoFile}
	for _, audio := range audioTracks {
		args = append(args, "-i", audio.File)
	}
	for _, sub := range subTracks {
		args = append(args, "-i", sub.File)
	}
	if mapCover {
		args = append(args, "-i", coverFile)
	}

	// Map every input stream explicitly; without this ffmpeg keeps only one
	// stream of each type.
	args = append(args, "-map", "0:v:0")
	for i := range audioTracks {
		args = append(args, "-map", fmt.Sprintf("%d:a:0", 1+i))
	}
	for j := range subTracks {
		args = append(args, "-map", fmt.Sprintf("%d", 1+len(audioTracks)+j))
	}
	if mapCover {
		// The cover is the last input: index 1 + #audio + #subs.
		coverInput := 1 + len(audioTracks) + len(subTracks)
		args = append(args, "-map", fmt.Sprintf("%d:v:0", coverInput))
	}

	// Codecs. -c:v copy covers the main video and (for MP4) the mjpeg cover;
	// the MKV cover is an attachment (type t), untouched by -c:v/-c:a/-c:s.
	args = append(args, "-c:v", "copy", "-c:a", "copy")
	if len(subTracks) > 0 {
		if isMP4 {
			// MP4 cannot carry ASS; convert to mov_text.
			args = append(args, "-c:s", "mov_text")
		} else {
			args = append(args, "-c:s", "copy")
		}
	}

	for i, audio := range audioTracks {
		args = append(args,
			fmt.Sprintf("-metadata:s:a:%d", i), "language="+output.LanguageCodes[audio.Locale],
			fmt.Sprintf("-metadata:s:a:%d", i), "title="+output.TrackTitle(audio.Locale),
		)
	}
	for j, sub := range subTracks {
		args = append(args,
			fmt.Sprintf("-metadata:s:s:%d", j), "language="+output.LanguageCodes[sub.Locale],
			fmt.Sprintf("-metadata:s:s:%d", j), "title="+output.TrackTitle(sub.Locale),
		)
	}

	// Mark only the first audio/subtitle track (the primary requested locale) as
	// default. Disposition must be set on every track: each downloaded audio
	// file is a standalone default stream, so the non-primary ones must be
	// explicitly cleared.
	for i := range audioTracks {
		disposition := "0"
		if i == 0 {
			disposition = "default"
		}
		args = append(args, fmt.Sprintf("-disposition:a:%d", i), disposition)
	}
	for j := range subTracks {
		disposition := "0"
		if j == 0 {
			disposition = "default"
		}
		args = append(args, fmt.Sprintf("-disposition:s:%d", j), disposition)
	}
	// MP4 cover: mark the mapped mjpeg stream (output video index 1, after the
	// main video) as attached_pic so players treat it as cover art.
	if mapCover {
		args = append(args, "-disposition:v:1", "attached_pic")
	}

	// Global container metadata. The title (S{NN}E{NN} - Title) and track
	// (episode number; ffmpeg converts "track" to Matroska PART_NUMBER) are
	// common. The rest is format-specific: MKV uses spec-conformant uppercase
	// SimpleTags (show/season_number are inert in Matroska and dropped); MP4
	// uses iTunes-style keys (show/season_number/episode_id are meaningful).
	m := info.EpisodeMetadata
	title := fmt.Sprintf("S%02vE%02v - %s", m.SeasonNumber, m.EpisodeNumber, info.Title)
	track := fmt.Sprintf("%v", m.EpisodeNumber)
	series := m.SeriesTitle
	genre := genreOf(info.Series)
	desc := info.Description
	date := airDate(m)
	rating := maturityOf(m)

	args = append(args, "-metadata:g", "title="+title, "-metadata:g", "track="+track)
	if isMP4 {
		args = metaG(args, "show", series)
		args = metaG(args, "season_number", fmt.Sprintf("%v", m.SeasonNumber))
		args = metaG(args, "episode_id", fmt.Sprintf("S%02vE%02v", m.SeasonNumber, m.EpisodeNumber))
		args = metaG(args, "artist", series)
		args = metaG(args, "genre", genre)
		args = metaG(args, "date", date)
		args = metaG(args, "description", desc)
		args = metaG(args, "content_rating", rating)
	} else {
		args = metaG(args, "ARTIST", series)
		args = metaG(args, "GENRE", genre)
		args = metaG(args, "DATE_RELEASED", date)
		args = metaG(args, "DESCRIPTION", desc)
		args = metaG(args, "LAW_RATING", rating)
	}

	// MKV cover: attach after all -map/-c/dispositions, before the output file.
	// Never -map it (it becomes an attachment stream, type t). filename= must be
	// set explicitly or ffmpeg stores the full filesystem path.
	if attachCover {
		args = append(args,
			"-attach", coverFile,
			"-metadata:s:t:0", "mimetype=image/jpeg",
			"-metadata:s:t:0", "filename=cover.jpg",
		)
	}

	args = append(args, outputFile)
	return args
}

// metaG appends a global metadata pair only when val is non-empty, so empty
// synopsis/dates/ratings don't produce inert `KEY=` tags.
func metaG(args []string, key, val string) []string {
	if val == "" {
		return args
	}
	return append(args, "-metadata:g", key+"="+val)
}

// genreOf returns the series categories joined, falling back to "Anime" when
// the series has no tenant_categories (the CMS "genres"-equivalent).
func genreOf(s media.Series) string {
	if len(s.TenantCategories) > 0 {
		return strings.Join(s.TenantCategories, ", ")
	}
	return "Anime"
}

// airDate returns the YYYY-MM-DD of the episode, preferring episode_air_date
// and falling back to availability_starts.
func airDate(m media.EpisodeMetadata) string {
	if len(m.EpisodeAirDate) >= 10 {
		return m.EpisodeAirDate[:10]
	}
	if len(m.AvailabilityStarts) >= 10 {
		return m.AvailabilityStarts[:10]
	}
	return ""
}

// maturityOf returns the first maturity rating, or "" if none.
func maturityOf(m media.EpisodeMetadata) string {
	if len(m.MaturityRatings) > 0 {
		return m.MaturityRatings[0]
	}
	return ""
}

// Merge merges the video, all audio tracks, all subtitle tracks and (optionally)
// a cover into a single container. It returns an error (instead of panicking)
// when ffmpeg fails, so the caller can clean up and report.
func Merge(videoFile string, audioTracks, subTracks []MediaTrack, outputFile, coverFile, format string, info media.EpisodeInfo) error {
	args := BuildMergeArgs(videoFile, audioTracks, subTracks, outputFile, coverFile, format, info)

	cmd := exec.Command("ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(outputFile)
		return fmt.Errorf("ffmpeg failed: %s\n%s", err, stderr.String())
	}

	// Remove temporary files
	_ = os.Remove(videoFile)
	for _, audio := range audioTracks {
		_ = os.Remove(audio.File)
	}
	for _, sub := range subTracks {
		_ = os.Remove(sub.File)
	}
	if coverFile != "" {
		_ = os.Remove(coverFile)
	}

	fmt.Printf("\nDownload finished! Output file: %s\n\n", outputFile)
	return nil
}
