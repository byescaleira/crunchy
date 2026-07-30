package jobs

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"crunchyroll-downloader/internal/download"
)

// spec is a shorthand to build a JobSpec with a Task.
func spec(label string, task Task) JobSpec {
	return JobSpec{Label: label, Task: task}
}

// TestManager_EnqueueComplete drives a job through its full lifecycle and
// checks that the channelProgress republishes the download.Progress calls as
// events, the segment counts land on the Job, and the job ends StatusDone with
// its event/done channels closed.
func TestManager_EnqueueComplete(t *testing.T) {
	m := NewManager(1)

	j := m.Enqueue(spec("S01E01", func(p download.Progress) error {
		p.Printf("Downloading %s audio...\n", "ja-JP")
		p.Phase("audio")
		p.Segment(1, 3)
		p.Segment(2, 3)
		p.Segment(3, 3)
		p.Printf("Cleaning up...")
		return nil
	}))

	<-j.Done()

	if got := j.Status(); got != StatusDone {
		t.Fatalf("status = %q, want %q", got, StatusDone)
	}
	if j.Error() != "" {
		t.Errorf("Error = %q, want empty", j.Error())
	}

	// Phase-weighted: Phase("audio") jumps to base 5; the three Segment ticks
	// map to 5+30/3=15, 5+60/3=25, 5+30=35 — last lands at (35,100).
	done, total := j.Segment()
	if done != 35 || total != 100 {
		t.Errorf("Segment = (%d,%d), want (35,100)", done, total)
	}
	if got := j.Phase(); got != "audio" {
		t.Errorf("Phase = %q, want %q", got, "audio")
	}

	var messages []string
	var lastStatus Status
	for e := range j.Events() {
		switch e.Type {
		case EventMessage:
			messages = append(messages, e.Message)
		case EventStatus:
			lastStatus = e.Status
		case EventDone:
			lastStatus = e.Status
		}
	}
	wantMsgs := []string{"Downloading ja-JP audio...\n", "Cleaning up..."}
	if len(messages) != len(wantMsgs) {
		t.Fatalf("messages = %v, want %v", messages, wantMsgs)
	}
	for i, want := range wantMsgs {
		if messages[i] != want {
			t.Errorf("messages[%d] = %q, want %q", i, messages[i], want)
		}
	}
	if lastStatus != StatusDone {
		t.Errorf("last event status = %q, want %q", lastStatus, StatusDone)
	}
}

// TestManager_ErrorTerminal enqueues a failing task and asserts the job reaches
// StatusError with the error message surfaced.
func TestManager_ErrorTerminal(t *testing.T) {
	m := NewManager(1)
	j := m.Enqueue(spec("bad", func(p download.Progress) error {
		return errBoom
	}))
	<-j.Done()

	if got := j.Status(); got != StatusError {
		t.Fatalf("status = %q, want %q", got, StatusError)
	}
	if j.Error() != errBoom.Error() {
		t.Errorf("Error = %q, want %q", j.Error(), errBoom.Error())
	}
}

// TestEnqueueMany_RunsUpToNConcurrent enqueues more jobs than the concurrency
// limit and asserts the Manager never exceeds it — the property that keeps a
// single user's bandwidth undivided and the rate-limit footprint modest.
func TestEnqueueMany_RunsUpToNConcurrent(t *testing.T) {
	const n = 5
	m := NewManager(3)
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32

	specs := make([]JobSpec, 0, n)
	for i := 0; i < n; i++ {
		specs = append(specs, spec("job", func(p download.Progress) error {
			cur := concurrent.Add(1)
			for {
				max := maxConcurrent.Load()
				if cur <= max || maxConcurrent.CompareAndSwap(max, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			concurrent.Add(-1)
			return nil
		}))
	}
	for _, j := range m.EnqueueMany(specs) {
		defer func(j *Job) { <-j.Done() }(j)
	}
	for _, j := range m.List() {
		<-j.Done()
	}

	if got := maxConcurrent.Load(); got > 3 {
		t.Errorf("max concurrent jobs = %d, want <= 3", got)
	}
	if got := len(m.List()); got != n {
		t.Errorf("List() has %d jobs, want %d", got, n)
	}
}

// TestManager_GetList checks lookup and enqueue-order preservation.
func TestManager_GetList(t *testing.T) {
	m := NewManager(2)
	var wg sync.WaitGroup
	ids := make([]string, 3)
	for i := range ids {
		wg.Add(1)
		j := m.Enqueue(spec("label", func(p download.Progress) error { wg.Done(); return nil }))
		ids[i] = j.ID
	}
	wg.Wait()
	for _, j := range m.List() {
		<-j.Done()
	}

	listed := m.List()
	if len(listed) != 3 {
		t.Fatalf("List() has %d jobs, want 3", len(listed))
	}
	for i, want := range ids {
		if listed[i].ID != want {
			t.Errorf("List()[%d].ID = %q, want %q", i, listed[i].ID, want)
		}
		if _, ok := m.Get(want); !ok {
			t.Errorf("Get(%q) = (_, false), want found", want)
		}
	}
}

// TestEnqueue_PopulatesJobMetadata asserts the display fields on a JobSpec are
// carried onto the Job verbatim so the card can render image + title + eyebrow
// with no extra API calls.
func TestEnqueue_PopulatesJobMetadata(t *testing.T) {
	m := NewManager(1)
	j := m.Enqueue(JobSpec{
		Label:         "S02E05 — The Fold",
		Task:          func(download.Progress) error { return nil },
		Title:         "The Fold",
		ImageURL:      "https://img/ep.jpg",
		SeriesTitle:   "Frieren",
		SeasonNumber:  2,
		EpisodeNumber: 5,
		GroupID:       "season-2",
		GroupLabel:    "Frieren — Season 2",
	})
	<-j.Done()

	if j.Title != "The Fold" || j.ImageURL != "https://img/ep.jpg" || j.SeriesTitle != "Frieren" {
		t.Errorf("display fields not set: %+v", j)
	}
	if j.SeasonNumber != 2 || j.EpisodeNumber != 5 {
		t.Errorf("season/episode numbers not set: %+v", j)
	}
	if j.GroupID != "season-2" || j.GroupLabel != "Frieren — Season 2" {
		t.Errorf("group fields not set: %+v", j)
	}
}

// TestEnqueueMany_IndependentErrors asserts that one failing job does not affect
// its siblings — each job is independent (strictly better than the old batch's
// skip-and-continue, because a failing episode no longer occupies the slot of
// the next). The failing job ends StatusError; the others end StatusDone.
func TestEnqueueMany_IndependentErrors(t *testing.T) {
	m := NewManager(3)
	specs := []JobSpec{
		spec("ok1", func(download.Progress) error { return nil }),
		spec("boom", func(download.Progress) error { return errBoom }),
		spec("ok2", func(download.Progress) error { return nil }),
	}
	js := m.EnqueueMany(specs)
	for _, j := range js {
		<-j.Done()
	}
	if js[0].Status() != StatusDone || js[2].Status() != StatusDone {
		t.Errorf("siblings should be done: %q %q", js[0].Status(), js[2].Status())
	}
	if js[1].Status() != StatusError {
		t.Errorf("failing job should be error, got %q", js[1].Status())
	}
	if js[1].Error() != errBoom.Error() {
		t.Errorf("failing job error = %q, want %q", js[1].Error(), errBoom.Error())
	}
}

// TestSubscribe_SnapshotAndBroadcast subscribes before any job exists (empty
// snapshot), then enqueues a job and asserts its live phase + done envelopes
// arrive over the broadcast channel tagged with the job id.
func TestSubscribe_SnapshotAndBroadcast(t *testing.T) {
	m := NewManager(2)
	ch, snapshot, cancel := m.Subscribe()
	defer cancel()
	if len(snapshot) != 0 {
		t.Fatalf("snapshot should be empty before any job, got %d", len(snapshot))
	}

	j := m.Enqueue(spec("ep", func(p download.Progress) error {
		p.Phase("audio")
		return nil
	}))

	var sawPhase, sawDone bool
	timeout := time.After(time.Second)
	for !sawDone {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("channel closed before done")
			}
			if ev.JobID != j.ID {
				t.Errorf("envelope job id = %q, want %q", ev.JobID, j.ID)
			}
			if ev.Event.Type == EventPhase {
				sawPhase = true
			}
			if ev.Event.Type == EventDone {
				sawDone = true
			}
		case <-timeout:
			t.Fatal("timed out waiting for the done envelope")
		}
	}
	if !sawPhase {
		t.Error("did not see the phase envelope")
	}
}

// TestEpisodeProgress covers the phase-weighting math: each phase owns a
// [base, base+span] slice of the 0-100 bar, indeterminate ticks hold at the
// base, a completed phase reports base+span, and an unknown phase reports 0.
func TestEpisodeProgress(t *testing.T) {
	cases := []struct {
		phase string
		done  int
		total int
		want  int
	}{
		{"subtitles", 0, 0, 0},  // indeterminate → base 0
		{"subtitles", 1, 2, 2},  // 0 + 5/2 = 2
		{"audio", 0, 3, 5},      // base 5
		{"audio", 1, 2, 20},     // 5 + 30/2 = 20
		{"audio", 2, 2, 35},     // completed → 5 + 30 = 35
		{"video", 0, 0, 35},     // indeterminate → base 35
		{"video", 1, 2, 62},     // 35 + 55/2 = 62
		{"video", 2, 2, 90},     // completed → 35 + 55 = 90
		{"mux", 0, 0, 90},       // base 90
		{"", 5, 5, 0},           // unknown phase → 0
	}
	for _, c := range cases {
		if got := episodeProgress(c.phase, c.done, c.total); got != c.want {
			t.Errorf("episodeProgress(%q,%d,%d) = %d, want %d", c.phase, c.done, c.total, got, c.want)
		}
	}
}

type boomErr string

func (b boomErr) Error() string { return string(b) }

const errBoom boomErr = "kaboom"