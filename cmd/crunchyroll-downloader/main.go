// crunchyroll-downloader is the original command-line downloader. It builds a
// crunchy.Client, resolves the requested URL, and drives a download.Downloader
// over it. Output is byte-identical to the pre-refactor CLI; the server binary
// (cmd/crunchy-server) reuses the same internal packages.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"crunchyroll-downloader/internal/crunchy"
	"crunchyroll-downloader/internal/download"
)

var (
	audioLang     = flag.String("audio-lang", "ja-JP", "Audio language(s), comma-separated for multiple (e.g. \"ja-JP,en-US\"). First is the default track")
	subtitlesLang = flag.String("subs-lang", "en-US", "Subtitle language(s), comma-separated for multiple (e.g. \"en-US,es-419\"). First is the default track")
	videoQuality  = flag.String("video-quality", "1080p", "Video quality")
	audioQuality  = flag.String("audio-quality", "192k", "Audio quality")
	seasonNumber  = flag.Int("season", 0, "Season number. Not used if an episode link is entered")
	etpRt         = flag.String("etp-rt", "", "The \"etp_rt\" cookie value of your account")
	debug         = flag.Bool("debug-manifest", false, "Log raw episode playback JSON and manifest XML")
)

// parseLangs splits a comma-separated locale list, trimming spaces and dropping
// empties.
func parseLangs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// processUrl resolves one URL to its content and drives the download over d.
// It returns an error for any non-recoverable failure so main's batch loop can
// log it and continue to the next URL instead of aborting the whole run. The
// per-episode and per-season error boundaries (one bad episode skipping the
// rest) live inside download.Downloader.
func processUrl(c *crunchy.Client, url string, d *download.Downloader, seasonNumber int) error {
	contentType, contentId, err := crunchy.ParseContentURL(url)
	if err != nil {
		return err
	}

	// The season/series API endpoints take a single preferred locale; use the
	// primary (first) requested one. All dub versions are still listed per
	// episode, so the other languages remain resolvable.
	primaryAudio := d.AudioLangs[0]
	primarySubs := "en-US"
	if len(d.SubsLangs) > 0 {
		primarySubs = d.SubsLangs[0]
	}

	if contentType == "watch" {
		info, err := c.GetEpisodeInfo(contentId)
		if err != nil {
			return err
		}
		return d.Episode(contentId, info)
	}

	seasons, err := c.GetSeasons(contentId, primaryAudio, primarySubs)
	if err != nil {
		return err
	}

	if seasonNumber != 0 {
		var seasonId string
		for _, season := range seasons {
			if season.SeasonNumber == seasonNumber {
				seasonId = season.ID
				break
			}
		}
		if seasonId == "" {
			return fmt.Errorf("This anime has no season %v!", seasonNumber)
		}

		episodes, err := c.GetSeasonEpisodes(seasonId, primaryAudio, primarySubs)
		if err != nil {
			return err
		}
		return d.Season(episodes)
	}

	fmt.Print("No season number specified, downloading all seasons...\n")
	for _, season := range seasons {
		episodes, err := c.GetSeasonEpisodes(season.ID, primaryAudio, primarySubs)
		if err != nil {
			return err
		}
		if err := d.Season(episodes); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	url := flag.String("url", "", "URL of the episode/season to download")
	urlsFile := flag.String("file", "", "Path to a text file with one URL per line")
	flag.Parse()

	if *url == "" && *urlsFile == "" {
		flag.Usage()
		os.Exit(1)
	}

	if *etpRt == "" {
		fmt.Println("You must specify the \"-etp-rt\" option!\n- Open Crunchyroll on your browser and log in.\n- Open developer tools (Ctrl+Shift+I), go to \"Application\", and then \"Cookies\".\n- The value of the \"ept_rt\" cookie is what you need to input into this option.")
		os.Exit(1)
	}

	client, err := crunchy.NewClient(*etpRt, *debug)
	if err != nil {
		fmt.Printf("Failed to get access token: %s\n", err)
		os.Exit(1)
	}

	audioLangs := parseLangs(*audioLang)
	if len(audioLangs) == 0 {
		audioLangs = []string{"ja-JP"}
	}
	subsLangs := parseLangs(*subtitlesLang)

	d := &download.Downloader{
		API:          client,
		VideoQuality: *videoQuality,
		AudioQuality: *audioQuality,
		AudioLangs:   audioLangs,
		SubsLangs:    subsLangs,
		Debug:        *debug,
	}

	if *urlsFile != "" {
		file, err := os.Open(*urlsFile)
		if err != nil {
			fmt.Printf("Failed to open URLs file: %s\n", err)
			os.Exit(1)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		var urls []string
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && strings.HasPrefix(line, "http") {
				urls = append(urls, line)
			}
		}

		fmt.Printf("Found %d URLs to download\n\n", len(urls))
		for i, u := range urls {
			fmt.Printf("=== [%d/%d] %s ===\n", i+1, len(urls), u)
			if err := processUrl(client, u, d, *seasonNumber); err != nil {
				fmt.Printf("! %s failed: %v\n", u, err)
			}
			fmt.Println()
		}
	} else {
		if err := processUrl(client, *url, d, *seasonNumber); err != nil {
			fmt.Printf("! %s\n", err)
			os.Exit(1)
		}
	}
}
