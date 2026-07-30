// Package mux merges the downloaded video, audio and subtitle tracks into a
// single MKV via ffmpeg. BuildMergeArgs is extracted so the load-bearing
// argument order/indices/dispositions can be unit-tested without invoking
// ffmpeg.
package mux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"crunchyroll-downloader/internal/media"
	"crunchyroll-downloader/internal/output"
)

// MediaTrack pairs a downloaded temporary file with the locale it represents.
type MediaTrack struct {
	File   string
	Locale string
}

// BuildMergeArgs constructs the ffmpeg argument list that merges the video, all
// audio tracks and all subtitle tracks into a single MKV. Extracted from Merge
// so the load-bearing arg order/indices/dispositions can be unit-tested
// without invoking ffmpeg.
func BuildMergeArgs(videoFile string, audioTracks, subTracks []MediaTrack, outputFile string, info media.EpisodeInfo) []string {
	args := []string{"-i", videoFile}
	for _, audio := range audioTracks {
		args = append(args, "-i", audio.File)
	}
	for _, sub := range subTracks {
		args = append(args, "-i", sub.File)
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

	args = append(args, "-c:v", "copy", "-c:a", "copy")
	if len(subTracks) > 0 {
		args = append(args, "-c:s", "copy")
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

	args = append(args,
		"-metadata:g", "title="+fmt.Sprintf("S%02vE%02v - %s", info.EpisodeMetadata.SeasonNumber, info.EpisodeMetadata.EpisodeNumber, info.Title),
		"-metadata:g", "show="+info.EpisodeMetadata.SeriesTitle,
		"-metadata:g", "track="+fmt.Sprintf("%v", info.EpisodeMetadata.EpisodeNumber),
		"-metadata:g", "season_number="+fmt.Sprintf("%v", info.EpisodeMetadata.SeasonNumber),
		outputFile,
	)
	return args
}

// Merge merges the video, all audio tracks and all subtitle tracks into a
// single MKV container.
func Merge(videoFile string, audioTracks, subTracks []MediaTrack, outputFile string, info media.EpisodeInfo) {
	args := BuildMergeArgs(videoFile, audioTracks, subTracks, outputFile, info)

	cmd := exec.Command("ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(outputFile)
		panic(fmt.Sprintf("ffmpeg failed: %s\n%s", err, stderr.String()))
	}

	// Remove temporary files
	_ = os.Remove(videoFile)
	for _, audio := range audioTracks {
		_ = os.Remove(audio.File)
	}
	for _, sub := range subTracks {
		_ = os.Remove(sub.File)
	}

	fmt.Printf("\nDownload finished! Output file: %s\n\n", outputFile)
}
