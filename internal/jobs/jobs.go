// Package jobs runs downloads as a queue of jobs, each observable through a
// stream of progress events. The Manager runs up to N jobs concurrently (one
// slot per episode), so a season or series download fans out into one job per
// episode with several running in parallel. The Widevine keys-ordering
// invariant is intra-episode (see internal/download): each Task builds its own
// Downloader, so concurrent episodes never race on the same content. A
// download publishes its progress through a channelProgress that adapts the
// download.Progress seam onto a job's event channel; the server (step k)
// drains that channel over SSE, and a multiplexed broadcast fans events out to
// every subscriber so a whole-season page needs a single stream, not one per
// card.
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
	StatusMuxing       Status = "muxing"
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

// EnvelopeEvent is an Event tagged with the job id it came from, for the
// multiplexed /jobs/events stream: one broadcast carries every job's events so
// a page with many cards opens a single EventSource instead of one per card.
type EnvelopeEvent struct {
	JobID string
	Event Event
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
// phase (done>=total) reports base+span. It is the shared math behind the
// progress adapter.
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

// Job is one enqueued download. Goroutines read it via the accessor methods;
// only the Manager's runner writes to it. The display fields (Title, ImageURL,
// SeriesTitle, Season/EpisodeNumber, GroupID, GroupLabel) are write-once — set
// at construction from the season-episode metadata — and read directly by the
// web templates without a mutex.
type Job struct {
	ID    string
	Label string

	// Display fields populated from media.SeasonEpisode / EpisodeInfo at enqueue
	// time so the job card can show the episode thumbnail + title + series
	// eyebrow with no extra API calls. Read-only after construction.
	Title         string
	ImageURL      string
	SeriesTitle   string
	SeasonNumber  int
	EpisodeNumber int
	GroupID       string // groups episodes of one season/series for a section header
	GroupLabel    string // header text for the group (e.g. "Frieren — Season 2")

	events   chan Event
	broadcast func(EnvelopeEvent)
	donec   chan struct{}

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
// Job state on connect. Each event is also forwarded to the Manager's
// broadcast (tagged with the job id) for the multiplexed /jobs/events stream.
func (j *Job) emit(e Event) {
	if j.broadcast != nil {
		j.broadcast(EnvelopeEvent{JobID: j.ID, Event: e})
	}
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

// JobSpec is the input to Enqueue: a Task plus the write-once display fields
// that let the job card render the episode thumbnail + title + series eyebrow.
type JobSpec struct {
	Label string
	Task  Task

	Title         string
	ImageURL      string
	SeriesTitle   string
	SeasonNumber  int
	EpisodeNumber int
	GroupID       string
	GroupLabel    string
}

// Manager runs a queue of jobs with up to maxConcurrent running at once. It is
// safe for concurrent use. The broadcast fans every job's events out to all
// subscribers (the multiplexed /jobs/events stream).
type Manager struct {
	mu    sync.Mutex
	jobs  map[string]*Job
	order []string
	sem   chan struct{}

	subsMu sync.Mutex
	subs   map[chan EnvelopeEvent]struct{}
}

// NewManager creates a Manager that runs up to maxConcurrent jobs at once. A
// value below 1 is clamped to 1 (strictly serial), so a zero-value config still
// works.
func NewManager(maxConcurrent int) *Manager {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Manager{
		jobs: map[string]*Job{},
		sem:  make(chan struct{}, maxConcurrent),
		subs: map[chan EnvelopeEvent]struct{}{},
	}
}

// Enqueue records a job and starts a runner goroutine that waits for one of the
// Manager's N slots, then runs spec.Task. The returned Job is already observable
// (StatusQueued) before the task begins; jobs beyond the concurrency limit stay
// queued until a slot frees.
func (m *Manager) Enqueue(spec JobSpec) *Job {
	j := &Job{
		ID:           uuid.NewString(),
		Label:        spec.Label,
		Title:        spec.Title,
		ImageURL:     spec.ImageURL,
		SeriesTitle:  spec.SeriesTitle,
		SeasonNumber: spec.SeasonNumber,
		EpisodeNumber: spec.EpisodeNumber,
		GroupID:      spec.GroupID,
		GroupLabel:   spec.GroupLabel,
		events:       make(chan Event, 256),
		broadcast:    m.broadcast,
		donec:        make(chan struct{}),
		status:       StatusQueued,
	}
	j.emit(Event{Type: EventStatus, Status: StatusQueued})

	m.mu.Lock()
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.mu.Unlock()

	go m.run(j, spec.Task)
	return j
}

// EnqueueMany enqueues a group of specs (one per episode of a season/series) as
// independent jobs. Up to N run concurrently; the rest stay queued. One
// episode's error never affects its siblings — each job is independent, which
// is strictly better than the old batch's skip-and-continue because a failing
// episode no longer blocks the slot of the next.
func (m *Manager) EnqueueMany(specs []JobSpec) []*Job {
	out := make([]*Job, 0, len(specs))
	for _, spec := range specs {
		out = append(out, m.Enqueue(spec))
	}
	return out
}

// run is the per-job runner. It blocks on the concurrency semaphore, runs the
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

// Subscribe registers a subscriber to the multiplexed event broadcast and
// returns the event channel (receive-only), a snapshot of the current jobs in
// enqueue order, and a cancel func the handler must defer so broadcast never
// sends into a closed channel. The handler sends the snapshot first (so a late
// subscriber catches up to the current state), then ranges over the channel
// for live events.
func (m *Manager) Subscribe() (<-chan EnvelopeEvent, []*Job, func()) {
	ch := make(chan EnvelopeEvent, 256)
	m.subsMu.Lock()
	m.subs[ch] = struct{}{}
	m.subsMu.Unlock()
	return ch, m.List(), func() { m.unsubscribe(ch) }
}

// unsubscribe removes ch from the broadcast set and closes it. Removing ch from
// the set before closing guarantees no in-flight broadcast sends into it: the
// broadcaster holds subsMu while sending, and unsubscribe holds subsMu while
// deleting, so the two never overlap on the same channel.
func (m *Manager) unsubscribe(ch chan EnvelopeEvent) {
	m.subsMu.Lock()
	delete(m.subs, ch)
	m.subsMu.Unlock()
	close(ch)
}

// broadcast fans ev out to every subscriber. Sends are non-blocking so a slow
// subscriber never stalls a worker; a full subscriber channel drops cosmetic
// segment ticks (the authoritative state stays on the Job and is re-snapshotted
// on reconnect).
func (m *Manager) broadcast(ev EnvelopeEvent) {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- ev:
		default:
		}
	}
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