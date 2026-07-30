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
}

// DownloadOpts carries the user's choices from the /download form into a job.
type DownloadOpts struct {
	VideoQuality string
	AudioQuality string
	AudioLangs   []string
	SubsLangs    []string
	OutputDir    string
}

// config is the persisted (0600) session config. etpRt is sensitive.
type config struct {
	EtpRt     string `json:"etpRt"`
	OutputDir string `json:"outputDir"`
}

// Server holds the session state and dependencies. Goroutines read api/buildTask
// via the accessor under mu; configure writes them.
type Server struct {
	manager *jobs.Manager
	dataDir string
	cfgPath string
	debug   bool

	mu        sync.RWMutex
	api       crunchyAPI
	buildTask func(contentId string, opts DownloadOpts) jobs.Task
	outputDir string
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

	if etpRt == "" {
		if cfg, err := s.loadConfig(); err == nil {
			etpRt = cfg.EtpRt
			s.outputDir = cfg.OutputDir
		}
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
	s.buildTask = makeBuildTask(client, s.debug, outputDir)
	s.mu.Unlock()
	return s.saveConfig(config{EtpRt: etpRt, OutputDir: outputDir})
}

// Configured reports whether a working token is on file.
func (s *Server) Configured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.api != nil
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
				Debug:        debug,
				Progress:     p,
			}
			return d.Episode(contentId, info)
		}
	}
}

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
	mux.HandleFunc("POST /download", s.handleDownload)
	mux.HandleFunc("GET /jobs", s.handleJobs)
	mux.HandleFunc("GET /jobs/list", s.handleJobsList)
	mux.HandleFunc("GET /jobs/{id}/events", s.handleJobEvents)

	sub, err := fs.Sub(web.Static, "static")
	if err != nil {
		panic("embedded static subtree: " + err.Error())
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(sub)))
	return mux
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
