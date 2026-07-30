package server

import (
	"fmt"
	"time"

	"crunchyroll-downloader/internal/jobs"
	"crunchyroll-downloader/internal/logging"
)

// handleJobEvent is the Manager tap: every job event the server broadcasts to
// SSE subscribers is also handed here, and mapped to one structured log line so
// the user can follow the full download lifecycle server-side — queued, each
// phase, throttled progress, the download's own progress messages, and the
// terminal result with duration — not just the final status.
//
// It is called outside the Manager's subsMu (see jobs.Manager.broadcast), so it
// may do I/O and look jobs up without stalling the SSE fan-out.
func (s *Server) handleJobEvent(ev jobs.EnvelopeEvent) {
	e := ev.Event
	base := []logging.Field{logging.F("job", shortID(ev.JobID))}

	// Enrich with the job's display fields (title/series/episode). They are
	// write-once (set at Enqueue), so reading them is safe; the authoritative
	// per-step state (status/phase/segment) is already in the event.
	if j, ok := s.manager.Get(ev.JobID); ok {
		if j.Title != "" {
			base = append(base, logging.F("title", j.Title))
		}
		if j.SeriesTitle != "" {
			base = append(base, logging.F("series", j.SeriesTitle))
		}
		if j.SeasonNumber != 0 || j.EpisodeNumber != 0 {
			base = append(base, logging.F("ep", fmt.Sprintf("S%02dE%02d", j.SeasonNumber, j.EpisodeNumber)))
		}
	}

	switch e.Type {
	case jobs.EventStatus:
		switch e.Status {
		case jobs.StatusQueued:
			s.log.Info("jobs", "queued", base...)
		case jobs.StatusDownloading:
			s.markStart(ev.JobID)
			s.log.Info("jobs", "started", base...)
		case jobs.StatusMuxing:
			s.log.Info("jobs", "muxing", base...)
		}
	case jobs.EventPhase:
		s.log.Info("jobs", "phase "+e.Phase, base...)
	case jobs.EventMessage:
		// The download's own narrative: "Downloading subtitles for …",
		// "Downloaded subtitles!", "Downloading Japanese audio…", etc.
		if e.Message != "" {
			s.log.Info("jobs", e.Message, base...)
		}
	case jobs.EventSegment:
		// Phase-weighted 0-100 ticks arrive on every segment (hundreds for
		// video). Log only when the percent crosses a 5% bucket boundary, so a
		// whole episode produces ~20 progress lines, not one per segment.
		pct := e.Done
		if e.Total > 0 {
			pct = e.Done * 100 / e.Total
		}
		if s.shouldLogSeg(ev.JobID, pct) {
			fields := append(base, logging.F("pct", fmt.Sprintf("%d%%", pct)))
			if j, ok := s.manager.Get(ev.JobID); ok && j.Phase() != "" {
				fields = append(fields, logging.F("phase", j.Phase()))
			}
			s.log.Info("jobs", "progress", fields...)
		}
	case jobs.EventError:
		s.log.Error("jobs", "failed", append(base, logging.F("err", e.Message))...)
	case jobs.EventDone:
		fields := append(base, logging.F("status", string(e.Status)))
		if dur := s.sinceStart(ev.JobID); dur > 0 {
			fields = append(fields, logging.F("dur", dur.Round(time.Millisecond)))
		}
		switch e.Status {
		case jobs.StatusDone:
			s.log.Info("jobs", "finished", fields...)
		case jobs.StatusCancelled:
			s.log.Warn("jobs", "cancelled", fields...)
		default: // StatusError — the detail was already logged on EventError.
			s.log.Warn("jobs", "finished", fields...)
		}
		s.clearStart(ev.JobID)
	case jobs.EventRemoved:
		s.log.Info("jobs", "removed", base...)
		s.clearStart(ev.JobID)
	}
}

// shortID trims a UUID to its first 8 chars for log lines — enough to correlate
// events for one job without flooding the line with 36 hex chars.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// markStart records the wall-clock time a job began downloading, so EventDone
// can append a duration. Idempotent: a restart re-uses the existing start.
func (s *Server) markStart(id string) {
	s.logStateMu.Lock()
	if _, ok := s.logStarts[id]; !ok {
		s.logStarts[id] = time.Now()
	}
	s.logStateMu.Unlock()
}

// sinceStart returns the elapsed time since markStart, or 0 if none was
// recorded (e.g. a job cancelled while still queued).
func (s *Server) sinceStart(id string) time.Duration {
	s.logStateMu.Lock()
	t, ok := s.logStarts[id]
	s.logStateMu.Unlock()
	if !ok {
		return 0
	}
	return time.Since(t)
}

// clearStart drops the bookkeeping for a job once it has reached a terminal
// state, so the maps don't grow unbounded across a long-lived server.
func (s *Server) clearStart(id string) {
	s.logStateMu.Lock()
	delete(s.logStarts, id)
	delete(s.logSeg, id)
	s.logStateMu.Unlock()
}

// shouldLogSeg reports whether the percent tick should be logged: only when it
// moves the job into a new 5% bucket (0,5,10,…,100). The first tick for a job
// always logs (the bucket starts at -1).
func (s *Server) shouldLogSeg(id string, pct int) bool {
	bucket := pct / 5
	if bucket > 19 {
		bucket = 19
	}
	s.logStateMu.Lock()
	defer s.logStateMu.Unlock()
	last := s.logSeg[id]
	if bucket == last {
		return false
	}
	s.logSeg[id] = bucket
	return true
}