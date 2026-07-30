package server

import (
	"net/http"
	"strings"

	"crunchyroll-downloader/internal/crunchy"
	"crunchyroll-downloader/internal/jobs"
	"crunchyroll-downloader/internal/web"
)

// splitLangs parses a comma-separated locale list from a form field, trimming
// spaces and dropping empties (mirrors the CLI's parseLangs).
func splitLangs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

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

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		render(w, r, web.Alert("error", err.Error()))
		return
	}
	contentIds := r.Form["contentIds"]
	if len(contentIds) == 0 {
		render(w, r, web.Alert("error", "Select at least one episode."))
		return
	}

	s.mu.RLock()
	buildTask := s.buildTask
	s.mu.RUnlock()
	if buildTask == nil {
		render(w, r, web.Alert("error", "Save your etp_rt token in Settings first."))
		return
	}

	opts := DownloadOpts{
		VideoQuality: valueOr(r, "videoQuality", "1080p"),
		AudioQuality: valueOr(r, "audioQuality", "192k"),
		AudioLangs:   splitLangs(r.FormValue("audioLangs")),
		SubsLangs:    splitLangs(r.FormValue("subsLangs")),
		OutputDir:    strings.TrimSpace(r.FormValue("outputDir")),
		Format:       valueOr(r, "format", "mkv"),
	}
	if opts.OutputDir == "" {
		s.mu.RLock()
		opts.OutputDir = s.outputDir
		s.mu.RUnlock()
	}
	if len(opts.AudioLangs) == 0 {
		opts.AudioLangs = []string{"ja-JP"}
	}
	if len(opts.SubsLangs) == 0 {
		opts.SubsLangs = []string{"en-US"}
	}

	newJobs := make([]*jobs.Job, 0, len(contentIds))
	for _, id := range contentIds {
		newJobs = append(newJobs, s.manager.Enqueue(id, buildTask(id, opts)))
	}
	render(w, r, web.JobsList(newJobs))
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	render(w, r, web.JobsPage(s.manager.List()))
}

func (s *Server) handleJobsList(w http.ResponseWriter, r *http.Request) {
	render(w, r, web.JobsList(s.manager.List()))
}
