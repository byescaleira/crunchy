package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"crunchyroll-downloader/internal/jobs"
)

// handleJobsEvents streams every job's progress as a single multiplexed
// Server-Sent Events stream. Each event payload carries the job id, so the page
// script dispatches by id to the matching card. This replaces one EventSource
// per card (which blows the browser's ~6-connections-per-host limit for a
// 24-episode season) with one stream for the whole page.
//
// On connect it subscribes to the Manager's broadcast, sends a snapshot of
// every job's current state (status + segment + phase) so a late subscriber
// catches up, then ranges over the broadcast channel for live events. The
// per-job /jobs/{id}/events stream is kept for /api-adjacent compat.
func (s *Server) handleJobsEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering

	flusher, _ := w.(http.Flusher)
	if flusher == nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// send writes one SSE event tagged with the job id and flushes immediately.
	send := func(name string, data any) {
		payload, err := json.Marshal(data)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload)
		if flusher != nil {
			flusher.Flush()
		}
	}

	ch, snapshot, cancel := s.manager.Subscribe()
	defer cancel()

	// Catch-up: replay current state for each job. The same listeners process
	// these as live events (status/segment/phase are idempotent), so a late
	// subscriber sees the correct bar/rail/badge before the first live tick.
	for _, j := range snapshot {
		st := string(j.Status())
		send("status", map[string]any{"id": j.ID, "status": st})
		if st == "error" && j.Error() != "" {
			send("status", map[string]any{"id": j.ID, "status": "error", "message": j.Error()})
		}
		done, total := j.Segment()
		send("segment", map[string]any{"id": j.ID, "done": done, "total": total})
		if ph := j.Phase(); ph != "" {
			send("phase", map[string]any{"id": j.ID, "phase": ph})
		}
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			e := ev.Event
			id := ev.JobID
			switch e.Type {
			case jobs.EventStatus:
				send("status", map[string]any{"id": id, "status": string(e.Status)})
			case jobs.EventMessage:
				send("message", map[string]any{"id": id, "message": e.Message})
			case jobs.EventSegment:
				send("segment", map[string]any{"id": id, "done": e.Done, "total": e.Total})
			case jobs.EventPhase:
				send("phase", map[string]any{"id": id, "phase": e.Phase})
			case jobs.EventError:
				send("status", map[string]any{"id": id, "status": string(jobs.StatusError), "message": e.Message})
			case jobs.EventDone:
				send("done", map[string]any{"id": id, "status": string(e.Status)})
			}
		}
	}
}

// handleJobEvents streams one job's progress events as Server-Sent Events (the
// per-job stream, kept for /api-adjacent compat). On connect it drains whatever
// the job has already buffered (so a late client catches up to the current
// state), then streams live until the job closes its event channel.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.manager.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering

	flusher, _ := w.(http.Flusher)
	if flusher == nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// send writes one SSE event and flushes it immediately.
	send := func(name string, data any) {
		payload, err := json.Marshal(data)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload)
		if flusher != nil {
			flusher.Flush()
		}
	}

	sawDone := false
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-job.Events():
			if !ok {
				// Channel closed: if we never saw a terminal event, synthesize
				// one from the authoritative job state so the client always
				// gets a final status (covers a late client connecting after the
				// done event was already drained).
				if !sawDone {
					send("done", map[string]any{"status": string(job.Status())})
				}
				return
			}
			switch e.Type {
			case jobs.EventStatus:
				send("status", map[string]any{"status": string(e.Status)})
			case jobs.EventMessage:
				send("message", map[string]any{"message": e.Message})
			case jobs.EventSegment:
				send("segment", map[string]any{"done": e.Done, "total": e.Total})
			case jobs.EventPhase:
				send("phase", map[string]any{"phase": e.Phase})
			case jobs.EventError:
				send("status", map[string]any{"status": string(jobs.StatusError), "message": e.Message})
			case jobs.EventDone:
				sawDone = true
				send("done", map[string]any{"status": string(e.Status)})
			}
		}
	}
}
