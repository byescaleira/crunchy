// Package download orchestrates fetching, decrypting and assembling an
// episode: downloading media segments in parallel, decrypting them one at a
// time straight to temp files (to keep memory bounded), downloading subtitles,
// and driving the mux step. A Downloader bundles the Crunchyroll API client
// with the user's quality/language selections so the CLI and the server share
// one code path.
package download

import (
	"bytes"
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

// API is the slice of the Crunchyroll client that Episode/Season need. The
// concrete *crunchy.Client satisfies it; tests substitute a fake to exercise
// the orchestration without network, Widevine, or ffmpeg.
type API interface {
	GetEpisode(id string) media.Episode
	GetEpisodeInfo(id string) media.EpisodeInfo
	GetSeasons(contentId, audioLocale, subLocale string) []media.Season
	GetSeasonEpisodes(contentId, audioLocale, subLocale string) []media.SeasonEpisode
	DeleteStream(contentId, sToken string) bool
	ParseManifest(url string) *mpd.MPD
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

	// The I/O steps are overridable func fields so the orchestration (the
	// keys-ordering invariant, the per-track sequence) can be tested without
	// network, Widevine, or ffmpeg. They default to the real implementations
	// the first time a download runs (see ensureSeams).
	downloadTrack     func(baseUrl, representationId *string, set *mpd.AdaptationSet, keys []*widevine.Key) (string, error)
	downloadSubtitles func(url string) string
	merge             func(videoFile string, audioTracks, subTracks []mux.MediaTrack, outputFile string, info media.EpisodeInfo)
}

// workers returns the configured segment-download parallelism, defaulting to 10
// (the former package-level const) when unset.
func (d *Downloader) workers() int {
	if d.MaxWorkers > 0 {
		return d.MaxWorkers
	}
	return 10
}

// ensureSeams fills in the default I/O implementations and HTTP client. It is
// idempotent so callers (and tests) only override the seams they care about.
func (d *Downloader) ensureSeams() {
	if d.HTTP == nil {
		d.HTTP = crunchy.SharedClient
	}
	if d.downloadTrack == nil {
		d.downloadTrack = d.downloadParts
	}
	if d.downloadSubtitles == nil {
		d.downloadSubtitles = func(url string) string { return downloadSubs(d.HTTP, url) }
	}
	if d.merge == nil {
		d.merge = mux.Merge
	}
}

func buildUrl(base, representationId, file string, partNum *int64) string {
	if partNum != nil {
		file = strings.ReplaceAll(file, "$Number$", fmt.Sprintf("%05d", *partNum))
		file = strings.ReplaceAll(file, "$Number%05d$", fmt.Sprintf("%05d", *partNum))
	}
	return base + strings.ReplaceAll(file, "$RepresentationID$", representationId)
}

func downloadPart(doer crunchy.Doer, url string) ([]byte, error) {
	maxRetries := 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}

		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", crunchy.UserAgent)
		req.Header.Set("Origin", "https://static.crunchyroll.com")
		req.Header.Set("Referer", "https://static.crunchyroll.com/")
		resp, err := doer.Do(req)
		if err != nil {
			if attempt < maxRetries-1 {
				continue
			}
			return nil, fmt.Errorf("failed after %d retries: %w", maxRetries, err)
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			if attempt < maxRetries-1 {
				continue
			}
			return nil, fmt.Errorf("failed after %d retries, status: %d", maxRetries, resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
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
// files so a heavy episode never keeps the whole file in RAM.
func (d *Downloader) downloadParts(baseUrl, representationId *string, set *mpd.AdaptationSet, keys []*widevine.Key) (string, error) {
	initUrl := buildUrl(*baseUrl, *representationId, *set.SegmentTemplate.Initialization, nil)
	initData, err := downloadPart(d.HTTP, initUrl)
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
			for job := range jobs {
				data, err := downloadPart(d.HTTP, job.url)
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
				fmt.Printf("\rDownloaded %v of %v segments (%v%%)", count, total, (100*count)/int64(total))
			}
		}()
	}

	for i, item := range timeline {
		url := buildUrl(*baseUrl, *representationId, *set.SegmentTemplate.Media, &item)
		jobs <- segmentJob{index: i, url: url}
	}
	close(jobs)
	wg.Wait()

	if downloadErr != nil {
		removeFiles(segFiles)
		return "", downloadErr
	}

	fmt.Println("\nFinished downloading!")

	// Decrypt segment-by-segment straight from the temp files into the output
	// file. This keeps only one segment in memory at a time during decryption,
	// instead of decoding the whole file into RAM like widevine.DecryptMP4Auto.
	filename := getFilename(set)
	if err := decryptSegmentsToFile(initData, segFiles, filename, keys); err != nil {
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
func decryptSegmentsToFile(initData []byte, segFiles []string, outputFile string, keys []*widevine.Key) error {
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

func downloadSubs(doer crunchy.Doer, url string) string {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("User-Agent", crunchy.UserAgent)
	req.Header.Set("Origin", "https://static.crunchyroll.com")
	req.Header.Set("Referer", "https://static.crunchyroll.com/")
	resp, err := doer.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}

	filename := getFilename(nil)
	file, err := os.Create(filename)
	if err != nil {
		panic(err)
	}
	file.Write(body)
	file.Close()

	return filename
}

// Episode downloads and muxes a single episode: its subtitles, every requested
// audio dub, and the video track, into a single MKV named after the series and
// episode. It mutates copies of the language selections (so "all" expands per
// episode), leaving the Downloader's fields untouched for the next episode.
//
// The Widevine keys-ordering invariant is enforced structurally here: each
// version's keys are loop-local (versionKeys), and the audio for version i is
// downloaded (and thus decrypted) synchronously before the loop advances to
// version i+1's GetLicense. The video track is downloaded once with version 0's
// keys.
func (d *Downloader) Episode(baseContentId string, info media.EpisodeInfo) {
	d.ensureSeams()

	cleanSeriesTitle := output.Sanitize(info.EpisodeMetadata.SeriesTitle)
	cleanEpisodeTitle := output.Sanitize(info.Title)

	if _, err := os.Stat(cleanSeriesTitle); err != nil {
		_ = os.MkdirAll(cleanSeriesTitle, 0777)
	}

	videoQuality := &d.VideoQuality
	audioQuality := &d.AudioQuality

	outputFile := filepath.Join(cleanSeriesTitle, fmt.Sprintf("%s S%02dE%02d - %s [%s].mkv",
		cleanSeriesTitle,
		info.EpisodeMetadata.SeasonNumber,
		info.EpisodeMetadata.EpisodeNumber,
		cleanEpisodeTitle,
		*videoQuality,
	))

	if _, err := os.Stat(outputFile); err == nil {
		fmt.Printf("Episode %v is already downloaded, skipping...\n", info.EpisodeMetadata.EpisodeNumber)
		return
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
			fmt.Printf("! Audio locale %s is not available for episode %v, aborting this episode.\n", locale, info.EpisodeMetadata.EpisodeNumber)
			return
		}
		versions = append(versions, audioVersion{locale: locale, contentId: guid})
	}

	fmt.Printf("Downloading: %s (S%02vE%02v) from %s\n", info.Title, info.EpisodeMetadata.SeasonNumber, info.EpisodeMetadata.EpisodeNumber, info.EpisodeMetadata.SeriesTitle)

	// activeStreams tracks every playback token we open so we can release them
	// all if anything fails partway through.
	activeStreams := map[string]string{}
	defer func() {
		fmt.Print("Cleaning up...")

		for id, sToken := range activeStreams {
			d.API.DeleteStream(id, sToken)
		}
		if r := recover(); r != nil {
			fmt.Printf("Recovered from error: %v\n", r)
		}
	}()

	// Fetch the first version's playback first so we can validate subtitle
	// availability before downloading anything heavy.
	firstEpisode := d.API.GetEpisode(versions[0].contentId)
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

	fmt.Printf("Audio locales: %s | Subtitle locales: %s\n", strings.Join(audioLangs, ", "), strings.Join(subsLangs, ", "))

	for _, locale := range subsLangs {
		if firstEpisode.Subtitles[locale] == nil {
			fmt.Printf("! Subtitle locale %s is not available for episode %v, aborting this episode.\n", locale, info.EpisodeMetadata.EpisodeNumber)
			return
		}
	}

	var subTracks []mux.MediaTrack
	for _, locale := range subsLangs {
		fmt.Printf("Downloading subtitles for %s...\n", output.TrackTitle(locale))
		subTracks = append(subTracks, mux.MediaTrack{File: d.downloadSubtitles(firstEpisode.Subtitles[locale].URL), Locale: locale})
	}
	if len(subTracks) > 0 {
		fmt.Println("Downloaded subtitles!")
	}

	var videoFile string
	var audioTracks []mux.MediaTrack

	for i, version := range versions {
		episode := firstEpisode
		if i > 0 {
			episode = d.API.GetEpisode(version.contentId)
			activeStreams[version.contentId] = episode.Token
		}

		manifestData := d.API.ParseManifest(episode.ManifestURL)
		pssh := manifest.GetPSSH(manifestData)
		if pssh == nil {
			panic("PSSH not found")
		}
		// GetLicense returns this version's keys; audio for this version must be
		// downloaded before the next license so the keys still match.
		versionKeys, err := d.API.GetLicense(*pssh, version.contentId, episode.Token)
		if err != nil {
			panic(fmt.Sprintf("getLicense for %s: %s", version.locale, err))
		}

		audioSet, err := manifest.FindAdaptationSet(manifestData, "audio")
		if err != nil {
			panic(fmt.Sprintf("audio set: %s", err))
		}
		fmt.Printf("Downloading %s audio...\n", output.TrackTitle(version.locale))
		audioBaseUrl, audioRepresentationId := manifest.GetBaseURL(audioSet, false, *audioQuality)
		if audioBaseUrl == nil {
			panic(fmt.Sprintf("failed to get the audio base URL for %s, maybe the audio quality you entered is wrong?", version.locale))
		}
		audioFile, err := d.downloadTrack(audioBaseUrl, audioRepresentationId, audioSet, versionKeys)
		if err != nil {
			panic(err)
		}
		audioTracks = append(audioTracks, mux.MediaTrack{File: audioFile, Locale: version.locale})

		// The video track is identical across dubs, so download it once using
		// the first version's keys (already loaded above).
		if i == 0 {
			videoSet, err := manifest.FindAdaptationSet(manifestData, "video")
			if err != nil {
				panic(fmt.Sprintf("video set: %s", err))
			}
			fmt.Println("Downloading video...")
			baseUrl, representationId := manifest.GetBaseURL(videoSet, true, *videoQuality)
			if baseUrl == nil {
				panic("failed to get the video base URL, maybe the video quality you entered is wrong?")
			}
			videoFile, err = d.downloadTrack(baseUrl, representationId, videoSet, versionKeys)
			if err != nil {
				panic(err)
			}
		}

		if success := d.API.DeleteStream(version.contentId, episode.Token); !success {
			fmt.Print("Failed to remove the player stream, you will probably have issues downloading other episodes.\n")
		}
		delete(activeStreams, version.contentId)
	}

	d.merge(videoFile, audioTracks, subTracks, outputFile, info)
}

// Season downloads every episode in a season list, building each episode's
// EpisodeInfo from its SeasonEpisode entry.
func (d *Downloader) Season(episodes []media.SeasonEpisode) {
	fmt.Printf("Downloading season %v of %s (%v episodes)\n\n", episodes[0].SeasonNumber, episodes[0].SeriesTitle, len(episodes))

	for _, episode := range episodes {
		info := media.EpisodeInfo{
			EpisodeMetadata: media.EpisodeMetadata{
				SeriesTitle:        episode.SeriesTitle,
				SeasonNumber:       episode.SeasonNumber,
				EpisodeNumber:      episode.EpisodeNumber,
				AudioLocale:        episode.AudioLocale,
				Versions:           episode.Versions,
				AvailabilityStarts: episode.AvailabilityStarts,
			},
			Title: episode.Title,
		}

		d.Episode(episode.ID, info)
	}
}
