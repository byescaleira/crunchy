// Package mux merges the downloaded video, audio and subtitle tracks into a
// single container via ffmpeg. BuildMergeArgs is extracted so the load-bearing
// argument order/indices/dispositions can be unit-tested without invoking
// ffmpeg. It is format-aware: .mkv attaches the cover and writes spec-conformant
// Matroska tags; .mp4 embeds the cover as an attached_pic video stream and
// converts ASS subtitles to mov_text with iTunes-style tags.
package mux

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
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
// videoW/videoH are the real video dimensions (probed by Merge via ffprobe);
// for MP4 they set the mov_text subtitle track canvas so QuickTime Player lists
// the subtitle (a 0x0 subtitle track is invisible to QuickTime). Ignored for MKV.
func BuildMergeArgs(videoFile string, audioTracks, subTracks []MediaTrack, outputFile, coverFile, format string, info media.EpisodeInfo, videoW, videoH int) []string {
	isMP4 := format == "mp4"
	attachCover := coverFile != "" && !isMP4 // MKV: -attach (an attachment stream)
	mapCover := coverFile != "" && isMP4     // MP4: a mapped mjpeg video stream

	args := []string{"-i", videoFile}
	for _, audio := range audioTracks {
		args = append(args, "-i", audio.File)
	}
	for _, sub := range subTracks {
		// mov_text (MP4) ignores ASS PlayResX/PlayResY and writes a 0x0 subtitle
		// track unless the canvas is set explicitly; a 0x0 subtitle track is
		// present (Subler shows the tx3g) but QuickTime Player won't list/select
		// it. -canvas_size is an input option (applies to the next -i) that makes
		// ffmpeg write the video dimensions into the subtitle tkhd, matching what
		// Apple-muxed files (e.g. iTunes .m4v) carry. MKV copies the ASS verbatim
		// (libass does its own scaling), so it doesn't need this. It does NOT
		// rescale the cue font — rescaleASSFont (called by Merge) handles that.
		if isMP4 && videoW > 0 && videoH > 0 {
			args = append(args, "-canvas_size", fmt.Sprintf("%dx%d", videoW, videoH))
		}
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
// when ffmpeg fails, so the caller can clean up and report. The context lets a
// cancelled job SIGTERM ffmpeg mid-mux (CommandContext sends the process-killing
// signal when ctx is cancelled); under a non-cancelled context it behaves
// identically to exec.Command, so the CLI output is unchanged.
func Merge(ctx context.Context, videoFile string, audioTracks, subTracks []MediaTrack, outputFile, coverFile, format string, info media.EpisodeInfo) error {
	// MP4's mov_text drops the one thing that makes Crunchyroll subtitles
	// readable: ASS PlayResY. The subs are authored on a small canvas (e.g.
	// 640x360) with a small Fontsize (e.g. 20); libass scales Fontsize by
	// display_height/PlayResY, so in MKV that 20 renders ~60px at 1080p. mov_text
	// ignores PlayResY and writes Fontsize verbatim into a subtitle track with
	// no dimensions, so players render it at ~20px on the video frame —
	// effectively invisible. MKV keeps the ASS verbatim (libass does the
	// scaling), so only MP4 needs this rescale to the real video height.
	videoW, videoH := 0, 0
	if format == "mp4" && len(subTracks) > 0 {
		videoW, videoH = probeVideoSize(videoFile)
		if videoH > 0 {
			for _, sub := range subTracks {
				if err := rescaleASSFont(sub.File, videoH); err != nil {
					return fmt.Errorf("rescale subtitle %s: %w", sub.Locale, err)
				}
			}
		}
	}

	args := BuildMergeArgs(videoFile, audioTracks, subTracks, outputFile, coverFile, format, info, videoW, videoH)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
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

// probeVideoSize returns the width and height (in pixels) of the first video
// stream in file, via ffprobe. Returns 0,0 if ffprobe is unavailable or the
// dimensions can't be read; callers treat 0 as "unknown" (subs keep their
// authored font size and the mov_text track stays 0x0 — no worse than before
// this fix).
func probeVideoSize(file string) (int, int) {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=p=0",
		file,
	).Output()
	if err != nil {
		return 0, 0
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) < 2 {
		return 0, 0
	}
	w, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
	return w, h
}

// rescaleASSFont rewrites an .ass in place so every Style Fontsize is scaled to a
// target video height, restoring what mov_text loses: the ASS PlayResY ratio.
//
// libass (MKV) renders Fontsize at display_height/PlayResY, so a Crunchyroll sub
// authored at PlayResY=360 with Fontsize=20 shows ~60px at 1080p. mov_text (MP4)
// ignores PlayResY and emits Fontsize verbatim with no subtitle-track dimensions,
// so the same 20 renders ~20px on the frame — invisible. Scaling each Style
// Fontsize by targetH/PlayResY before the mux makes mov_text emit the intended
// size. Only the Style Fontsize column is touched (mov_text ignores ASS
// positions/colors except bold/italic, which are unaffected). No-op when
// PlayResY is absent, zero, or already equals targetH.
func rescaleASSFont(path string, targetH int) error {
	if targetH <= 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")

	// Find PlayResY in [Script Info].
	playResY := 0
	for _, L := range lines {
		s := strings.TrimSpace(L)
		if strings.HasPrefix(s, "PlayResY:") {
			v, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(s, "PlayResY:")))
			playResY = v
			break
		}
	}
	if playResY <= 0 || playResY == targetH {
		return nil // unknown canvas, or already 1:1 with the video
	}
	scale := float64(targetH) / float64(playResY)

	// Style: Name,Fontname,Fontsize,... (Fontsize is the 3rd comma field, index 2).
	changed := false
	for i, L := range lines {
		if !strings.HasPrefix(strings.TrimSpace(L), "Style:") {
			continue
		}
		parts := strings.SplitN(L, ",", 23) // ASS Style has 23 fields; keep the tail whole
		if len(parts) < 3 {
			continue
		}
		fs, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err != nil {
			continue
		}
		parts[2] = strconv.Itoa(int(math.Round(float64(fs) * scale)))
		lines[i] = strings.Join(parts, ",")
		changed = true
	}
	if !changed {
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600)
}
