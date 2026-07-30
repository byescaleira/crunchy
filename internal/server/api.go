package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// Version is the server build version. It can be overridden at build time via
// -ldflags; it defaults to "dev" for local/test runs.
var Version = "dev"

// extensionOriginSchemes are the WebExtension origin schemes the Safari (and
// Chrome/Firefox) extension uses. These origins rotate per install/launch
// (safari-web-extension://<GUID>), so they cannot be allowlisted by exact
// string — but the scheme is fixed, which is enough to distinguish a legitimate
// extension request from a drive-by web page.
var extensionOriginSchemes = []string{
	"safari-web-extension://",
	"chrome-extension://",
	"moz-extension://",
}

// apiOriginAllowed reports whether a request's Origin may use the /api/*
// surface. The server is 127.0.0.1-only, but the user's browser can still reach
// 127.0.0.1 while viewing an attacker-controlled page, so a blanket
// Access-Control-Allow-Origin: * would let that page enqueue downloads to the
// user's disk. Instead we allow only:
//   - no Origin at all (curl / same-machine tools — not subject to CORS), and
//   - browser-extension origins (the actual extension client), and
//   - same-origin requests (the Origin host:port equals the request's Host).
//
// A drive-by http(s) page fails all three and gets no CORS headers, so the
// browser blocks the cross-origin fetch/read.
func apiOriginAllowed(origin, host string) bool {
	if origin == "" {
		return true
	}
	for _, scheme := range extensionOriginSchemes {
		if strings.HasPrefix(origin, scheme) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == host // same-origin (same host:port)
}

// writeJSON marshals v as JSON and writes it as the response body with the
// given status and a application/json content type. It is the single JSON
// response helper used by all /api/* handlers; the HTML handlers keep using
// render() (text/html).
func writeJSON(w http.ResponseWriter, status int, v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"internal json error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(payload)
}

// apiMiddleware scopes CORS to /api/* only and restricts it to allowed origins.
// The path is cleaned before the /api/ prefix check so a path like
// /api/../settings (which the mux would redirect to /settings) cannot smuggle
// CORS headers onto an HTML route. For an allowed origin it reflects that
// origin (plus Vary: Origin), short-circuits OPTIONS preflights with 204, and
// otherwise delegates to the wrapped handler. A disallowed origin gets no CORS
// headers (preflight) or a 403 (actual request), so browsers block it.
// Non-/api/ routes (Settings/Browse/Jobs HTML) always pass through untouched.
func (s *Server) apiMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(path.Clean(r.URL.Path), "/api/") {
			h.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if !apiOriginAllowed(origin, r.Host) {
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden origin"})
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// handleAPIHealth reports server liveness and version for the extension.
func (s *Server) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"version": Version,
	})
}

// downloadRequest is the JSON body for POST /api/download. Field names are
// lenient (the JSON tags match the extension's payload); the values map onto
// the shared DownloadOpts via handleAPIDownload.
type downloadRequest struct {
	Kind     string   `json:"kind"`
	ID       string   `json:"id"`
	Audio    []string `json:"audio"`
	Subs     []string `json:"subs"`
	Quality  string   `json:"quality"`
	Location string   `json:"location"`
	Format   string   `json:"format"`
}

// handleAPIDownload enqueues a download from a JSON request, reusing the same
// enqueue helper as the HTML form (handleDownloadPost). It applies the same
// defaults/normalization and returns a job id on success.
func (s *Server) handleAPIDownload(w http.ResponseWriter, r *http.Request) {
	var req downloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Invalid JSON body."})
		return
	}

	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "episode"
	}
	id := strings.TrimSpace(req.ID)

	// Validation — JSON errors, not HTML re-renders.
	if id == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "No target selected."})
		return
	}
	if len(req.Audio) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Pick at least one audio language."})
		return
	}
	switch kind {
	case "episode", "season", "series":
	default:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "kind must be one of episode, season, series."})
		return
	}

	opts := DownloadOpts{
		Kind:         kind,
		ID:           id,
		VideoQuality: strings.TrimSpace(req.Quality),
		AudioLangs:   req.Audio,
		SubsLangs:    req.Subs,
		OutputDir:    strings.TrimSpace(req.Location),
		Format:       strings.TrimSpace(req.Format),
	}
	// Same defaults/normalization as handleDownloadPost.
	if opts.VideoQuality == "" {
		opts.VideoQuality = "1080p"
	}
	if opts.Format == "" {
		opts.Format = "mkv"
	}
	if opts.OutputDir == "" {
		opts.OutputDir = s.sessionOutputDir()
	}
	if len(opts.SubsLangs) == 0 {
		opts.SubsLangs = []string{"en-US"}
	}

	job, err := s.enqueue(kind, id, opts)
	if err != nil {
		// errNotConfigured carries a safe, user-actionable message (it never
		// mentions the token value), so surface it verbatim so the extension
		// can prompt the user to save their token. All other enqueue errors
		// (CMS discovery, network, ffmpeg wiring) can leak internal endpoint /
		// transport details via err.Error(); log those server-side and return a
		// fixed generic string to the client. The token is never echoed.
		msg := "failed to start download"
		if errors.Is(err, errNotConfigured) {
			msg = err.Error()
		} else {
			log.Printf("api download enqueue %s %s: %v", kind, id, err)
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": msg})
		return
	}
	// Remember the user's choices so the modal/extension pre-fills them next
	// time (shared helper with the web form).
	s.persistLastOpts(opts)

	writeJSON(w, http.StatusCreated, map[string]any{"jobId": job.ID})
}

// jobJSON is the JSON view of a jobs.Job for GET /api/jobs/{id}. The channel
// fields (Events/Done) are deliberately omitted — they cannot be marshaled
// and would leak internals.
type jobJSON struct {
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	Status   string         `json:"status"`
	Progress jobProgress    `json:"progress"`
	Error    string         `json:"error"`
}

type jobProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// handleAPIJob returns the current state of one job.
func (s *Server) handleAPIJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.manager.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "job not found"})
		return
	}
	done, total := job.Segment()
	writeJSON(w, http.StatusOK, jobJSON{
		ID:     job.ID,
		Label:  job.Label,
		Status: string(job.Status()),
		Progress: jobProgress{
			Done:  done,
			Total: total,
		},
		Error: job.Error(),
	})
}