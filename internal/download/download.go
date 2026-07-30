// Package download orchestrates fetching, decrypting and assembling an
// episode: downloading media segments in parallel, decrypting them one at a
// time straight to temp files (to keep memory bounded), downloading subtitles,
// and driving the mux step. A Downloader bundles the Crunchyroll API client
// with the user's quality/language selections so the CLI and the server share
// one code path.
package download

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/iyear/gowidevine"
	"github.com/unki2aut/go-mpd"

	"crunchyroll-downloader/internal/crunchy"
	"crunchyroll-downloader/internal/drm"
	"crunchyroll-downloader/internal/manifest"
	"crunchyroll-downloader/internal/media"
	"crunchyroll-downloader/internal/mux"
	"crunchyroll-downloader/internal/output"
)

// API is the slice of the Crunchyroll client that Episode/Season need. Every
// method returns an error instead of panicking/os.Exit, so a single bad episode
// or transport blip no longer aborts the whole batch. The concrete
// *crunchy.Client satisfies it; tests substitute a fake.
type API interface {
	GetEpisode(id string) (media.Episode, error)
	GetEpisodeInfo(id string) (media.EpisodeInfo, error)
	GetSeasons(contentId, audioLocale, subLocale string) ([]media.Season, error)
	GetSeasonEpisodes(contentId, audioLocale, subLocale string) ([]media.SeasonEpisode, error)
	GetSeries(id string) (media.Series, error)
	DownloadImage(url string) (string, error)
	DeleteStream(contentId, sToken string) (bool, error)
	ParseManifest(url string) (*mpd.MPD, error)
	GetLicense(psshData, contentId, videoToken string) ([]*widevine.Key, error)
}

// Downloader bundles the Crunchyroll API client with the user's quality and
// language selections. Its Episode/Season methods drive a download.
type Downloader struct {
	API          API
	HTTP         crunchy.Doer // raw HTTP for CDN segment/subtitle fetches
	VideoQuality string
	AudioQuality string
	AudioLangs   []string
	SubsLangs    []string
	MaxWorkers   int
	Debug        bool
	Progress     Progress
	OutputDir    string
	Format       string // "mkv" (default) or "mp4"

	// The I/O steps are overridable func fields so the orchestration (the
	// keys-ordering invariant, the per-track sequence) can be tested without
	// network, Widevine, or ffmpeg. They default to the real implementations
	// the first time a download runs (see ensureSeams). Each carries a
	// context.Context so a cancelled job aborts the in-flight work (segment
	// fetches, decrypt loop, ffmpeg) instead of running to completion.
	downloadTrack     func(ctx context.Context, baseUrl, representationId *string, set *mpd.AdaptationSet, keys []*widevine.Key) (string, error)
	downloadSubtitles func(ctx context.Context, url string) (string, error)
	merge             func(ctx context.Context, videoFile string, audioTracks, subTracks []mux.MediaTrack, outputFile, coverFile, format string, info media.EpisodeInfo) error
}

// workers returns the configured segment-download parallelism, defaulting to 16.
// The default was raised from 10 once SharedClient got a real Transport
// (MaxIdleConnsPerHost 100): the pool can now actually reuse keep-alive
// connections to the CDN host, so more workers translate to throughput rather
// than to new TLS handshakes. Set explicitly to stay conservative vs. CDN limits.
func (d *Downloader) workers() int {
	if d.MaxWorkers > 0 {
		return d.MaxWorkers
	}
	return 16
}

// ensureSeams fills in the default I/O implementations, HTTP client and progress
// reporter. It is idempotent so callers (and tests) only override the seams they
// care about.
func (d *Downloader) ensureSeams() {
	if d.HTTP == nil {
		d.HTTP = crunchy.SharedClient
	}
	if d.downloadTrack == nil {
		d.downloadTrack = d.downloadParts
	}
	if d.downloadSubtitles == nil {
		d.downloadSubtitles = func(ctx context.Context, url string) (string, error) { return downloadSubs(ctx, d.HTTP, url) }
	}
	if d.merge == nil {
		d.merge = mux.Merge
	}
	if d.Format == "" {
		d.Format = "mkv"
	}
	if d.Progress == nil {
		d.Progress = stdoutProgress{}
	}
}

func buildUrl(base, representationId, file string, partNum *int64) string {
	if partNum != nil {
		file = strings.ReplaceAll(file, "$Number$", fmt.Sprintf("%05d", *partNum))
		file = strings.ReplaceAll(file, "$Number%05d$", fmt.Sprintf("%05d", *partNum))
	}
	return base + strings.ReplaceAll(file, "$RepresentationID$", representationId)
}

// downloadPart fetches one segment URL with up to maxRetries attempts and a
// linear backoff. The context both cancels an in-flight request (so a cancelled
// job aborts the worker immediately, not after the body finishes) and bounds
// each attempt with a per-segment timeout — a stalled CDN response fails fast
// and retries instead of hanging a worker indefinitely. A cancelled context is
// surfaced as ctx.Err() so the caller can distinguish cancel from a real error.
func downloadPart(ctx context.Context, doer crunchy.Doer, url string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	maxRetries := 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Respect cancellation during the backoff sleep; a cancelled job
			// should not pay the full backoff before exiting.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}

		// Bound this single attempt so a stuck segment fails fast and retries,
		// rather than holding a worker slot forever.
		reqCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("User-Agent", crunchy.UserAgent)
		req.Header.Set("Origin", "https://static.crunchyroll.com")
		req.Header.Set("Referer", "https://static.crunchyroll.com/")
		resp, err := doer.Do(req)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				// Parent cancelled — don't mask it as a retryable transport error.
				return nil, ctx.Err()
			}
			if attempt < maxRetries-1 {
				continue
			}
			return nil, fmt.Errorf("failed after %d retries: %w", maxRetries, err)
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			cancel()
			if attempt < maxRetries-1 {
				continue
			}
			return nil, fmt.Errorf("failed after %d retries, status: %d", maxRetries, resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err != nil {
			if attempt < maxRetries-1 {
				continue
			}
			return nil, fmt.Errorf("failed reading body after %d retries: %w", maxRetries, err)
		}
		return body, nil
	}
	return nil, fmt.Errorf("failed after %d retries", maxRetries)
}

func getFilename(set *mpd.AdaptationSet) string {
	if set == nil {
		f, _ := os.CreateTemp("", "crdl-subs-*.ass")
		name := f.Name()
		f.Close()
		return name
	}
	for _, representation := range set.Representations {
		if representation.Height != nil {
			f, _ := os.CreateTemp("", "crdl-video-*.mp4")
			name := f.Name()
			f.Close()
			return name
		} else if representation.Bandwidth != nil {
			f, _ := os.CreateTemp("", "crdl-audio-*.m4a")
			name := f.Name()
			f.Close()
			return name
		}
	}
	return ""
}

type segmentJob struct {
	index int
	url   string
}

// downloadParts fetches the init segment and every media segment for one
// adaptation set (in parallel), then decrypts them one at a time straight into
// a temp output file using keys. Segments are streamed through per-segment temp
// files so a heavy episode never keeps the whole file in RAM. The context aborts
// the worker pool mid-download (each worker selects on ctx.Done()) and the
// decrypt pass mid-loop.
func (d *Downloader) downloadParts(ctx context.Context, baseUrl, representationId *string, set *mpd.AdaptationSet, keys []*widevine.Key) (string, error) {
	initUrl := buildUrl(*baseUrl, *representationId, *set.SegmentTemplate.Initialization, nil)
	initData, err := downloadPart(ctx, d.HTTP, initUrl)
	if err != nil {
		return "", err
	}

	timeline := manifest.ExpandTimeline(set.SegmentTemplate.SegmentTimeline.S, 1)
	total := len(timeline)
	// segFiles holds the on-disk path of each downloaded segment, in order.
	// Segments are written straight to temp files instead of accumulated in
	// memory, so downloading a heavy episode no longer keeps the whole file in
	// RAM (which was freezing the machine once it grew past available memory).
	segFiles := make([]string, total)
	var downloadErr error
	var errOnce sync.Once
	var done atomic.Int64

	jobs := make(chan segmentJob, total)
	var wg sync.WaitGroup

	for w := 0; w < d.workers(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					// Cancelled: stop pulling jobs. Surface the cancel error once.
					errOnce.Do(func() { downloadErr = ctx.Err() })
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					data, err := downloadPart(ctx, d.HTTP, job.url)
					if err != nil {
						errOnce.Do(func() { downloadErr = err })
						return
					}
					tmp, err := os.CreateTemp("", "crdl-seg-*.mp4")
					if err != nil {
						errOnce.Do(func() { downloadErr = err })
						return
					}
					name := tmp.Name()
					_, err = tmp.Write(data)
					tmp.Close()
					if err != nil {
						os.Remove(name)
						errOnce.Do(func() { downloadErr = err })
						return
					}
					segFiles[job.index] = name
					count := done.Add(1)
					d.Progress.Segment(int(count), total)
				}
			}
		}()
	}

	// Produce jobs, but bail out early if the context is already cancelled so we
	// don't enqueue the full timeline just to discard it.
	for i, item := range timeline {
		select {
		case <-ctx.Done():
			errOnce.Do(func() { downloadErr = ctx.Err() })
			// Drain remaining produce into the closed-after channel: close now so
			// workers exit their range, then wait.
			close(jobs)
			wg.Wait()
			removeFiles(segFiles)
			return "", ctx.Err()
		default:
		}
		url := buildUrl(*baseUrl, *representationId, *set.SegmentTemplate.Media, &item)
		jobs <- segmentJob{index: i, url: url}
	}
	close(jobs)
	wg.Wait()

	if downloadErr != nil {
		removeFiles(segFiles)
		return "", downloadErr
	}

	d.Progress.Printf("\nFinished downloading!\n")

	// Decrypt segment-by-segment straight from the temp files into the output
	// file. This keeps only one segment in memory at a time during decryption,
	// instead of decoding the whole file into RAM like widevine.DecryptMP4Auto.
	filename := getFilename(set)
	if err := decryptSegmentsToFile(ctx, initData, segFiles, filename, keys); err != nil {
		os.Remove(filename)
		removeFiles(segFiles)
		return "", err
	}
	removeFiles(segFiles)
	return filename, nil
}

// removeFiles deletes every non-empty path in files, ignoring errors so a
// missing file (already removed, or never created on a failed worker) is a
// no-op.
func removeFiles(files []string) {
	for _, f := range files {
		if f != "" {
			os.Remove(f)
		}
	}
}

// decryptSegmentsToFile decrypts the init segment followed by each media
// segment into outputFile, reading one segment at a time. Decoding init + a
// single segment per iteration lets mp4ff resolve the encryption context from
// the moov (needed for correct senc parsing under both CENC and CBCS) while
// keeping memory bounded to one segment's mdat instead of the whole file.
func decryptSegmentsToFile(ctx context.Context, initData []byte, segFiles []string, outputFile string, keys []*widevine.Key) error {
	key, err := drm.ContentKey(keys)
	if err != nil {
		return err
	}

	out, err := os.Create(outputFile)
	if err != nil {
		return err
	}
	defer out.Close()

	var decryptInfo mp4.DecryptInfo
	for i, segFile := range segFiles {
		// The decrypt pass is sequential and can be long; check cancellation
		// before each segment so a cancelled job exits here too, not only in the
		// download pool. The partial output file is cleaned up by the caller.
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := os.Open(segFile)
		if err != nil {
			return fmt.Errorf("open segment %d: %w", i, err)
		}
		inMp4, err := mp4.DecodeFile(io.MultiReader(bytes.NewReader(initData), f))
		f.Close()
		if err != nil {
			return fmt.Errorf("decode segment %d: %w", i, err)
		}
		if i == 0 {
			if inMp4.Init == nil {
				return fmt.Errorf("no init part of file")
			}
			decryptInfo, err = mp4.DecryptInit(inMp4.Init)
			if err != nil {
				return fmt.Errorf("decrypt init: %w", err)
			}
			if err = inMp4.Init.Encode(out); err != nil {
				return fmt.Errorf("write init: %w", err)
			}
		}
		for _, seg := range inMp4.Segments {
			if err = mp4.DecryptSegment(seg, decryptInfo, key); err != nil {
				if err.Error() == "no senc box in traf" {
					// No SENC box: samples may be unencrypted here, skip
					// decryption for this segment. Mirrors widevine.DecryptMP4.
					err = nil
				} else {
					return fmt.Errorf("decrypt segment %d: %w", i, err)
				}
			}
			if err = seg.Encode(out); err != nil {
				return fmt.Errorf("encode segment %d: %w", i, err)
			}
		}
		// Drop this segment's temp file as soon as it's in the output so disk
		// usage stays close to one copy of the media, not two.
		os.Remove(segFile)
	}
	return nil
}

func downloadSubs(ctx context.Context, doer crunchy.Doer, url string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", crunchy.UserAgent)
	req.Header.Set("Origin", "https://static.crunchyroll.com")
	req.Header.Set("Referer", "https://static.crunchyroll.com/")
	resp, err := doer.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	filename := getFilename(nil)
	file, err := os.Create(filename)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return "", err
	}
	file.Close()

	return filename, nil
}

// assHasUsableDialogue reports whether the .ass file at path contains at least
// one Dialogue event whose Text field carries renderable text (after ASS
// override blocks and line-break markers are stripped).
//
// ffmpeg happily muxes a .ass that parses but has no usable Dialogue into an
// empty subtitle track: the stream exists (subtitle editors list it as a
// component) but carries zero cues, so players show nothing while mux reports
// success. Crunchyroll lists a subtitle by its URL only (download.go checks
// sub.URL != ""), so a locale it serves empty or signs-only slips through
// silently. Validating the content here — before mux — turns that into a loud
// skip instead of a silently empty track.
func assHasUsableDialogue(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	inEvents := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "[Events]" {
			inEvents = true
			continue
		}
		if !inEvents || !strings.HasPrefix(line, "Dialogue:") {
			continue
		}
		// Dialogue: Layer,Start,End,Style,Name,MarginL,MarginR,MarginV,Effect,Text
		// Text is everything after the 9th comma; SplitN keeps it whole so any
		// commas inside the text itself don't split it.
		rest := strings.TrimPrefix(line, "Dialogue:")
		parts := strings.SplitN(rest, ",", 10)
		if len(parts) < 10 {
			continue
		}
		if hasASSRenderableText(parts[9]) {
			return true, nil
		}
	}
	return false, sc.Err()
}

// hasASSRenderableText reports whether an ASS Text field has any text mov_text
// will actually emit as a readable cue. Override blocks {\...}, line-break
// markers (\N, \n, \h) and surrounding whitespace are stripped first; whatever
// remains is renderable. Errs toward true: a borderline event is kept rather
// than risk skipping a valid subtitle.
//
// The one thing that is NOT renderable is ASS drawing mode: while a \pN
// override (N != 0) is active, the "text" is vector-geometry commands
// (m/l/b/s ...), which mov_text cannot draw — it emits them verbatim as
// literal garbage ("m 0 0 l 100 0 100 100 0 100"). So text produced while
// drawing mode is on does not count toward renderable, and a signs-only line
// (\p1 ... \p0 with no dialogue) is skipped rather than muxed as garbage.
func hasASSRenderableText(text string) bool {
	var out strings.Builder
	drawing := false
	for {
		i := strings.IndexByte(text, '{')
		if i < 0 {
			if !drawing {
				out.WriteString(text)
			}
			break
		}
		if !drawing {
			out.WriteString(text[:i])
		}
		j := strings.IndexByte(text[i:], '}')
		if j < 0 {
			break // unbalanced override; drop the rest
		}
		if on, ok := assDrawingMode(text[i+1 : i+j]); ok {
			drawing = on
		}
		text = text[i+j+1:]
	}
	s := strings.ReplaceAll(out.String(), `\N`, "")
	s = strings.ReplaceAll(s, `\n`, "")
	s = strings.ReplaceAll(s, `\h`, "")
	return strings.TrimSpace(s) != ""
}

// assDrawingMode scans one override block (the text between '{' and '}') for
// the last \pN override and reports whether it turns ASS drawing mode on. ok is
// false when the block carries no \p override, so the caller leaves the drawing
// flag unchanged. \pbo (blur edges), \pos, etc. are distinct tags and ignored.
func assDrawingMode(block string) (on bool, ok bool) {
	i := 0
	for i < len(block) {
		if block[i] == '\\' && i+1 < len(block) && block[i+1] == 'p' {
			k := i + 2
			if k < len(block) && block[k] >= '0' && block[k] <= '9' {
				n := 0
				for k < len(block) && block[k] >= '0' && block[k] <= '9' {
					n = n*10 + int(block[k]-'0')
					k++
				}
				on = n != 0
				ok = true
				i = k
				continue
			}
		}
		i++
	}
	return on, ok
}

// Episode downloads and muxes a single episode: its subtitles, every requested
// audio dub, and the video track, into a single MKV named after the series and
// episode. It mutates copies of the language selections (so "all" expands per
// episode), leaving the Downloader's fields untouched for the next episode.
//
// Every failure is returned as an error instead of panicking, so Season (and the
// server's job runner) can log it and move on. The activeStreams cleanup still
// runs via defer; a best-effort stream release must never be skipped because the
// download itself failed.
//
// The Widevine keys-ordering invariant is enforced structurally here: each
// version's keys are loop-local (versionKeys), and the audio for version i is
// downloaded (and thus decrypted) synchronously before the loop advances to
// version i+1's GetLicense. The video track is downloaded once with version 0's
// keys.
func (d *Downloader) Episode(ctx context.Context, baseContentId string, info media.EpisodeInfo) error {
	d.ensureSeams()

	// Honour cancellation at the top so a job cancelled before it acquired its
	// slot (or while queued) exits immediately with the cancel status rather than
	// beginning real work.
	if err := ctx.Err(); err != nil {
		return err
	}

	cleanSeriesTitle := output.Sanitize(info.EpisodeMetadata.SeriesTitle)

	// OutputDir lets the server direct downloads into a user-chosen folder; the
	// CLI leaves it empty so the series directory is created relative to the CWD,
	// matching the pre-refactor behavior.
	seriesDir := filepath.Join(d.OutputDir, cleanSeriesTitle)
	if _, err := os.Stat(seriesDir); err != nil {
		_ = os.MkdirAll(seriesDir, 0777)
	}

	videoQuality := &d.VideoQuality
	audioQuality := &d.AudioQuality

	ext := ".mkv"
	if d.Format == "mp4" {
		ext = ".mp4"
	}
	// Filename: "<season>.<episode> <title> (<quality>).<ext>" under the series
	// directory. The requested quality string (not the manifest height) is used so
	// the name is known before the manifest is fetched and the skip-check below
	// stays valid.
	outputFile := filepath.Join(seriesDir, output.BuildEpisodeFilename(
		info.EpisodeMetadata.SeasonNumber,
		info.EpisodeMetadata.EpisodeNumber,
		info.Title,
		*videoQuality,
		ext,
	))

	// Announce the final output name + path now that they're known, so the
	// server can later serve the file to a remote client and delete it after
	// delivery. Fires before the skip-check below so an already-present file
	// remains servable. Best-effort: d.Progress is always set (CLI or server).
	if d.Progress != nil {
		d.Progress.Output(filepath.Base(outputFile), outputFile)
	}

	if _, err := os.Stat(outputFile); err == nil {
		d.Progress.Printf("Episode %v is already downloaded, skipping...\n", info.EpisodeMetadata.EpisodeNumber)
		return nil
	}

	// Fetch series metadata + HD cover best-effort: enriches the mux tags
	// (genres, synopsis, air date, maturity) and attaches the cover. A missing
	// series or image never breaks a download — the cover is just skipped. The
	// cover is the series poster (the same art across every episode, falling
	// back to the episode's own art only when the series has none), so
	// pickCoverImage is called after info.Series is resolved. The temp cover file
	// is cleaned up by the mux step (and by this defer as a safety net for paths
	// that return before muxing).
	var coverFile string
	if seriesID := info.EpisodeMetadata.SeriesID; seriesID != "" {
		if series, err := d.API.GetSeries(seriesID); err == nil {
			info.Series = series
		}
	}
	if img, ok := pickCoverImage(info); ok {
		coverFile, _ = d.API.DownloadImage(img)
	}
	if coverFile != "" {
		defer os.Remove(coverFile)
	}

	// Copy the language lists so the "all" expansion below is local to this
	// episode and doesn't mutate the Downloader fields shared across episodes.
	audioLangs := append([]string(nil), d.AudioLangs...)
	subsLangs := append([]string(nil), d.SubsLangs...)

	// Resolve each requested audio locale to its version GUID. Each dub is a
	// separate playback stream with its own manifest, token and Widevine keys.
	guidByLocale := map[string]string{}
	if info.EpisodeMetadata.AudioLocale != "" {
		guidByLocale[info.EpisodeMetadata.AudioLocale] = baseContentId
	}
	for _, v := range info.EpisodeMetadata.Versions {
		guidByLocale[v.AudioLocale] = v.GUID
	}

	if len(audioLangs) == 1 && audioLangs[0] == "all" {
		audioLangs = make([]string, 0, len(guidByLocale))
		if primaryLocale := info.EpisodeMetadata.AudioLocale; primaryLocale != "" {
			if _, ok := guidByLocale[primaryLocale]; ok {
				audioLangs = append(audioLangs, primaryLocale)
			}
		}
		for locale := range guidByLocale {
			if locale != info.EpisodeMetadata.AudioLocale {
				audioLangs = append(audioLangs, locale)
			}
		}
		if len(audioLangs) > 1 {
			sort.Strings(audioLangs[1:])
		}
	}

	type audioVersion struct {
		locale    string
		contentId string
	}
	var versions []audioVersion
	for _, locale := range audioLangs {
		guid, ok := guidByLocale[locale]
		if !ok {
			return fmt.Errorf("audio locale %s not available for episode %v", locale, info.EpisodeMetadata.EpisodeNumber)
		}
		versions = append(versions, audioVersion{locale: locale, contentId: guid})
	}

	d.Progress.Printf("Downloading: %s (S%02vE%02v) from %s\n", info.Title, info.EpisodeMetadata.SeasonNumber, info.EpisodeMetadata.EpisodeNumber, info.EpisodeMetadata.SeriesTitle)

	// activeStreams tracks every playback token we open so we can release them
	// all if anything fails partway through. The release is best-effort: a
	// transport error here must not mask the real download error returned below.
	activeStreams := map[string]string{}
	defer func() {
		d.Progress.Printf("Cleaning up...")

		for id, sToken := range activeStreams {
			_, _ = d.API.DeleteStream(id, sToken)
		}
	}()

	// Fetch the first version's playback first so we can validate subtitle
	// availability before downloading anything heavy.
	firstEpisode, err := d.API.GetEpisode(versions[0].contentId)
	if err != nil {
		return err
	}
	activeStreams[versions[0].contentId] = firstEpisode.Token

	if len(subsLangs) == 1 && subsLangs[0] == "all" {
		subsLangs = make([]string, 0, len(firstEpisode.Subtitles))
		for locale, sub := range firstEpisode.Subtitles {
			if sub != nil && sub.URL != "" {
				subsLangs = append(subsLangs, locale)
			}
		}
		sort.Strings(subsLangs)
	}

	d.Progress.Printf("Audio locales: %s | Subtitle locales: %s\n", strings.Join(audioLangs, ", "), strings.Join(subsLangs, ", "))

	for _, locale := range subsLangs {
		if firstEpisode.Subtitles[locale] == nil {
			return fmt.Errorf("subtitle locale %s not available for episode %v", locale, info.EpisodeMetadata.EpisodeNumber)
		}
	}

	var subTracks []mux.MediaTrack
	if len(subsLangs) > 0 {
		d.Progress.Phase("subtitles")
	}
	for _, locale := range subsLangs {
		if err := ctx.Err(); err != nil {
			return err
		}
		d.Progress.Printf("Downloading subtitles for %s...\n", output.TrackTitle(locale))
		f, err := d.downloadSubtitles(ctx, firstEpisode.Subtitles[locale].URL)
		if err != nil {
			return err
		}
		// Guard against a silently empty subtitle track: ffmpeg muxes a .ass
		// with no usable Dialogue into an empty stream (present in the file,
		// but zero cues, so players show nothing) and reports success.
		// Crunchyroll lists a locale by URL only, so an empty or signs-only
		// .ass it serves would otherwise slip through. Validate the content
		// and skip the locale loudly instead of muxing an empty track.
		ok, vErr := assHasUsableDialogue(f)
		if vErr != nil {
			_ = os.Remove(f)
			return fmt.Errorf("validate subtitle %s: %w", locale, vErr)
		}
		if !ok {
			d.Progress.Printf("! Subtitle track for %s has no dialogue events; skipping it so we don't mux an empty track.\n", output.TrackTitle(locale))
			_ = os.Remove(f)
			continue
		}
		subTracks = append(subTracks, mux.MediaTrack{File: f, Locale: locale})
	}
	if len(subTracks) > 0 {
		d.Progress.Printf("Downloaded subtitles!\n")
	}

	var videoFile string
	var audioTracks []mux.MediaTrack

	for i, version := range versions {
		// Cancellation boundary between versions: a cancelled job stops here
		// rather than fetching the next manifest/license. The API methods
		// themselves don't take a context, so this check (plus the
		// download-track ctx below) is where cancel takes effect.
		if err := ctx.Err(); err != nil {
			return err
		}
		episode := firstEpisode
		if i > 0 {
			episode, err = d.API.GetEpisode(version.contentId)
			if err != nil {
				return err
			}
			activeStreams[version.contentId] = episode.Token
		}

		manifestData, err := d.API.ParseManifest(episode.ManifestURL)
		if err != nil {
			return err
		}
		pssh := manifest.GetPSSH(manifestData)
		if pssh == nil {
			return fmt.Errorf("PSSH not found")
		}
		// GetLicense returns this version's keys; audio for this version must be
		// downloaded before the next license so the keys still match.
		versionKeys, err := d.API.GetLicense(*pssh, version.contentId, episode.Token)
		if err != nil {
			return fmt.Errorf("getLicense for %s: %s", version.locale, err)
		}

		audioSet, err := manifest.FindAdaptationSet(manifestData, "audio")
		if err != nil {
			return fmt.Errorf("audio set: %s", err)
		}
		d.Progress.Printf("Downloading %s audio...\n", output.TrackTitle(version.locale))
		d.Progress.Phase("audio")
		audioBaseUrl, audioRepresentationId := manifest.GetBaseURL(audioSet, false, *audioQuality)
		if audioBaseUrl == nil {
			return fmt.Errorf("failed to get the audio base URL for %s, maybe the audio quality you entered is wrong?", version.locale)
		}
		audioFile, err := d.downloadTrack(ctx, audioBaseUrl, audioRepresentationId, audioSet, versionKeys)
		if err != nil {
			return err
		}
		audioTracks = append(audioTracks, mux.MediaTrack{File: audioFile, Locale: version.locale})

		// The video track is identical across dubs, so download it once using
		// the first version's keys (already loaded above).
		if i == 0 {
			videoSet, err := manifest.FindAdaptationSet(manifestData, "video")
			if err != nil {
				return fmt.Errorf("video set: %s", err)
			}
			d.Progress.Phase("video")
			d.Progress.Printf("Downloading video...\n")
			baseUrl, representationId := manifest.GetBaseURL(videoSet, true, *videoQuality)
			if baseUrl == nil {
				return fmt.Errorf("failed to get the video base URL, maybe the video quality you entered is wrong?")
			}
			videoFile, err = d.downloadTrack(ctx, baseUrl, representationId, videoSet, versionKeys)
			if err != nil {
				return err
			}
		}

		if success, _ := d.API.DeleteStream(version.contentId, episode.Token); !success {
			d.Progress.Printf("Failed to remove the player stream, you will probably have issues downloading other episodes.\n")
		}
		delete(activeStreams, version.contentId)
	}

	d.Progress.Phase("mux")
	return d.merge(ctx, videoFile, audioTracks, subTracks, outputFile, coverFile, d.Format, info)
}

// EpisodeInfoFromSeasonEpisode builds the EpisodeInfo the per-episode download
// path needs from a season-episode list entry, copying the rich W1 fields
// (series/season ids, air date, duration, maturity, subtitle locales, dub
// versions) into EpisodeMetadata. Shared by Downloader.Season and the server's
// per-episode batch tasks so the two never drift apart.
func EpisodeInfoFromSeasonEpisode(episode media.SeasonEpisode) media.EpisodeInfo {
	return media.EpisodeInfo{
		EpisodeMetadata: media.EpisodeMetadata{
			SeriesTitle:        episode.SeriesTitle,
			SeriesID:           episode.SeriesID,
			SeasonNumber:       episode.SeasonNumber,
			SeasonTitle:        episode.SeasonTitle,
			EpisodeNumber:      episode.EpisodeNumber,
			AudioLocale:        episode.AudioLocale,
			Versions:           episode.Versions,
			AvailabilityStarts: episode.AvailabilityStarts,
			EpisodeAirDate:     episode.EpisodeAirDate,
			DurationMS:         episode.DurationMS,
			IsPremiumOnly:      episode.IsPremiumOnly,
			MaturityRatings:    episode.MaturityRatings,
			SubtitleLocales:    episode.SubtitleLocales,
			Images:             episode.Images,
		},
		Title:       episode.Title,
		Description: episode.Description,
	}
}

// pickCoverImage resolves the cover art source for an episode, preferring the
// series poster (portrait art) — the same cover across every episode of a
// series — then the episode's own portrait art, then the episode thumbnail as a
// last resort. Returns the image source URL and ok=true when any art is
// available; (img, false) means no cover attaches.
func pickCoverImage(info media.EpisodeInfo) (string, bool) {
	if img, ok := media.BestImage(info.Series.Images.PosterTall); ok {
		return img.Source, true
	}
	if img, ok := media.BestImage(info.EpisodeMetadata.Images.PosterTall); ok {
		return img.Source, true
	}
	if img, ok := media.BestImage(info.EpisodeMetadata.Images.Thumbnail); ok {
		return img.Source, true
	}
	return "", false
}

// Season downloads every episode in a season list, building each episode's
// EpisodeInfo from its SeasonEpisode entry. A failed episode is logged and
// skipped so one bad episode can't abort the whole season. The CLI never cancels,
// so it runs each episode under a background context; the per-episode output and
// skip-and-continue behavior are unchanged.
func (d *Downloader) Season(episodes []media.SeasonEpisode) error {
	d.Progress.Printf("Downloading season %v of %s (%v episodes)\n\n", episodes[0].SeasonNumber, episodes[0].SeriesTitle, len(episodes))

	ctx := context.Background()
	for _, episode := range episodes {
		info := EpisodeInfoFromSeasonEpisode(episode)

		if err := d.Episode(ctx, episode.ID, info); err != nil {
			d.Progress.Printf("! Episode %v failed: %v\n", episode.EpisodeNumber, err)
			continue
		}
	}
	return nil
}
