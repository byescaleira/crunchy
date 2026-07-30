package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"crunchyroll-downloader/internal/download"
	"crunchyroll-downloader/internal/jobs"
	"crunchyroll-downloader/internal/media"
)

// fakeAPI is a crunchyAPI stub for handler tests.
type fakeAPI struct {
	seasons    []media.Season
	seasonErr  error
	episodes   []media.SeasonEpisode
	episodeErr error
	info       media.EpisodeInfo
	infoErr    error

	seasonReq    string
	episodeReqID string
}

func (f *fakeAPI) GetSeasons(contentId, audioLocale, subLocale string) ([]media.Season, error) {
	f.seasonReq = contentId
	return f.seasons, f.seasonErr
}
func (f *fakeAPI) GetSeasonEpisodes(contentId, audioLocale, subLocale string) ([]media.SeasonEpisode, error) {
	f.episodeReqID = contentId
	return f.episodes, f.episodeErr
}
func (f *fakeAPI) GetEpisodeInfo(id string) (media.EpisodeInfo, error) {
	return f.info, f.infoErr
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		manager: jobs.NewManager(),
		dataDir: t.TempDir(),
	}
}

func postForm(target, body string) (*http.Request, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r, httptest.NewRecorder()
}

func get(target string) (*http.Request, *httptest.ResponseRecorder) {
	return httptest.NewRequest(http.MethodGet, target, nil), httptest.NewRecorder()
}

func body(t *testing.T, h http.Handler, r *http.Request, w *httptest.ResponseRecorder) string {
	t.Helper()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET/POST %s: status %d, body %s", r.URL.Path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestSettings_GetNotConfigured(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	r, w := get("/settings")
	got := body(t, h, r, w)
	if !strings.Contains(got, "Settings") {
		t.Error("settings page missing title")
	}
	if !strings.Contains(got, `name="etpRt"`) {
		t.Error("settings page missing etpRt input")
	}
	if strings.Contains(got, "verified") {
		t.Error("unconfigured server should not claim a verified token")
	}
}

func TestSettingsPost_EmptyToken(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	r, w := postForm("/settings", "etpRt=")
	got := body(t, h, r, w)
	if !strings.Contains(got, "required") {
		t.Errorf("expected 'required' error, got: %s", got)
	}
	if s.Configured() {
		t.Error("empty token should not configure the server")
	}
}

func TestBrowsePost_NotConfigured(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	r, w := postForm("/browse", "url=https://www.crunchyroll.com/series/ABCDEFGHI")
	got := body(t, h, r, w)
	if !strings.Contains(got, "etp_rt") {
		t.Errorf("expected a 'save token first' message, got: %s", got)
	}
}

func TestBrowsePost_Seasons(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{
		seasons: []media.Season{
			{ID: "s1", SeasonNumber: 1},
			{ID: "s2", SeasonNumber: 2},
		},
	}
	h := s.Handler()
	r, w := postForm("/browse", "url=https://www.crunchyroll.com/series/ABCDEFGHI")
	got := body(t, h, r, w)
	if !strings.Contains(got, "Season 1") || !strings.Contains(got, "Season 2") {
		t.Errorf("expected both season cards, got: %s", got)
	}
	if !strings.Contains(got, "/season/s1/episodes") {
		t.Error("season card missing episodes link")
	}
}

func TestBrowsePost_BadURL(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{}
	h := s.Handler()
	r, w := postForm("/browse", "url=not-a-url")
	got := body(t, h, r, w)
	if !strings.Contains(got, "Invalid URL") {
		t.Errorf("expected Invalid URL error, got: %s", got)
	}
}

func TestSeasonEpisodes(t *testing.T) {
	s := newTestServer(t)
	f := &fakeAPI{
		episodes: []media.SeasonEpisode{
			{ID: "ep1", EpisodeNumber: 1, Title: "Pilot", AudioLocale: "ja-JP"},
			{ID: "ep2", EpisodeNumber: 2, Title: "Second", AudioLocale: "ja-JP",
				Versions: []*media.DubVersion{{AudioLocale: "en-US", GUID: "g"}}},
		},
	}
	s.api = f
	h := s.Handler()
	r, w := get("/season/SEASON42/episodes")
	got := body(t, h, r, w)
	if !strings.Contains(got, "Pilot") || !strings.Contains(got, "Second") {
		t.Errorf("expected both episode rows, got: %s", got)
	}
	// Each row now has a per-episode Download button that opens the modal for
	// that episode's id, instead of a checkbox. (templ escapes & to &amp; in
	// attributes, so check the query parts separately.)
	if !strings.Contains(got, "kind=episode") || !strings.Contains(got, "id=ep1") || !strings.Contains(got, "id=ep2") {
		t.Errorf("expected per-episode download buttons, got: %s", got)
	}
	if !strings.Contains(got, "en-US") {
		t.Error("dub version locale not rendered as a badge")
	}
	if f.episodeReqID != "SEASON42" {
		t.Errorf("GetSeasonEpisodes got id %q, want SEASON42", f.episodeReqID)
	}
}

func TestDownloadNew_RendersForm(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	r, w := get("/downloads/new?kind=episode&id=ep1")
	got := body(t, h, r, w)
	if !strings.Contains(got, `hx-post="/downloads"`) {
		t.Error("modal form must post to /downloads")
	}
	if !strings.Contains(got, `name="kind"`) || !strings.Contains(got, `value="ep1"`) {
		t.Error("modal form must carry the kind and target id as hidden fields")
	}
	if !strings.Contains(got, "Download episode") {
		t.Errorf("modal summary missing; got: %s", got)
	}
	// Audio and subtitle are checkbox groups; format is a select with both opts.
	if !strings.Contains(got, `name="audioLangs"`) || !strings.Contains(got, `name="subsLangs"`) {
		t.Error("modal form missing audio/subtitle fields")
	}
	if !strings.Contains(got, `value="mkv"`) || !strings.Contains(got, `value="mp4"`) {
		t.Error("modal form missing format options")
	}
}

func TestDownloadPost_NoAudio(t *testing.T) {
	s := newTestServer(t)
	s.buildTask = func(string, DownloadOpts) jobs.Task { return func(download.Progress) error { return nil } }
	h := s.Handler()
	// kind=episode, an id, but no audio language checked.
	r, w := postForm("/downloads", "kind=episode&id=ep1&videoQuality=1080p")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 re-render, got %d (body: %s)", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, "Pick at least one audio language") {
		t.Errorf("expected audio validation error, got: %s", got)
	}
	// The form is re-rendered, so the dialog stays open.
	if !strings.Contains(got, `hx-post="/downloads"`) {
		t.Error("422 should re-render the form into the modal")
	}
	if len(s.manager.List()) != 0 {
		t.Error("validation failure must not enqueue a job")
	}
}

func TestDownloadPost_Episode(t *testing.T) {
	s := newTestServer(t)
	s.buildTask = func(string, DownloadOpts) jobs.Task { return func(download.Progress) error { return nil } }
	h := s.Handler()
	r, w := postForm("/downloads",
		"kind=episode&id=ep1&videoQuality=1080p&audioQuality=192k&audioLangs=ja-JP&subsLangs=en-US&format=mkv")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	// Success returns an empty body so the modal content is cleared before close.
	if w.Body.Len() != 0 {
		t.Errorf("expected empty body on success, got: %s", w.Body.String())
	}
	// The HX-Trigger header closes the modal and refreshes the queue.
	trig := w.Header().Get("HX-Trigger")
	if !strings.Contains(trig, "closeDownloadModal") || !strings.Contains(trig, "downloadsUpdated") {
		t.Errorf("expected HX-Trigger to close modal + refresh queue, got: %q", trig)
	}
	if len(s.manager.List()) != 1 {
		t.Errorf("expected one enqueued job, got %d", len(s.manager.List()))
	}
}

func TestDownloadPost_NotConfigured(t *testing.T) {
	s := newTestServer(t)
	// No buildTask set: the server has no token.
	h := s.Handler()
	r, w := postForm("/downloads", "kind=episode&id=ep1&audioLangs=ja-JP")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "etp_rt") {
		t.Errorf("expected 'save token' message, got: %s", w.Body.String())
	}
}

func TestJobsList(t *testing.T) {
	s := newTestServer(t)
	s.buildTask = func(string, DownloadOpts) jobs.Task { return func(download.Progress) error { return nil } }
	s.manager.Enqueue("ep1", func(download.Progress) error { return nil })

	h := s.Handler()
	r, w := get("/jobs/list")
	got := body(t, h, r, w)
	if !strings.Contains(got, "ep1") {
		t.Errorf("jobs list missing the enqueued job; got: %s", got)
	}
}

// TestSSE_JobEvents enqueues a job that publishes a segment and a message, then
// connects to its SSE stream and asserts the events arrive in order.
func TestSSE_JobEvents(t *testing.T) {
	s := newTestServer(t)
	task := func(p download.Progress) error {
		p.Segment(1, 2)
		p.Printf("Downloading ja-JP audio...\n")
		return nil
	}
	job := s.manager.Enqueue("ep1", task)
	h := s.Handler()

	r, w := get("/jobs/" + job.ID + "/events")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("SSE status %d, body %s", w.Code, w.Body.String())
	}
	got := w.Body.String()
	for _, want := range []string{
		"event: status",
		"event: segment",
		`"done":1`,
		`"total":2`,
		"event: done",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("SSE stream missing %q\nfull stream:\n%s", want, got)
		}
	}
}

func TestSSE_UnknownJob(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	r, w := get("/jobs/nope/events")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown job, got %d", w.Code)
	}
}
