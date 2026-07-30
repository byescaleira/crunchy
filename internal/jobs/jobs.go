// Package jobs runs downloads as a serialized queue of jobs, each observable
// through a stream of progress events. The Manager runs one job at a time so the
// Widevine keys-ordering invariant (see internal/download) is never violated by
// two downloads of the same content racing, and so a single user's bandwidth
// isn't subdivided. A download publishes its progress through a channelProgress
// that adapts the download.Progress seam onto a job's event channel; the server
// (step k) drains that channel over SSE.
package jobs

import (
	"fmt"
	"sync"

	"github.com/google/uuid"

	"crunchyroll-downloader/internal/download"
)

// Status is the state of a job in the Manager's state machine.
type Status string

const (
	StatusQueued      Status = "queued"
	StatusDownloading Status = "downloading"
	StatusMuxing      Status = "muxing"
	StatusDone        Status = "done"
	StatusError       Status = "error"
)

// EventType names the kind of progress Event published to subscribers.
type EventType string

const (
	EventStatus  EventType = "status"  // a Status transition
	EventMessage EventType = "message" // a Printf progress line
	EventSegment EventType = "segment" // a Segment(done, total) tick
	EventPhase   EventType = "phase"   // a Phase(name) transition (subtitles/audio/video/mux)
	EventDone    EventType = "done"    // terminal: the job finished (success or error)
	EventError   EventType = "error"   // terminal error detail
)

// Event is one update published to a job's subscribers. The authoritative job
// state lives on the Job struct (Status, Error, segment counts, phase); Events
// are the live feed, so a subscriber that drops some can still read the final
// state.
type Event struct {
	Type    EventType
	Status  Status
	Message string
	Phase   string
	Done    int
	Total   int
}

// phaseRange is the [base, base+span] slice of the overall 0-100 progress bar
// that a download phase owns. The ranges telescope to 100:
// subtitles 0-5, audio 5-35, video 35-90, mux 90-100.
type phaseRange struct {
	base int
	span int
}

var phaseRanges = map[string]phaseRange{
	"subtitles": {0, 5},
	"audio":     {5, 30},
	"video":     {35, 55},
	"mux":       {90, 10},
}

// phaseBase returns the start of a phase's slice of the bar, or 0 for an
// unknown phase.
func phaseBase(name string) int {
	if r, ok := phaseRanges[name]; ok {
		return r.base
	}
	return 0
}

// episodeProgress maps a raw (done, total) segment tick within the named phase
// to an overall 0-100 episode percentage using the phase's [base, base+span]
// slice. Indeterminate ticks (total<=0) hold at the phase base; a completed
// phase (done>=total) reports base+span. It is the shared math behind both the
// single-episode and batch progress adapters.
func episodeProgress(phase string, done, total int) int {
	r, ok := phaseRanges[phase]
	if !ok {
		// Unknown phase (none announced yet): report a flat 0 so the bar stays
		// empty rather than flickering on the raw segment count.
		return 0
	}
	if total <= 0 {
		return r.base
	}
	if done <= 0 {
		return r.base
	}
	if done >= total {
		return r.base + r.span
	}
	return r.base + (r.span*done)/total
}

// Job is one enqueued download. Goroutines read it via the accessor methods; only
// the Manager's runner writes to it.
type Job struct {
	ID    string
	Label string

	events chan Event
	donec  chan struct{}

	mu       sync.RWMutex
	status   Status
	err      string
	segDone  int
	segTotal int
	phase    string
}

// Status reports the job's current state.
func (j *Job) Status() Status {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.status
}

// Phase reports the last announced download phase ("subtitles", "audio", "video",
// "mux"), or "" before the first phase is announced.
func (j *Job) Phase() string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.phase
}

// Error reports the error message when Status is StatusError, else "".
func (j *Job) Error() string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.err
}

// Segment reports the last (done, total) the job published.
func (j *Job) Segment() (int, int) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.segDone, j.segTotal
}

// Events returns the channel of progress events. It is closed when the job
// finishes; ranging over it drains the buffered events then exits.
func (j *Job) Events() <-chan Event { return j.events }

// Done returns a channel that closes when the job has reached a terminal state.
func (j *Job) Done() <-chan struct{} { return j.donec }

func (j *Job) set(status Status, err string) {
	j.mu.Lock()
	j.status = status
	j.err = err
	j.mu.Unlock()
}

func (j *Job) setSegment(done, total int) {
	j.mu.Lock()
	j.segDone = done
	j.segTotal = total
	j.mu.Unlock()
}

func (j *Job) setPhase(p string) {
	j.mu.Lock()
	j.phase = p
	j.mu.Unlock()
}

// emit publishes e to subscribers. Sends are non-blocking so a high-frequency
// segment burst can never stall the worker (and therefore the whole queue):
// dropped segment ticks are cosmetic, and the authoritative state stays on the
// Job. The terminal done/error events are also published here but, because they
// are rare, will not be dropped in practice; subscribers additionally read the
// Job state on connect.
func (j *Job) emit(e Event) {
	select {
	case j.events <- e:
	default:
	}
}

// Task is the unit of work the Manager runs. The Manager hands it a
// download.Progress (a channelProgress bound to the job) so the download's
// existing Printf/Segment calls publish into the job's event stream unchanged.
// A nil error marks the job done; a non-nil error marks it failed.
type Task func(progress download.Progress) error

// Manager serializes a queue of jobs. It is safe for concurrent use.
type Manager struct {
	mu    sync.Mutex
	jobs  map[string]*Job
	order []string
	sem   chan struct{}
}

// NewManager creates a Manager that runs one job at a time.
func NewManager() *Manager {
	return &Manager{
		jobs: map[string]*Job{},
		sem:  make(chan struct{}, 1),
	}
}

// Enqueue records a job and starts a runner goroutine that waits for the
// Manager's single slot, then runs task. The returned Job is already observable
// (StatusQueued) before task begins.
func (m *Manager) Enqueue(label string, task Task) *Job {
	j := &Job{
		ID:     uuid.NewString(),
		Label:  label,
		events: make(chan Event, 256),
		donec:  make(chan struct{}),
		status: StatusQueued,
	}
	j.emit(Event{Type: EventStatus, Status: StatusQueued})

	m.mu.Lock()
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.mu.Unlock()

	go m.run(j, task)
	return j
}

// run is the per-job runner. It blocks on the serialization semaphore, runs the
// task with a channelProgress, records the terminal state, and closes the job's
// event/done channels so subscribers can drain and exit.
func (m *Manager) run(j *Job, task Task) {
	m.sem <- struct{}{}
	defer func() {
		<-m.sem
		close(j.events)
		close(j.donec)
	}()

	j.set(StatusDownloading, "")
	j.emit(Event{Type: EventStatus, Status: StatusDownloading})

	if err := task(&channelProgress{job: j}); err != nil {
		j.set(StatusError, err.Error())
		j.emit(Event{Type: EventError, Message: err.Error()})
		j.emit(Event{Type: EventDone, Status: StatusError})
		return
	}
	// Emit a terminal status event before the done event so late subscribers
	// (and the existing status listener) catch the transition, not just the
	// done listener. Defense-in-depth for the client-side badge update.
	j.set(StatusDone, "")
	j.emit(Event{Type: EventStatus, Status: StatusDone})
	j.emit(Event{Type: EventDone, Status: StatusDone})
}

// EnqueueBatch records a parent job that runs tasks sequentially — one at a
// time, reusing the Manager's single slot so two downloads never race (the
// Widevine keys-ordering invariant holds across episodes, not just within one).
// It aggregates segment progress across the sub-tasks and continues past a
// sub-task error (skip-and-continue), recording the first error on the job but
// still running the remaining sub-tasks. The returned parent Job is observable
// (StatusQueued) before the runner begins; its events stream the aggregate.
//
// Use Enqueue for a single episode; EnqueueBatch for a season or series, where
// each Task is one episode.
func (m *Manager) EnqueueBatch(label string, tasks []Task) *Job {
	j := &Job{
		ID:     uuid.NewString(),
		Label:  label,
		events: make(chan Event, 256),
		donec:  make(chan struct{}),
		status: StatusQueued,
	}
	j.emit(Event{Type: EventStatus, Status: StatusQueued})

	m.mu.Lock()
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.mu.Unlock()

	go m.runBatch(j, tasks)
	return j
}

// runBatch is the batch runner. It blocks on the serialization semaphore (so a
// batch is run one sub-task at a time and never concurrently with another job),
// runs each task with a batchProgress that aggregates segment ticks across the
// sub-tasks, continues past a task error recording the first one, and ends the
// parent in StatusError (if any sub-task failed) or StatusDone.
func (m *Manager) runBatch(j *Job, tasks []Task) {
	m.sem <- struct{}{}
	defer func() {
		<-m.sem
		close(j.events)
		close(j.donec)
	}()

	j.set(StatusDownloading, "")
	j.emit(Event{Type: EventStatus, Status: StatusDownloading})

	bp := &batchProgress{job: j, episodeCount: len(tasks)}
	var firstErr string
	anyErr := false
	for _, task := range tasks {
		if err := task(bp); err != nil {
			anyErr = true
			if firstErr == "" {
				firstErr = err.Error()
			}
		}
		// Advance to the next episode so the next sub-task's phase/segment ticks
		// continue the bar monotonically across the batch (each episode owns a
		// 0-100 slice of the overall episodeCount*100 bar).
		bp.episodeIndex++
		bp.phase = ""
	}

	if anyErr {
		j.set(StatusError, firstErr)
		j.emit(Event{Type: EventError, Message: firstErr})
		j.emit(Event{Type: EventDone, Status: StatusError})
		return
	}
	// Emit a terminal status event before the done event so late subscribers
	// catch the transition (see run).
	j.set(StatusDone, "")
	j.emit(Event{Type: EventStatus, Status: StatusDone})
	j.emit(Event{Type: EventDone, Status: StatusDone})
}

// batchProgress adapts a parent batch Job onto the download.Progress seam,
// mapping each episode's phase-weighted progress onto a 0-100 slice of an
// overall episodeCount*100 bar so the aggregate climbs monotonically 0-100
// across the whole batch (season/series). episodeIndex is the number of
// completed sub-tasks; the active episode owns slice
// [episodeIndex*100, (episodeIndex+1)*100). Printf passes through as a message
// event so each episode's progress lines still stream.
type batchProgress struct {
	job          *Job
	episodeCount int
	episodeIndex int
	phase        string
}

func (b *batchProgress) Printf(format string, args ...any) {
	b.job.emit(Event{Type: EventMessage, Message: fmt.Sprintf(format, args...)})
}

func (b *batchProgress) Phase(name string) {
	b.phase = name
	b.job.setPhase(name)
	b.job.emit(Event{Type: EventPhase, Phase: name})
	aggDone := b.episodeIndex*100 + phaseBase(name)
	aggTotal := b.episodeCount * 100
	b.job.setSegment(aggDone, aggTotal)
	b.job.emit(Event{Type: EventSegment, Done: aggDone, Total: aggTotal})
}

func (b *batchProgress) Segment(done, total int) {
	epPct := episodeProgress(b.phase, done, total)
	aggDone := b.episodeIndex*100 + epPct
	aggTotal := b.episodeCount * 100
	b.job.setSegment(aggDone, aggTotal)
	b.job.emit(Event{Type: EventSegment, Done: aggDone, Total: aggTotal})
}

// Get returns the job with id, or (nil, false) if it does not exist.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// List returns the jobs in enqueue order.
func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Job, len(m.order))
	for i, id := range m.order {
		out[i] = m.jobs[id]
	}
	return out
}

// channelProgress adapts a single-episode Job onto the download.Progress seam:
// Printf becomes a message Event, Phase announces a download phase and jumps the
// bar to that phase's base, and Segment is mapped through episodeProgress to a
// phase-weighted 0-100 Event (mirrored onto the Job for late subscribers). The
// bar climbs 0-90 across subtitles/audio/video, sits at 90 through mux, and the
// done event fills it to 100. It performs no I/O of its own.
type channelProgress struct {
	job   *Job
	phase string
}

func (c *channelProgress) Printf(format string, args ...any) {
	c.job.emit(Event{Type: EventMessage, Message: fmt.Sprintf(format, args...)})
}

func (c *channelProgress) Phase(name string) {
	c.phase = name
	c.job.setPhase(name)
	c.job.emit(Event{Type: EventPhase, Phase: name})
	base := phaseBase(name)
	c.job.setSegment(base, 100)
	c.job.emit(Event{Type: EventSegment, Done: base, Total: 100})
}

func (c *channelProgress) Segment(done, total int) {
	pct := episodeProgress(c.phase, done, total)
	c.job.setSegment(pct, 100)
	c.job.emit(Event{Type: EventSegment, Done: pct, Total: 100})
}
