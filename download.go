package main

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
	"github.com/unki2aut/go-mpd"
)

const maxWorkers = 10

func buildUrl(base, representationId, file string, partNum *int64) string {
	if partNum != nil {
		file = strings.ReplaceAll(file, "$Number$", fmt.Sprintf("%05d", *partNum))
		file = strings.ReplaceAll(file, "$Number%05d$", fmt.Sprintf("%05d", *partNum))
	}
	return base + strings.ReplaceAll(file, "$RepresentationID$", representationId)
}

func downloadPart(url string) ([]byte, error) {
	maxRetries := 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}

		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Origin", "https://static.crunchyroll.com")
		req.Header.Set("Referer", "https://static.crunchyroll.com/")
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:147.0) Gecko/20100101 Firefox/147.0")
		resp, err := sharedClient.Do(req)
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

func downloadParts(baseUrl, representationId *string, set *mpd.AdaptationSet) (string, error) {
	initUrl := buildUrl(*baseUrl, *representationId, *set.SegmentTemplate.Initialization, nil)
	initData, err := downloadPart(initUrl)
	if err != nil {
		return "", err
	}

	timeline := expandTimeline(set.SegmentTemplate.SegmentTimeline.S, 1)
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

	for w := 0; w < maxWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				data, err := downloadPart(job.url)
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
	if err := decryptSegmentsToFile(initData, segFiles, filename); err != nil {
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
func decryptSegmentsToFile(initData []byte, segFiles []string, outputFile string) error {
	key, err := contentKey(keys)
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

func downloadSubs(url string) string {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Origin", "https://static.crunchyroll.com")
	req.Header.Set("Referer", "https://static.crunchyroll.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:147.0) Gecko/20100101 Firefox/147.0")
	resp, err := sharedClient.Do(req)
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

// sanitize replaces characters that are illegal in Windows filenames (or break
// the final path) with underscores, collapses repeated underscores, and trims
// trailing spaces/dots. An empty string becomes "Unknown".
func sanitize(s string) string {
	if s == "" {
		return "Unknown"
	}

	// Characters that are illegal in Windows filenames or break the final path
	illegal := []string{"\\", "/", ":", "*", "?", "\"", "<", ">", "|", "'", "’", "`", "“", "”"}
	res := s
	for _, char := range illegal {
		res = strings.ReplaceAll(res, char, "_")
	}
	for strings.Contains(res, "__") {
		res = strings.ReplaceAll(res, "__", "_")
	}
	return strings.TrimRight(res, " .")
}

func downloadEpisode(baseContentId string, info EpisodeInfo, audioLangs, subsLangs []string, videoQuality, audioQuality *string) {
	cleanSeriesTitle := sanitize(info.EpisodeMetadata.SeriesTitle)
	cleanEpisodeTitle := sanitize(info.Title)

	if _, err := os.Stat(cleanSeriesTitle); err != nil {
		_ = os.MkdirAll(cleanSeriesTitle, 0777)
	}

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
			deleteStream(id, sToken)
		}
		if r := recover(); r != nil {
			fmt.Printf("Recovered from error: %v\n", r)
		}
	}()

	// Fetch the first version's playback first so we can validate subtitle
	// availability before downloading anything heavy.
	firstEpisode := getEpisode(versions[0].contentId)
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

	var subTracks []mediaTrack
	for _, locale := range subsLangs {
		fmt.Printf("Downloading subtitles for %s...\n", trackTitle(locale))
		subTracks = append(subTracks, mediaTrack{file: downloadSubs(firstEpisode.Subtitles[locale].URL), locale: locale})
	}
	if len(subTracks) > 0 {
		fmt.Println("Downloaded subtitles!")
	}

	var videoFile string
	var audioTracks []mediaTrack

	for i, version := range versions {
		episode := firstEpisode
		if i > 0 {
			episode = getEpisode(version.contentId)
			activeStreams[version.contentId] = episode.Token
		}

		manifest := parseManifest(episode.ManifestURL)
		pssh := getPssh(manifest)
		if pssh == nil {
			panic("PSSH not found")
		}
		// getLicense stores the keys in the global "keys" used by downloadParts,
		// so audio for this version must be downloaded before the next license.
		if err := getLicense(*pssh, version.contentId, episode.Token); err != nil {
			panic(fmt.Sprintf("getLicense for %s: %s", version.locale, err))
		}

		audioSet, err := findAdaptationSet(manifest, "audio")
		if err != nil {
			panic(fmt.Sprintf("audio set: %s", err))
		}
		fmt.Printf("Downloading %s audio...\n", trackTitle(version.locale))
		audioBaseUrl, audioRepresentationId := getBaseUrl(audioSet, false, *audioQuality)
		if audioBaseUrl == nil {
			panic(fmt.Sprintf("failed to get the audio base URL for %s, maybe the audio quality you entered is wrong?", version.locale))
		}
		audioFile, err := downloadParts(audioBaseUrl, audioRepresentationId, audioSet)
		if err != nil {
			panic(err)
		}
		audioTracks = append(audioTracks, mediaTrack{file: audioFile, locale: version.locale})

		// The video track is identical across dubs, so download it once using
		// the first version's keys (already loaded above).
		if i == 0 {
			videoSet, err := findAdaptationSet(manifest, "video")
			if err != nil {
				panic(fmt.Sprintf("video set: %s", err))
			}
			fmt.Println("Downloading video...")
			baseUrl, representationId := getBaseUrl(videoSet, true, *videoQuality)
			if baseUrl == nil {
				panic("failed to get the video base URL, maybe the video quality you entered is wrong?")
			}
			videoFile, err = downloadParts(baseUrl, representationId, videoSet)
			if err != nil {
				panic(err)
			}
		}

		if success := deleteStream(version.contentId, episode.Token); !success {
			fmt.Print("Failed to remove the player stream, you will probably have issues downloading other episodes.\n")
		}
		delete(activeStreams, version.contentId)
	}

	mergeEverything(videoFile, audioTracks, subTracks, outputFile, info)
}

func downloadSeason(videoQuality, audioQuality *string, audioLangs, subsLangs []string, episodes []SeasonEpisode) {
	fmt.Printf("Downloading season %v of %s (%v episodes)\n\n", episodes[0].SeasonNumber, episodes[0].SeriesTitle, len(episodes))

	for _, episode := range episodes {
		info := EpisodeInfo{
			EpisodeMetadata: EpisodeMetadata{
				SeriesTitle:        episode.SeriesTitle,
				SeasonNumber:       episode.SeasonNumber,
				EpisodeNumber:      episode.EpisodeNumber,
				AudioLocale:        episode.AudioLocale,
				Versions:           episode.Versions,
				AvailabilityStarts: episode.AvailabilityStarts,
			},
			Title: episode.Title,
		}

		downloadEpisode(episode.ID, info, audioLangs, subsLangs, videoQuality, audioQuality)
	}
}
