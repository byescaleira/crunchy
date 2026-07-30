package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"crunchyroll-downloader/internal/browser"
	"crunchyroll-downloader/internal/crunchy"
	"crunchyroll-downloader/internal/jobs"
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

// handleSettingsFindCookies scans the user's local browser cookie stores for the
// etp_rt cookie and, on a hit, saves + verifies it via configure (the same path
// the paste form uses). The token is never rendered, logged, or put in an error
// — the messages name the browser it came from, never the value. On a miss the
// user is told to log in to crunchyroll.com in their browser and click Find
// again (the button is the refresh). This is an HTML POST under /settings, not
// /api/*, so CORS is unchanged.
func (s *Server) handleSettingsFindCookies(w http.ResponseWriter, r *http.Request) {
	value, browserName, err := browser.FindEtpRt()
	if err != nil {
		// err is non-sensitive (permission / parse / keychain), never the token.
		render(w, r, web.Alert("error", "Could not read browser cookies: "+err.Error()))
		return
	}
	if value == "" {
		render(w, r, web.Alert("warning",
			"No etp_rt cookie found. Log in to crunchyroll.com in your browser, "+
				"then click Find again."))
		return
	}
	if err := s.configure(value, s.outputDir); err != nil {
		// configure returns a transport/token error, never the token itself.
		render(w, r, web.Alert("error",
			"Found a token in "+browserName+" but it could not be verified: "+err.Error()))
		return
	}
	render(w, r, web.Alert("success", "Token found in "+browserName+" and saved."))
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

// handleBrowse renders the discovery surface: a curated genre rail plus a grid
// of series cards. With no query it shows the popular grid; ?cat=<slug> filters
// the grid to one genre (via the discover/browse `categories` param); a
// non-empty ?q= is either a series URL (redirect to the detail page) or a title
// (search results grid). Series cards link to /series/{id} (the detail page),
// not back into Browse. This is a GET-only route — the search box lives in the
// capsule navbar and submits on Enter.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	cat := strings.TrimSpace(r.URL.Query().Get("cat"))

	if !s.Configured() {
		render(w, r, web.BrowsePage(web.BrowseView{ErrText: "Save your etp_rt token in Settings first."}))
		return
	}

	s.mu.RLock()
	api := s.api
	s.mu.RUnlock()

	// A Crunchyroll series URL navigates to the detail page rather than
	// rendering inline. A /watch/ episode link is rejected with a hint.
	if q != "" {
		if contentType, contentId, err := crunchy.ParseContentURL(q); err == nil {
			if contentType == "watch" {
				render(w, r, web.BrowsePage(web.BrowseView{
					Mode:    web.BrowseSearch,
					Query:   q,
					ErrText: "That's an episode link. Browse from a series page to pick episodes.",
				}))
				return
			}
			http.Redirect(w, r, "/series/"+contentId, http.StatusSeeOther)
			return
		}
	}

	view := web.BrowseView{Mode: web.BrowsePopular, ActiveCat: cat}

	switch {
	case q != "":
		// Title search.
		hits, err := api.SearchSeries(q, 24)
		if err != nil {
			render(w, r, web.BrowsePage(web.BrowseView{Mode: web.BrowseSearch, Query: q, ErrText: "Search is unavailable right now."}))
			return
		}
		view.Mode = web.BrowseSearch
		view.Query = q
		view.Hits = hits
	case cat != "" && media.GenreLabel(cat) != "":
		// Genre filter (validated against the curated list so an unknown slug
		// falls through to popular instead of a guaranteed-empty query).
		panels, err := api.BrowseByCategory(cat, 36, 0)
		if err != nil {
			render(w, r, web.BrowsePage(web.BrowseView{Mode: web.BrowseCategory, ActiveCat: cat, ErrText: "Could not load that category."}))
			return
		}
		view.Mode = web.BrowseCategory
		view.Panels = panels
	default:
		// Popular.
		panels, err := api.BrowsePopular(36, 0) // best-effort; degrade to empty grid
		if err != nil {
			render(w, r, web.BrowsePage(web.BrowseView{Mode: web.BrowsePopular, ErrText: "Popular anime is unavailable right now."}))
			return
		}
		view.Mode = web.BrowsePopular
		view.Panels = panels
	}

	render(w, r, web.BrowsePage(view))
}

// handleSeriesDetail renders the full series detail page: the cinematic hero
// (key art + title + meta + Download-series), the seasons list with per-season
// Download buttons, and an #episodes target that a season's View button fills
// via HTMX. The series fetch is best-effort — a missing series degrades to a
// titled hero that still shows the seasons + download affordances.
func (s *Server) handleSeriesDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.Configured() {
		http.Redirect(w, r, "/browse", http.StatusSeeOther)
		return
	}

	s.mu.RLock()
	api := s.api
	s.mu.RUnlock()

	seasons, err := api.GetSeasons(id, "ja-JP", "en-US")
	if err != nil {
		render(w, r, web.SeriesDetailPage(media.Series{}, id, nil, "Failed to list seasons: "+err.Error()))
		return
	}
	series, _ := api.GetSeries(id) // best-effort; degrades to a titled hero
	render(w, r, web.SeriesDetailPage(series, id, seasons, ""))
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

	started, err := s.enqueue(kind, id, opts)
	if err != nil {
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
	// #download-queue. jobStarted carries a toast title (the single episode's
	// label, or an "N queued" count) so the page the user enqueued from can show
	// feedback without leaving — the toast listener lives in layout.templ.
	var toastTitle string
	switch n := len(started); {
	case n == 1:
		toastTitle = started[0].Label
	case n > 1:
		toastTitle = fmt.Sprintf("%d downloads queued", n)
	default:
		toastTitle = "Download started"
	}
	trigger, _ := json.Marshal(map[string]any{
		"closeDownloadModal": nil,
		"downloadsUpdated":   nil,
		"jobStarted":         map[string]string{"title": toastTitle},
	})
	w.Header().Set("HX-Trigger", string(trigger))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	render(w, r, web.JobsPage(s.manager.List()))
}

// handleJobsList renders the jobs partial swapped into whichever jobs container
// requested it. The default (no view) renders the Jobs page's status-grouped list
// (Queued / Downloading / Completed / Errors) — every job including terminal ones,
// so the page is a full history. ?view=queue renders the active-only list used by
// a Browse "Now downloading" panel. The URL is chosen client-side via each
// container's data-jobs-url attribute (see refreshJobs in layout.templ).
func (s *Server) handleJobsList(w http.ResponseWriter, r *http.Request) {
	js := s.manager.List()
	if r.URL.Query().Get("view") == "queue" {
		render(w, r, web.JobsQueue(js))
		return
	}
	render(w, r, web.JobsListSections(js))
}

// handleJobsCancelAll cancels every non-terminal job at once (running jobs abort
// at their next I/O boundary; queued jobs cancel when the dispatcher starts them).
// It is an HTML POST route (not /api/*) so CORS is unchanged, and it fires a
// jobsChanged HX-Trigger so the page refreshes whichever jobs container is
// present. Mirrors the per-card handleJobCancel pattern.
func (s *Server) handleJobsCancelAll(w http.ResponseWriter, r *http.Request) {
	s.manager.CancelAll()
	w.Header().Set("HX-Trigger", `{"jobsChanged":null}`)
	w.WriteHeader(http.StatusOK)
}

// handleJobCancel aborts a running (or queued) job by cancelling its context.
// The task observes the cancellation at its next I/O boundary and the job
// reaches StatusCancelled, which the SSE stream forwards to the card (the SSE
// status event flips the control groups live). The response is empty with a
// jobsChanged HX-Trigger so the page refreshes whichever jobs container is
// present (#download-queue on Browse, #jobs-list on Jobs).
func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.manager.Cancel(id)
	w.Header().Set("HX-Trigger", `{"jobsChanged":null}`)
	w.WriteHeader(http.StatusOK)
}

// handleJobDelete removes a terminal job from the Manager. The Manager
// broadcasts EventRemoved, which the SSE stream forwards as a `removed` event
// so the page script drops the card live. The stored restart opts for the id are
// cleared too so the map does not leak. A still-running job is not deleted
// (returns OK without removing; the caller must cancel first). jobsChanged
// refreshes whichever jobs container is present so the list stays consistent.
func (s *Server) handleJobDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.manager.Delete(id) {
		s.clearRestart(id)
	}
	w.Header().Set("HX-Trigger", `{"jobsChanged":null}`)
	w.WriteHeader(http.StatusOK)
}

// handleJobFile streams a completed job's output file to whichever client
// requested it. The "Baixar" button on a done card and the popover's auto-
// download both hit this route. When the requester is the host itself (loopback
// or one of the host's own interface IPs — the Mac reaching its own panel over
// its LAN IP), the file is served and KEPT so the Mac's downloads persist. When
// the requester is remote (a phone on the LAN), the file is deleted after a
// complete, clean send — so the Mac stops holding it and the phone ends up with
// the only copy, matching "temporário no servidor até que seja baixado no
// client." A partial/aborted send leaves the file in place so a flaky client can
// retry. The job's output pointer is cleared once the file is gone so a second
// grab 404s instead of serving a stale path.
func (s *Server) handleJobFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := s.manager.Get(id)
	if !ok || j.Status() != jobs.StatusDone {
		http.NotFound(w, r)
		return
	}
	path, name := j.OutputPath(), j.OutputName()
	if path == "" || name == "" {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		j.SetOutput("", "")
		http.NotFound(w, r)
		return
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		j.SetOutput("", "")
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Type", videoContentType(name))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	n, copyErr := io.Copy(w, f)
	f.Close()
	// Delete only on a clean, complete send from a remote client: the host
	// keeps its copy, and a partial send leaves the file for a retry.
	if copyErr == nil && n == info.Size() && !isLocalPeer(r) {
		_ = os.Remove(path)
		j.SetOutput("", "")
	}
}

// videoContentType picks a reasonable Content-Type for the download attachment
// by extension (.mp4 → video/mp4, else the Matroska container, incl. .mkv).
func videoContentType(name string) string {
	if strings.HasSuffix(name, ".mp4") {
		return "video/mp4"
	}
	return "video/x-matroska"
}

// handleJobRestart re-enqueues a job's target using the download opts stored when
// it was first created. The Job carries RestartKind/RestartID (per-episode);
// restartOptsFor supplies the quality/language/output choices. A new job is
// enqueued (its card appears once jobsChanged refreshes the container); the old
// card is left in place — deleting a terminal source card is a separate,
// explicit action.
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
		w.Header().Set("HX-Trigger", `{"jobsChanged":null}`)
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
		w.Header().Set("HX-Trigger", `{"jobsChanged":null}`)
		w.WriteHeader(http.StatusFailedDependency)
		return
	}
	w.Header().Set("HX-Trigger", `{"jobsChanged":null}`)
	w.WriteHeader(http.StatusOK)
}
