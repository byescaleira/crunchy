package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"crunchyroll-downloader/internal/jobs"
)

// handleJobEvents streams a job's progress events as Server-Sent Events. On
// connect it drains whatever the job has already buffered (so a late client
// catches up to the current state), then streams live until the job closes its
// event channel. The browser's EventSource (see JobsPage) updates the progress
// bar and status badge from these events.
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
			case jobs.EventError:
				send("status", map[string]any{"status": string(jobs.StatusError), "message": e.Message})
			case jobs.EventDone:
				sawDone = true
				send("done", map[string]any{"status": string(e.Status)})
			}
		}
	}
}
