package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"crunchyroll-downloader/internal/crunchy"
	"crunchyroll-downloader/internal/media"
	"crunchyroll-downloader/internal/web"
)

// valueOr returns the first form value for key, or def if empty.
func valueOr(r *http.Request, key, def string) string {
	if v := strings.TrimSpace(r.FormValue(key)); v != "" {
		return v
	}
	return def
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	maxConcurrent := s.cfg.MaxConcurrent
	s.mu.RUnlock()
	if maxConcurrent < 1 {
		maxConcurrent = 3
	}
	render(w, r, web.SettingsPage(s.Configured(), "", maxConcurrent))
}

func (s *Server) handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render(w, r, web.Alert("error", err.Error()))
		return
	}
	etpRt := strings.TrimSpace(r.FormValue("etpRt"))
	if etpRt == "" {
		render(w, r, web.Alert("error", "The etp_rt cookie is required."))
		return
	}

	if err := s.configure(etpRt, s.outputDir); err != nil {
		// Never echo the token itself; surface only the transport/token error.
		render(w, r, web.Alert("error", "Failed to verify token: "+err.Error()))
		return
	}
	render(w, r, web.Alert("success", "Token saved and verified."))
}

// handleSettingsDownloadsPost persists the max-concurrent-downloads preference.
// The value is clamped to 1..8; it is read only at startup to size the
// jobs.Manager, so the rendered alert reminds the user to restart for it to
// take effect. Only the MaxConcurrent field is touched — the token and other
// prefs are preserved (read-modify-write via s.cfg).
func (s *Server) handleSettingsDownloadsPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render(w, r, web.Alert("error", err.Error()))
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("maxConcurrent")))
	if err != nil || n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	s.mu.Lock()
	s.cfg.MaxConcurrent = n
	cfg := s.cfg
	s.mu.Unlock()
	if err := s.saveConfig(cfg); err != nil {
		render(w, r, web.Alert("error", "Could not save: "+err.Error()))
		return
	}
	render(w, r, web.Alert("success", fmt.Sprintf("Saved — %d concurrent downloads. Restart the server for it to take effect.", n)))
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		render(w, r, web.BrowsePage(nil, "Save your etp_rt token in Settings first."))
		return
	}
	s.mu.RLock()
	api := s.api
	s.mu.RUnlock()
	var popular []media.BrowsePanel
	if api != nil {
		popular, _ = api.BrowsePopular(36, 0) // best-effort; degrade to empty grid
	}
	render(w, r, web.BrowsePage(popular, ""))
}

// handleBrowsePost serves the unified Browse search box. The single `q` field
// is either a series URL (drills straight into the seasons list, reusing the
// existing series flow) or a title (searches the discover/search endpoint); an
// empty query re-shows the popular grid. Results swap into #seasons.
func (s *Server) handleBrowsePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render(w, r, web.PopularGrid(nil, "Could not parse the form."))
		return
	}
	q := strings.TrimSpace(r.FormValue("q"))

	if !s.Configured() {
		render(w, r, web.PopularGrid(nil, "Save your etp_rt token in Settings first."))
		return
	}

	s.mu.RLock()
	api := s.api
	s.mu.RUnlock()

	// A Crunchyroll series URL drills straight into the seasons list.
	if contentType, contentId, err := crunchy.ParseContentURL(q); err == nil {
		if contentType == "watch" {
			render(w, r, web.PopularGrid(nil, "That's an episode link. Browse from a /series/ page to pick episodes."))
			return
		}
		seasons, err := api.GetSeasons(contentId, "ja-JP", "en-US")
		if err != nil {
			render(w, r, web.BrowseSeriesResult(media.Series{}, contentId, nil, "Failed to list seasons: "+err.Error()))
			return
		}
		// Best-effort series hero: a missing series fetch degrades to the empty
		// hero (which still renders the Download-series button + the season log).
		series, _ := api.GetSeries(contentId)
		render(w, r, web.BrowseSeriesResult(series, contentId, seasons, ""))
		return
	}

	// Empty query → popular grid.
	if q == "" {
		panels, err := api.BrowsePopular(36, 0)
		if err != nil {
			render(w, r, web.PopularGrid(nil, "Popular anime is unavailable right now."))
			return
		}
		render(w, r, web.PopularGrid(panels, ""))
		return
	}

	// Otherwise treat q as a title search.
	hits, err := api.SearchSeries(q, 20)
	if err != nil || len(hits) == 0 {
		render(w, r, web.SearchResults(nil, "No results for \""+q+"\"."))
		return
	}
	render(w, r, web.SearchResults(hits, ""))
}

func (s *Server) handleSeasonEpisodes(w http.ResponseWriter, r *http.Request) {
	seasonID := r.PathValue("id")
	if !s.Configured() {
		render(w, r, web.EpisodesTable(nil, "Save your etp_rt token in Settings first."))
		return
	}

	s.mu.RLock()
	api := s.api
	s.mu.RUnlock()
	episodes, err := api.GetSeasonEpisodes(seasonID, "ja-JP", "en-US")
	if err != nil {
		render(w, r, web.EpisodesTable(nil, "Failed to list episodes: "+err.Error()))
		return
	}
	render(w, r, web.EpisodesTable(episodes, ""))
}

func (s *Server) handleDownloadNew(w http.ResponseWriter, r *http.Request) {
	kind := valueOr(r, "kind", "episode")
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	opts := s.downloadFormOpts(kind, id)
	if kind == "season" || kind == "series" {
		if !s.Configured() {
			s.renderDownloadForm422(w, r, opts, map[string]string{"_": "Save your etp_rt token in Settings first."})
			return
		}
		if summary, ok := s.batchSummary(kind, id); ok {
			opts.Summary = summary
		}
	}
	render(w, r, web.DownloadForm(opts, nil))
}

// batchSummary best-effort enriches the modal headline for a season or series
// target with real counts from the CMS (season N of <series>, M episodes; or
// <series>, N seasons, M episodes). Returns ok=false on any failure so the
// caller keeps the generic summary.
func (s *Server) batchSummary(kind, id string) (string, bool) {
	s.mu.RLock()
	api := s.api
	s.mu.RUnlock()
	if api == nil {
		return "", false
	}
	switch kind {
	case "season":
		episodes, err := api.GetSeasonEpisodes(id, "ja-JP", "en-US")
		if err != nil || len(episodes) == 0 {
			return "", false
		}
		ep := episodes[0]
		return fmt.Sprintf("Download season %v of %s (%v episodes)", ep.SeasonNumber, ep.SeriesTitle, len(episodes)), true
	case "series":
		seasons, err := api.GetSeasons(id, "ja-JP", "en-US")
		if err != nil || len(seasons) == 0 {
			return "", false
		}
		total := 0
		for _, s := range seasons {
			total += s.NumberOfEpisodes
		}
		title := id
		if series, err := api.GetSeries(id); err == nil && series.Title != "" {
			title = series.Title
		}
		return fmt.Sprintf("Download series %s (%v seasons, %v episodes)", title, len(seasons), total), true
	}
	return "", false
}

// downloadFormOpts builds the modal view-model for a kind/target. It pre-fills
// from the last-used options (persisted across restarts) and falls back per field
// to sensible defaults (ja-JP audio, en-US subs, 1080p/192k, .mkv, the session
// output dir) when a last-used field is empty. Used for both the initial GET and
// the 422 re-render, so a re-rendered form keeps the same pre-fill for the fields
// the user didn't touch.
func (s *Server) downloadFormOpts(kind, id string) web.DownloadFormOpts {
	last := s.lastDownloadOpts()
	videoQuality := last.VideoQuality
	if videoQuality == "" {
		videoQuality = "1080p"
	}
	audioQuality := last.AudioQuality
	if audioQuality == "" {
		audioQuality = "192k"
	}
	format := last.Format
	if format == "" {
		format = "mkv"
	}
	audio := last.AudioLangs
	if len(audio) == 0 {
		audio = []string{"ja-JP"}
	}
	subs := last.SubsLangs
	if len(subs) == 0 {
		subs = []string{"en-US"}
	}
	outputDir := last.OutputDir
	if outputDir == "" {
		outputDir = s.sessionOutputDir()
	}
	return web.DownloadFormOpts{
		Kind:          kind,
		ID:            id,
		Summary:       s.downloadSummary(kind, id),
		VideoQuality:  videoQuality,
		AudioQuality:  audioQuality,
		Format:        format,
		SelectedAudio: audio,
		SelectedSubs:  subs,
		OutputDir:     outputDir,
	}
}

// downloadSummary returns the modal headline for a target. For an episode it
// best-effort resolves the title; season/series get a generic headline here
// (enriched with counts by batchSummary on the GET path).
func (s *Server) downloadSummary(kind, id string) string {
	switch kind {
	case "season":
		return "Download season"
	case "series":
		return "Download series"
	default:
		if title, _, _, _ := s.episodeMeta(id); title != "" && title != id {
			return "Download episode — " + title
		}
		return "Download episode"
	}
}

// episodeMeta best-effort resolves an episode's display metadata (title,
// thumbnail URL, season/episode numbers) for the job card and the modal summary.
// It never errors: any failure (no token, network, missing) falls back to the
// id for the title and zero values for the rest. It is a single GetEpisodeInfo
// round-trip — the only extra fetch the per-episode path makes.
func (s *Server) episodeMeta(id string) (title, imageURL string, season, episode int) {
	s.mu.RLock()
	api := s.api
	s.mu.RUnlock()
	if api == nil {
		return id, "", 0, 0
	}
	if info, err := api.GetEpisodeInfo(id); err == nil {
		if info.Title != "" {
			title = info.Title
		} else {
			title = id
		}
		imageURL = bestThumb(info.EpisodeMetadata.Images.Thumbnail)
		season = info.EpisodeMetadata.SeasonNumber
		episode = info.EpisodeMetadata.EpisodeNumber
		return
	}
	return id, "", 0, 0
}

// sessionOutputDir returns the configured output directory (under the read lock).
func (s *Server) sessionOutputDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.outputDir
}

// renderDownloadForm422 re-renders the modal form with errs and a 422 status, so
// htmx swaps it back into #download-modal-content and the dialog stays open.
func (s *Server) renderDownloadForm422(w http.ResponseWriter, r *http.Request, opts web.DownloadFormOpts, errs map[string]string) {
	w.WriteHeader(http.StatusUnprocessableEntity)
	render(w, r, web.DownloadForm(opts, errs))
}

func (s *Server) handleDownloadPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderDownloadForm422(w, r, s.downloadFormOpts("episode", ""), map[string]string{"_": "Could not parse the form."})
		return
	}
	kind := valueOr(r, "kind", "episode")
	id := strings.TrimSpace(r.FormValue("id"))
	audio := r.Form["audioLangs"]
	subs := r.Form["subsLangs"]

	errs := map[string]string{}
	if id == "" {
		errs["_"] = "No target selected."
	}
	if len(audio) == 0 {
		errs["audio"] = "Pick at least one audio language."
	}
	if len(errs) > 0 {
		opts := s.downloadFormOpts(kind, id)
		opts.VideoQuality = valueOr(r, "videoQuality", "1080p")
		opts.AudioQuality = valueOr(r, "audioQuality", "192k")
		opts.Format = valueOr(r, "format", "mkv")
		opts.SelectedAudio = audio
		opts.SelectedSubs = subs
		if dir := strings.TrimSpace(r.FormValue("outputDir")); dir != "" {
			opts.OutputDir = dir
		}
		s.renderDownloadForm422(w, r, opts, errs)
		return
	}

	opts := DownloadOpts{
		Kind:         kind,
		ID:           id,
		VideoQuality: valueOr(r, "videoQuality", "1080p"),
		AudioQuality: valueOr(r, "audioQuality", "192k"),
		AudioLangs:   audio,
		SubsLangs:    subs,
		OutputDir:    strings.TrimSpace(r.FormValue("outputDir")),
		Format:       valueOr(r, "format", "mkv"),
	}
	if opts.OutputDir == "" {
		opts.OutputDir = s.sessionOutputDir()
	}
	if len(opts.SubsLangs) == 0 {
		opts.SubsLangs = []string{"en-US"}
	}

	if _, err := s.enqueue(kind, id, opts); err != nil {
		opts := s.downloadFormOpts(kind, id)
		opts.SelectedAudio = audio
		opts.SelectedSubs = subs
		s.renderDownloadForm422(w, r, opts, map[string]string{"_": err.Error()})
		return
	}

	// Remember the user's choices so the modal pre-fills them next time.
	s.persistLastOpts(opts)

	// Success: empty body + HX-Trigger. The layout's closeDownloadModal listener
	// closes the dialog and clears the form; downloadsUpdated refreshes
	// #download-queue.
	w.Header().Set("HX-Trigger", `{"closeDownloadModal":null,"downloadsUpdated":null}`)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	render(w, r, web.JobsPage(s.manager.List()))
}

func (s *Server) handleJobsList(w http.ResponseWriter, r *http.Request) {
	render(w, r, web.JobsList(s.manager.List()))
}

// handleJobCancel aborts a running (or queued) job by cancelling its context.
// The task observes the cancellation at its next I/O boundary and the job
// reaches StatusCancelled, which the SSE stream forwards to the card. The
// response is empty with a downloadsUpdated HX-Trigger so htmx refreshes
// #download-queue (and the card's control buttons via the SSE status event).
func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.manager.Cancel(id)
	w.Header().Set("HX-Trigger", `{"downloadsUpdated":null}`)
	w.WriteHeader(http.StatusOK)
}

// handleJobDelete removes a terminal job from the Manager. The Manager
// broadcasts EventRemoved, which the SSE stream forwards as a `removed` event
// so the page script drops the card live — no list refresh needed. The stored
// restart opts for the id are cleared too so the map does not leak. A
// still-running job is not deleted (returns OK without removing; the caller
// must cancel first).
func (s *Server) handleJobDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.manager.Delete(id) {
		s.clearRestart(id)
	}
	// Even if nothing was deleted (race / already gone), refresh the queue so the
	// page is consistent.
	w.Header().Set("HX-Trigger", `{"downloadsUpdated":null}`)
	w.WriteHeader(http.StatusOK)
}

// handleJobRestart re-enqueues a job's target using the download opts stored when
// it was first created. The Job carries RestartKind/RestartID (per-episode);
// restartOptsFor supplies the quality/language/output choices. A new job is
// enqueued (a fresh card appears via the SSE snapshot); the old card is left in
// place — deleting a terminal source card is a separate, explicit action.
func (s *Server) handleJobRestart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.manager.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	kind := job.RestartKind
	target := job.RestartID
	if kind == "" || target == "" {
		// No restart target recorded (e.g. an old job from before this feature):
		// nothing to re-enqueue.
		w.Header().Set("HX-Trigger", `{"downloadsUpdated":null}`)
		w.WriteHeader(http.StatusOK)
		return
	}
	opts, hasOpts := s.restartOptsFor(id)
	if !hasOpts {
		// Fall back to the last-used prefs so a restart still works if the stored
		// opts were trimmed (they are session-only and not persisted).
		opts = s.lastDownloadOpts()
	}
	if _, err := s.enqueue(kind, target, opts); err != nil {
		// Surface a non-sensitive error (enqueue only returns transport/config
		// errors, never the token). A 424 keeps the page usable.
		w.Header().Set("HX-Trigger", `{"downloadsUpdated":null}`)
		w.WriteHeader(http.StatusFailedDependency)
		return
	}
	w.Header().Set("HX-Trigger", `{"downloadsUpdated":null}`)
	w.WriteHeader(http.StatusOK)
}
