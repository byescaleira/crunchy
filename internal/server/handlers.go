package server

import (
	"net/http"
	"strings"

	"crunchyroll-downloader/internal/crunchy"
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
	render(w, r, web.SettingsPage(s.Configured(), ""))
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

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	render(w, r, web.BrowsePage(nil, "", ""))
}

func (s *Server) handleBrowsePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render(w, r, web.SeasonsList(nil, err.Error()))
		return
	}
	url := strings.TrimSpace(r.FormValue("url"))

	if !s.Configured() {
		render(w, r, web.SeasonsList(nil, "Save your etp_rt token in Settings first."))
		return
	}

	contentType, contentId, err := crunchy.ParseContentURL(url)
	if err != nil {
		render(w, r, web.SeasonsList(nil, err.Error()))
		return
	}
	// /browse is for series pages; an episode /watch/ link belongs to the
	// download flow, not the seasons list.
	if contentType == "watch" {
		render(w, r, web.SeasonsList(nil, "That's an episode link. Browse from a /series/ page to pick episodes."))
		return
	}

	s.mu.RLock()
	api := s.api
	s.mu.RUnlock()
	seasons, err := api.GetSeasons(contentId, "ja-JP", "en-US")
	if err != nil {
		render(w, r, web.SeasonsList(nil, "Failed to list seasons: "+err.Error()))
		return
	}
	render(w, r, web.SeasonsList(seasons, ""))
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
	render(w, r, web.DownloadForm(s.downloadFormOpts(kind, id), nil))
}

// downloadFormOpts builds the modal view-model for a kind/target, applying
// sensible defaults (ja-JP audio, en-US subs, 1080p/192k, .mkv, the session
// output dir). Used for both the initial GET and the 422 re-render, so a
// re-rendered form keeps the same defaults for the fields the user didn't touch.
func (s *Server) downloadFormOpts(kind, id string) web.DownloadFormOpts {
	return web.DownloadFormOpts{
		Kind:          kind,
		ID:            id,
		Summary:       s.downloadSummary(kind, id),
		VideoQuality:  "1080p",
		AudioQuality:  "192k",
		Format:        "mkv",
		SelectedAudio: []string{"ja-JP"},
		SelectedSubs:  []string{"en-US"},
		OutputDir:     s.sessionOutputDir(),
	}
}

// downloadSummary returns the modal headline for a target. For an episode it
// best-effort resolves the title; season/series summaries are enriched with
// episode/season counts in W4.
func (s *Server) downloadSummary(kind, id string) string {
	switch kind {
	case "season":
		return "Download season"
	case "series":
		return "Download series"
	default:
		if title := s.episodeTitle(id); title != "" && title != id {
			return "Download episode — " + title
		}
		return "Download episode"
	}
}

// episodeTitle best-effort resolves an episode's title for job labels and the
// modal summary. It never errors: any failure (no token, network, missing) falls
// back to the id.
func (s *Server) episodeTitle(id string) string {
	s.mu.RLock()
	api := s.api
	s.mu.RUnlock()
	if api == nil {
		return id
	}
	if info, err := api.GetEpisodeInfo(id); err == nil && info.Title != "" {
		return info.Title
	}
	return id
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
	if kind == "episode" && id == "" {
		errs["_"] = "No episode selected."
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

	s.mu.RLock()
	buildTask := s.buildTask
	s.mu.RUnlock()
	if buildTask == nil {
		opts := s.downloadFormOpts(kind, id)
		opts.SelectedAudio = audio
		opts.SelectedSubs = subs
		s.renderDownloadForm422(w, r, opts, map[string]string{"_": "Save your etp_rt token in Settings first."})
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

	// W3 supports the episode granularity. Season/series batch routing lands in
	// W4 (EnqueueBatch); until then refuse them with a 422 rather than enqueueing
	// a season id as if it were a single episode.
	switch kind {
	case "season", "series":
		opts := s.downloadFormOpts(kind, id)
		opts.SelectedAudio = audio
		opts.SelectedSubs = subs
		s.renderDownloadForm422(w, r, opts, map[string]string{"_": "Season and series downloads arrive in the next wave."})
		return
	default:
		s.manager.Enqueue(s.episodeTitle(id), buildTask(id, opts))
	}

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
