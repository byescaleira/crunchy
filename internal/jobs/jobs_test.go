package jobs

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"crunchyroll-downloader/internal/download"
)

// TestManager_EnqueueComplete drives a job through its full lifecycle and
// checks that the channelProgress republishes the download.Progress calls as
// events, the segment counts land on the Job, and the job ends StatusDone with
// its event/done channels closed.
func TestManager_EnqueueComplete(t *testing.T) {
	m := NewManager()

	j := m.Enqueue("S01E01", func(p download.Progress) error {
		p.Printf("Downloading %s audio...\n", "ja-JP")
		p.Phase("audio")
		p.Segment(1, 3)
		p.Segment(2, 3)
		p.Segment(3, 3)
		p.Printf("Cleaning up...")
		return nil
	})

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
	m := NewManager()
	j := m.Enqueue("bad", func(p download.Progress) error {
		return errBoom
	})
	<-j.Done()

	if got := j.Status(); got != StatusError {
		t.Fatalf("status = %q, want %q", got, StatusError)
	}
	if j.Error() != errBoom.Error() {
		t.Errorf("Error = %q, want %q", j.Error(), errBoom.Error())
	}
}

// TestManager_Serializes enqueues several jobs and asserts the Manager runs them
// one at a time — the property that protects the Widevine keys-ordering
// invariant and keeps a single user's bandwidth undivided.
func TestManager_Serializes(t *testing.T) {
	m := NewManager()
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32

	const n = 5
	for i := 0; i < n; i++ {
		m.Enqueue("job", func(p download.Progress) error {
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
		})
	}

	for _, j := range m.List() {
		<-j.Done()
	}

	if got := maxConcurrent.Load(); got != 1 {
		t.Errorf("max concurrent jobs = %d, want 1", got)
	}
}

// TestManager_GetList checks lookup and enqueue-order preservation.
func TestManager_GetList(t *testing.T) {
	m := NewManager()
	var wg sync.WaitGroup
	ids := make([]string, 3)
	for i := range ids {
		wg.Add(1)
		j := m.Enqueue("label", func(p download.Progress) error { wg.Done(); return nil })
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

// TestEnqueueBatch_OrderedAggregateContinue pins the batch contract: sub-tasks
// run sequentially in order, segment progress aggregates across sub-tasks, a
// failing sub-task is skipped-and-continued (the rest still run), the first
// error is recorded, and the parent still reaches a terminal state.
func TestEnqueueBatch_OrderedAggregateContinue(t *testing.T) {
	m := NewManager()

	var order []int
	var mu sync.Mutex
	record := func(n int) {
		mu.Lock()
		order = append(order, n)
		mu.Unlock()
	}

	tasks := []Task{
		// sub-task 1: succeeds. Phase("audio") → episode 0 base 5; Segment(1,2)
		// → 5 + 30/2 = 20.
		func(p download.Progress) error {
			record(1)
			p.Phase("audio")
			p.Segment(1, 2)
			return nil
		},
		// sub-task 2: fails. Phase("audio") → episode 1 base 100+5=105;
		// Segment(2,2) → 100 + 35 = 135.
		func(p download.Progress) error {
			record(2)
			p.Phase("audio")
			p.Segment(2, 2)
			return errBoom
		},
		// sub-task 3: succeeds, reports no segments — still runs despite sub-task 2's error.
		func(p download.Progress) error {
			record(3)
			return nil
		},
	}

	j := m.EnqueueBatch("Season 1", tasks)
	<-j.Done()

	// All three sub-tasks ran, in order, despite the middle failure.
	mu.Lock()
	wantOrder := []int{1, 2, 3}
	if len(order) != len(wantOrder) {
		t.Errorf("sub-task order = %v, want %v", order, wantOrder)
	} else {
		for i, want := range wantOrder {
			if order[i] != want {
				t.Errorf("order[%d] = %d, want %d (full: %v)", i, order[i], want, order)
			}
		}
	}
	mu.Unlock()

	// First error recorded; parent ends in error.
	if got := j.Status(); got != StatusError {
		t.Fatalf("status = %q, want %q", got, StatusError)
	}
	if j.Error() != errBoom.Error() {
		t.Errorf("Error = %q, want %q", j.Error(), errBoom.Error())
	}

	// Phase-weighted aggregate across 3 episodes (bar total = 300). The last
	// reported segment is (135, 300).
	done, total := j.Segment()
	if done != 135 || total != 300 {
		t.Errorf("aggregate Segment = (%d,%d), want (135,300)", done, total)
	}

	// The segment events climb monotonically across the batch (no per-episode
	// drop): 5 → 20 → 105 → 135, all over a 300 total.
	var segs []Event
	for e := range j.Events() {
		if e.Type == EventSegment {
			segs = append(segs, e)
		}
	}
	wantSegs := []int{5, 20, 105, 135}
	if len(segs) != len(wantSegs) {
		t.Fatalf("expected %d segment events, got %d: %+v", len(wantSegs), len(segs), segs)
	}
	for i, want := range wantSegs {
		if segs[i].Done != want || segs[i].Total != 300 {
			t.Errorf("segment[%d] = (%d,%d), want (%d,300)", i, segs[i].Done, segs[i].Total, want)
		}
	}
}

// TestEpisodeProgress covers the phase-weighting math that both progress
// adapters share: each phase owns a [base, base+span] slice of the 0-100 bar,
// indeterminate ticks hold at the base, a completed phase reports base+span,
// and an unknown phase reports 0.
func TestEpisodeProgress(t *testing.T) {
	cases := []struct {
		phase string
		done  int
		total int
		want  int
	}{
		{"subtitles", 0, 0, 0},   // indeterminate → base 0
		{"subtitles", 1, 2, 2},   // 0 + 5/2 = 2
		{"audio", 0, 3, 5},       // base 5
		{"audio", 1, 2, 20},      // 5 + 30/2 = 20
		{"audio", 2, 2, 35},      // completed → 5 + 30 = 35
		{"video", 0, 0, 35},      // indeterminate → base 35
		{"video", 1, 2, 62},      // 35 + 55/2 = 62
		{"video", 2, 2, 90},       // completed → 35 + 55 = 90
		{"mux", 0, 0, 90},        // base 90
		{"", 5, 5, 0},            // unknown phase → 0
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
