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

	done, total := j.Segment()
	if done != 3 || total != 3 {
		t.Errorf("Segment = (%d,%d), want (3,3)", done, total)
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

type boomErr string

func (b boomErr) Error() string { return string(b) }

const errBoom boomErr = "kaboom"
