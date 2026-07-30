package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"crunchyroll-downloader/internal/download"
	"crunchyroll-downloader/internal/jobs"
	"crunchyroll-downloader/internal/logging"
)

// TestHandleJobEvent_Lifecycle drives a fake task through every phase the real
// download walks, with many segment ticks, and asserts the structured log
// carries the whole lifecycle (queued → phase → progress → finished) — not
// just the final status — and that segment progress is throttled to ~5%
// buckets instead of one line per tick.
func TestHandleJobEvent_Lifecycle(t *testing.T) {
	var buf strings.Builder
	s := &Server{
		log:       logging.New(&buf),
		logStarts: map[string]time.Time{},
		logSeg:    map[string]int{},
		manager:   jobs.NewManager(1),
	}
	s.manager.SetTap(s.handleJobEvent)

	// Walk the phases a real episode does. 100 video ticks exercise the throttle.
	task := func(ctx context.Context, p download.Progress) error {
		p.Phase("subtitles")
		p.Segment(0, 1)
		p.Printf("Downloaded subtitles!")
		p.Phase("audio")
		for i := 0; i <= 10; i++ {
			p.Segment(i, 10)
		}
		p.Phase("video")
		for i := 0; i < 100; i++ {
			p.Segment(i, 100)
		}
		p.Phase("mux")
		return nil
	}

	j := s.manager.Enqueue(jobs.JobSpec{
		Title:        "Test Ep",
		SeriesTitle:  "Test Series",
		SeasonNumber: 1,
		EpisodeNumber: 3,
		Task:         task,
	})
	select {
	case <-j.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("job did not finish in time")
	}

	out := buf.String()
	wantSubs := []string{
		"[jobs] queued",
		"[jobs] started",
		"[jobs] phase subtitles",
		"[jobs] Downloaded subtitles!",
		"[jobs] phase audio",
		"[jobs] progress",
		"[jobs] phase video",
		"[jobs] phase mux",
		"[jobs] finished",
		"status=done",
		"dur=",
		"job=" + shortID(j.ID),
		`title="Test Ep"`,
		"ep=S01E03",
	}
	for _, want := range wantSubs {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q\n--- got ---\n%s", want, out)
		}
	}

	// 100 video segment ticks must collapse to a handful of progress lines.
	if n := strings.Count(out, "[jobs] progress"); n > 25 {
		t.Errorf("progress logged %d times, expected <=25 (5%% throttle), out:\n%s", n, out)
	}
}

// TestHandleJobEvent_FailedAndCancelled asserts error/cancel results land as
// WARN/ERROR with the status, and the bookkeeping maps are cleared (no leak).
func TestHandleJobEvent_FailedAndCancelled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		task   func(ctx context.Context, p download.Progress) error
		status string
		level  string // substring expected in the terminal line
	}{
		{
			name:   "error",
			task:   func(ctx context.Context, p download.Progress) error { return context.DeadlineExceeded },
			status: "error",
			level:  "WARN",
		},
		{
			name: "cancelled",
			task: func(ctx context.Context, p download.Progress) error {
				<-ctx.Done()
				return ctx.Err()
			},
			status: "cancelled",
			level:  "WARN",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf strings.Builder
			s := &Server{
				log:       logging.New(&buf),
				logStarts: map[string]time.Time{},
				logSeg:    map[string]int{},
				manager:   jobs.NewManager(1),
			}
			s.manager.SetTap(s.handleJobEvent)

			j := s.manager.Enqueue(jobs.JobSpec{Title: tc.name, Task: tc.task})
			if tc.name == "cancelled" {
				time.Sleep(20 * time.Millisecond)
				s.manager.Cancel(j.ID)
			}
			select {
			case <-j.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("job did not finish")
			}

			out := buf.String()
			if !strings.Contains(out, "status="+tc.status) {
				t.Errorf("missing status=%s in:\n%s", tc.status, out)
			}
			if !strings.Contains(out, tc.level) {
				t.Errorf("expected %q level line in:\n%s", tc.level, out)
			}
			s.logStateMu.Lock()
			_, hasStart := s.logStarts[j.ID]
			s.logStateMu.Unlock()
			if hasStart {
				t.Errorf("start bookkeeping not cleared after %s", tc.status)
			}
		})
	}
}