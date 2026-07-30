// Package server is the control-panel HTTP server. It serves the embedded web UI,
// lets the user paste their etp-rt token, browse a series' seasons and episodes,
// and enqueue downloads that run through internal/jobs (which reuses the
// internal/download pipeline). Progress streams back over SSE. The server is
// single-user and binds to 127.0.0.1 only (enforced by the cmd); the etp-rt
// cookie is kept in memory for the session and optionally persisted to
// data-dir/config.json with 0600 — it is never logged.
package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/a-h/templ"

	"crunchyroll-downloader/internal/crunchy"
	"crunchyroll-downloader/internal/download"
	"crunchyroll-downloader/internal/jobs"
	"crunchyroll-downloader/internal/media"
	"crunchyroll-downloader/internal/web"
)

// crunchyAPI is the slice of *crunchy.Client the browse/episodes handlers need.
// It is an interface so handler tests can inject a fake without network.
type crunchyAPI interface {
	GetSeasons(contentId, audioLocale, subLocale string) ([]media.Season, error)
	GetSeasonEpisodes(contentId, audioLocale, subLocale string) ([]media.SeasonEpisode, error)
	GetEpisodeInfo(id string) (media.EpisodeInfo, error)
	GetSeries(id string) (media.Series, error)
	BrowsePopular(n, start int) ([]media.BrowsePanel, error)
	SearchSeries(q string, n int) ([]media.SearchHit, error)
}

// DownloadOpts carries the user's choices from the download-options modal into a
// job. Kind/ID identify the target (episode/season/series content id); the rest
// are the option fields. It is shared by the web form (W3) and the JSON API
// (W6) via the enqueue helper.
type DownloadOpts struct {
	Kind  string // "episode" | "season" | "series"
	ID    string // content id of the target
	Label string // human-readable job label

	VideoQuality string
	AudioQuality string
	AudioLangs   []string
	SubsLangs    []string
	OutputDir    string
	Format       string // "mkv" (default) or "mp4"
}

// config is the persisted (0600) session config. etpRt is sensitive. The
// Last* fields remember the user's last download-options choices so the modal
// pre-fills them next time instead of resetting to defaults every download.
type config struct {
	EtpRt     string `json:"etpRt"`
	OutputDir string `json:"outputDir"`

	LastVideoQuality string   `json:"lastVideoQuality"`
	LastAudioQuality string   `json:"lastAudioQuality"`
	LastFormat       string   `json:"lastFormat"`
	LastAudioLangs   []string `json:"lastAudioLangs"`
	LastSubsLangs    []string `json:"lastSubsLangs"`
	LastOutputDir    string   `json:"lastOutputDir"`
}

// Server holds the session state and dependencies. Goroutines read api/buildTask
// via the accessor under mu; configure writes them.
type Server struct {
	manager *jobs.Manager
	dataDir string
	cfgPath string
	debug   bool

	mu               sync.RWMutex
	api              crunchyAPI
	buildTask        func(contentId string, opts DownloadOpts) jobs.Task
	buildSeasonTasks func(seasonID string, opts DownloadOpts) ([]jobs.Task, string, error)
	buildSeriesTasks func(seriesID string, opts DownloadOpts) ([]jobs.Task, string, error)
	outputDir        string
	cfg              config // single source of truth for persisted prefs (mu-guarded)
}

// New creates a Server rooted at dataDir. If etpRt is empty it tries to load it
// from data-dir/config.json. A saved token is used to build a crunchy.Client
// best-effort: if the network is down, the server still starts (unconfigured)
// and the user can re-save the token from Settings.
func New(dataDir, etpRt string, debug bool) (*Server, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	s := &Server{
		manager: jobs.NewManager(),
		dataDir: dataDir,
		cfgPath: filepath.Join(dataDir, "config.json"),
		debug:   debug,
	}

	// Always load the saved config so the last-used download prefs (and the
	// chosen output dir) survive a restart, even when a token is supplied via
	// the flag. A missing file is not an error — cfg stays zero-valued and the
	// accessors fall back to defaults.
	saved, _ := s.loadConfig()
	s.mu.Lock()
	s.cfg = saved
	s.outputDir = saved.OutputDir
	s.mu.Unlock()

	if etpRt == "" {
		etpRt = saved.EtpRt
	}

	if etpRt != "" {
		// configure is best-effort at startup; failures leave the server
		// unconfigured rather than refusing to start.
		_ = s.configure(etpRt, s.outputDir)
	}
	return s, nil
}

// configure builds a crunchy.Client from etpRt, wires the browse API and the
// download-task factory onto it, and persists the config (0600). It returns an
// error if the token can't be exchanged for an access token; the caller decides
// whether that's fatal.
func (s *Server) configure(etpRt, outputDir string) error {
	client, err := crunchy.NewClient(etpRt, s.debug)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.api = client
	s.outputDir = outputDir
	// Update only the token + session output dir on the live config; the Last*
	// prefs are preserved (they live in s.cfg). Copy out under the lock and
	// persist after unlock so saveConfig never re-enters the mutex.
	s.cfg.EtpRt = etpRt
	s.cfg.OutputDir = outputDir
	s.buildTask = makeBuildTask(client, s.debug, outputDir)
	s.buildSeasonTasks = makeSeasonTaskBuilder(client, s.debug)
	s.buildSeriesTasks = makeSeriesTaskBuilder(client, s.debug)
	cfg := s.cfg
	s.mu.Unlock()
	return s.saveConfig(cfg)
}

// Configured reports whether a working token is on file.
func (s *Server) Configured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.api != nil
}

// lastDownloadOpts returns the last-used download options from the persisted
// config (Kind/ID empty — those are per-target, never remembered). It is the
// single source the modal pre-fills from; downloadFormOpts applies per-field
// defaults on top of it when a field is empty.
func (s *Server) lastDownloadOpts() DownloadOpts {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return DownloadOpts{
		VideoQuality: s.cfg.LastVideoQuality,
		AudioQuality: s.cfg.LastAudioQuality,
		Format:       s.cfg.LastFormat,
		AudioLangs:   s.cfg.LastAudioLangs,
		SubsLangs:    s.cfg.LastSubsLangs,
		OutputDir:    s.cfg.LastOutputDir,
	}
}

// persistLastOpts remembers the user's last download-options choices by writing
// them into the live config and persisting it (0600). It never drops the token
// or session output dir — it reads s.cfg (which carries them) and only overwrites
// the Last* fields. Best-effort: a write failure is logged, not returned, so a
// full disk never fails an otherwise-successful enqueue.
func (s *Server) persistLastOpts(opts DownloadOpts) {
	s.mu.Lock()
	s.cfg.LastVideoQuality = opts.VideoQuality
	s.cfg.LastAudioQuality = opts.AudioQuality
	s.cfg.LastFormat = opts.Format
	s.cfg.LastAudioLangs = opts.AudioLangs
	s.cfg.LastSubsLangs = opts.SubsLangs
	s.cfg.LastOutputDir = opts.OutputDir
	cfg := s.cfg
	s.mu.Unlock()
	if err := s.saveConfig(cfg); err != nil {
		log.Printf("persist last download opts: %v", err)
	}
}

// makeBuildTask returns a factory that builds a jobs.Task for one episode
// contentId. The task fetches episode info, then runs the download pipeline with
// a Progress supplied by the jobs.Manager (so progress flows to SSE), the chosen
// quality/languages, and the session output dir.
func makeBuildTask(client *crunchy.Client, debug bool, outputDir string) func(string, DownloadOpts) jobs.Task {
	return func(contentId string, opts DownloadOpts) jobs.Task {
		return func(p download.Progress) error {
			info, err := client.GetEpisodeInfo(contentId)
			if err != nil {
				return err
			}
			d := &download.Downloader{
				API:          client,
				VideoQuality: opts.VideoQuality,
				AudioQuality: opts.AudioQuality,
				AudioLangs:   opts.AudioLangs,
				SubsLangs:    opts.SubsLangs,
				OutputDir:    opts.OutputDir,
				Format:       opts.Format,
				Debug:        debug,
				Progress:     p,
			}
			return d.Episode(contentId, info)
		}
	}
}

// makeSeasonTaskBuilder returns a factory that discovers a season's episodes and
// builds one jobs.Task per episode (each running the full download.Episode
// pipeline), plus a parent label. The tasks run sequentially inside an
// EnqueueBatch parent so the Widevine keys-ordering invariant holds across
// episodes and progress aggregates across them.
func makeSeasonTaskBuilder(client *crunchy.Client, debug bool) func(string, DownloadOpts) ([]jobs.Task, string, error) {
	return func(seasonID string, opts DownloadOpts) ([]jobs.Task, string, error) {
		episodes, err := client.GetSeasonEpisodes(seasonID, "ja-JP", "en-US")
		if err != nil {
			return nil, "", fmt.Errorf("list season episodes: %w", err)
		}
		tasks := make([]jobs.Task, 0, len(episodes))
		for _, ep := range episodes {
			tasks = append(tasks, seasonEpisodeTask(client, debug, ep, opts))
		}
		return tasks, seasonLabel(episodes), nil
	}
}

// makeSeriesTaskBuilder returns a factory that discovers every season of a
// series, then every episode in each season, and builds one jobs.Task per
// episode (flattened across seasons), plus a parent label.
func makeSeriesTaskBuilder(client *crunchy.Client, debug bool) func(string, DownloadOpts) ([]jobs.Task, string, error) {
	return func(seriesID string, opts DownloadOpts) ([]jobs.Task, string, error) {
		seasons, err := client.GetSeasons(seriesID, "ja-JP", "en-US")
		if err != nil {
			return nil, "", fmt.Errorf("list seasons: %w", err)
		}
		var tasks []jobs.Task
		var firstEpisode media.SeasonEpisode
		for _, s := range seasons {
			episodes, err := client.GetSeasonEpisodes(s.ID, "ja-JP", "en-US")
			if err != nil {
				return nil, "", fmt.Errorf("list season %v episodes: %w", s.SeasonNumber, err)
			}
			for _, ep := range episodes {
				if firstEpisode.SeriesTitle == "" {
					firstEpisode = ep
				}
				tasks = append(tasks, seasonEpisodeTask(client, debug, ep, opts))
			}
		}
		return tasks, seriesLabel(seriesID, firstEpisode, client), nil
	}
}

// seasonEpisodeTask builds one per-episode Task from a season-episode list entry.
// The EpisodeInfo is built from the list entry (which already carries the W1
// rich metadata), so no per-episode GetEpisodeInfo round-trip is needed.
func seasonEpisodeTask(client *crunchy.Client, debug bool, ep media.SeasonEpisode, opts DownloadOpts) jobs.Task {
	return func(p download.Progress) error {
		d := &download.Downloader{
			API:          client,
			VideoQuality: opts.VideoQuality,
			AudioQuality: opts.AudioQuality,
			AudioLangs:   opts.AudioLangs,
			SubsLangs:    opts.SubsLangs,
			OutputDir:    opts.OutputDir,
			Format:       opts.Format,
			Debug:        debug,
			Progress:     p,
		}
		return d.Episode(ep.ID, download.EpisodeInfoFromSeasonEpisode(ep))
	}
}

// seasonLabel returns a parent label for a season batch, derived from the first
// episode's series/season titles.
func seasonLabel(episodes []media.SeasonEpisode) string {
	if len(episodes) == 0 {
		return "Season"
	}
	ep := episodes[0]
	if ep.SeriesTitle != "" {
		return fmt.Sprintf("%s — Season %v", ep.SeriesTitle, ep.SeasonNumber)
	}
	return fmt.Sprintf("Season %v", ep.SeasonNumber)
}

// seriesLabel returns a parent label for a series batch, preferring the series
// title from GetSeries, then the first episode's SeriesTitle, then the id.
func seriesLabel(seriesID string, first media.SeasonEpisode, client *crunchy.Client) string {
	if series, err := client.GetSeries(seriesID); err == nil && series.Title != "" {
		return series.Title
	}
	if first.SeriesTitle != "" {
		return first.SeriesTitle
	}
	return seriesID
}

// enqueue routes a download request to the right Manager method by granularity,
// returning the resulting (parent) job. episode → Enqueue (one job); season /
// series → EnqueueBatch (one parent job spanning many episode sub-tasks). It is
// the single entry point shared by the web form (W3) and the JSON API (W6).
func (s *Server) enqueue(kind, id string, opts DownloadOpts) (*jobs.Job, error) {
	s.mu.RLock()
	buildTask := s.buildTask
	buildSeason := s.buildSeasonTasks
	buildSeries := s.buildSeriesTasks
	s.mu.RUnlock()

	switch kind {
	case "season":
		if buildSeason == nil {
			return nil, errNotConfigured
		}
		tasks, label, err := buildSeason(id, opts)
		if err != nil {
			return nil, err
		}
		return s.manager.EnqueueBatch(label, tasks), nil
	case "series":
		if buildSeries == nil {
			return nil, errNotConfigured
		}
		tasks, label, err := buildSeries(id, opts)
		if err != nil {
			return nil, err
		}
		return s.manager.EnqueueBatch(label, tasks), nil
	default:
		if buildTask == nil {
			return nil, errNotConfigured
		}
		return s.manager.Enqueue(s.episodeTitle(id), buildTask(id, opts)), nil
	}
}

// errNotConfigured is returned by enqueue when no token is on file.
var errNotConfigured = fmt.Errorf("save your etp_rt token in Settings first")

// Handler returns the routes. It uses Go 1.22 ServeMux method+pattern routing.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/browse", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("POST /settings", s.handleSettingsPost)
	mux.HandleFunc("GET /browse", s.handleBrowse)
	mux.HandleFunc("POST /browse", s.handleBrowsePost)
	mux.HandleFunc("GET /season/{id}/episodes", s.handleSeasonEpisodes)
	mux.HandleFunc("GET /downloads/new", s.handleDownloadNew)
	mux.HandleFunc("POST /downloads", s.handleDownloadPost)
	mux.HandleFunc("GET /jobs", s.handleJobs)
	mux.HandleFunc("GET /jobs/list", s.handleJobsList)
	mux.HandleFunc("GET /jobs/{id}/events", s.handleJobEvents)

	// JSON API surface (W6) for the Safari extension. CORS is scoped to these
	// routes only via s.apiMiddleware (HTML routes are untouched).
	mux.HandleFunc("GET /api/health", s.handleAPIHealth)
	mux.HandleFunc("POST /api/download", s.handleAPIDownload)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleAPIJob)

	sub, err := fs.Sub(web.Static, "static")
	if err != nil {
		panic("embedded static subtree: " + err.Error())
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(sub)))
	return s.apiMiddleware(mux)
}

// render writes a templ component as the response body.
func render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// loadConfig reads data-dir/config.json. A missing file is not an error.
func (s *Server) loadConfig() (config, error) {
	b, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return config{}, err
	}
	var cfg config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// saveConfig writes data-dir/config.json with 0600 so the sensitive etp-rt stays
// private to the user.
func (s *Server) saveConfig(cfg config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.cfgPath, b, 0o600)
}
