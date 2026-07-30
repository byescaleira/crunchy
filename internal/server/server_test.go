package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	series     media.Series
	seriesErr  error
	panels     []media.BrowsePanel
	panelsErr  error
	hits       []media.SearchHit
	hitsErr    error

	seasonReq    string
	episodeReqID string
	seriesReqID  string
	searchReq    string
	catReq       string
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
func (f *fakeAPI) GetSeries(id string) (media.Series, error) {
	f.seriesReqID = id
	return f.series, f.seriesErr
}
func (f *fakeAPI) BrowsePopular(n, start int) ([]media.BrowsePanel, error) {
	return f.panels, f.panelsErr
}
func (f *fakeAPI) BrowseByCategory(category string, n, start int) ([]media.BrowsePanel, error) {
	f.catReq = category
	return f.panels, f.panelsErr
}
func (f *fakeAPI) SearchSeries(q string, n int) ([]media.SearchHit, error) {
	f.searchReq = q
	return f.hits, f.hitsErr
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	return &Server{
		manager:     jobs.NewManager(3),
		dataDir:     dir,
		cfgPath:     filepath.Join(dir, "config.json"),
		restartOpts: map[string]DownloadOpts{},
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

func TestBrowse_NotConfigured(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	r, w := get("/browse?q=https://www.crunchyroll.com/series/ABCDEFGHI")
	got := body(t, h, r, w)
	if !strings.Contains(got, "etp_rt") {
		t.Errorf("expected a 'save token first' message, got: %s", got)
	}
}

// TestBrowse_SeriesURLRedirects confirms a pasted series URL in the navbar
// search navigates to the detail page (a 303 redirect) rather than rendering
// inline on Browse.
func TestBrowse_SeriesURLRedirects(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{
		seasons: []media.Season{{ID: "s1", SeasonNumber: 1}},
	}
	h := s.Handler()
	r, w := get("/browse?q=https://www.crunchyroll.com/series/ABCDEFGHI")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect to the detail page, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/series/ABCDEFGHI" {
		t.Errorf("redirect Location = %q, want /series/ABCDEFGHI", loc)
	}
}

func TestBrowse_BadQuery(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{}
	h := s.Handler()
	// A non-URL, non-empty query falls through to a title search; with no
	// results the page shows a friendly "No results" message.
	r, w := get("/browse?q=not-a-url")
	got := body(t, h, r, w)
	if !strings.Contains(got, "No results") {
		t.Errorf("expected a 'No results' message, got: %s", got)
	}
}

func TestBrowse_GetPopular(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{
		panels: []media.BrowsePanel{
			{ID: "PANELID001", Title: "Frieren", SlugTitle: "frieren"},
		},
	}
	h := s.Handler()
	r, w := get("/browse")
	got := body(t, h, r, w)
	if !strings.Contains(got, "Frieren") {
		t.Errorf("popular grid missing the panel title; got: %s", got)
	}
	// The card is now a link to the detail page (/series/{id}), not an hx-post
	// tap target back into Browse.
	if !strings.Contains(got, `href="/series/PANELID001"`) {
		t.Errorf("popular card should link to the detail page; got: %s", got)
	}
	if strings.Contains(got, `hx-post="/browse"`) {
		t.Errorf("popular card must not post back into Browse; got: %s", got)
	}
}

func TestBrowse_GetPopular_NotConfigured(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	r, w := get("/browse")
	got := body(t, h, r, w)
	if !strings.Contains(got, "etp_rt") {
		t.Errorf("unconfigured browse should prompt for a token; got: %s", got)
	}
}

func TestBrowse_Category(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{
		panels: []media.BrowsePanel{{ID: "CATID0001", Title: "Action Show", SlugTitle: "action-show"}},
	}
	h := s.Handler()
	r, w := get("/browse?cat=action")
	got := body(t, h, r, w)
	if !strings.Contains(got, "Action Show") {
		t.Errorf("category grid missing the panel title; got: %s", got)
	}
	if !strings.Contains(got, "Action") { // the section eyebrow uses the genre label
		t.Errorf("category eyebrow missing the genre label; got: %s", got)
	}
	if s.api.(*fakeAPI).catReq != "action" {
		t.Errorf("BrowseByCategory got %q, want action", s.api.(*fakeAPI).catReq)
	}
}

func TestBrowse_TitleSearch(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{
		hits: []media.SearchHit{
			{ID: "HITID00001", Title: "Frieren", SlugTitle: "frieren-beyond-journey"},
		},
	}
	h := s.Handler()
	r, w := get("/browse?q=Frieren")
	got := body(t, h, r, w)
	if !strings.Contains(got, "Frieren") {
		t.Errorf("search results missing the hit title; got: %s", got)
	}
	if !strings.Contains(got, "Search results") {
		t.Errorf("expected the search-results header; got: %s", got)
	}
	if s.api.(*fakeAPI).searchReq != "Frieren" {
		t.Errorf("SearchSeries got %q, want Frieren", s.api.(*fakeAPI).searchReq)
	}
}

func TestBrowse_EmptyQuery(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{
		panels: []media.BrowsePanel{{ID: "PANELID002", Title: "Solo Leveling", SlugTitle: "solo-leveling"}},
	}
	h := s.Handler()
	r, w := get("/browse?q=")
	got := body(t, h, r, w)
	if !strings.Contains(got, "Solo Leveling") || !strings.Contains(got, "Popular now") {
		t.Errorf("empty query should re-show the popular grid; got: %s", got)
	}
}

// TestSeriesDetail renders the full detail page (hero + seasons + #episodes)
// for a /series/{id} GET.
func TestSeriesDetail(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{
		seasons: []media.Season{
			{ID: "s1", SeasonNumber: 1},
			{ID: "s2", SeasonNumber: 2},
		},
	}
	h := s.Handler()
	r, w := get("/series/ABCDEFGHI")
	got := body(t, h, r, w)
	if !strings.Contains(got, "Season 1") || !strings.Contains(got, "Season 2") {
		t.Errorf("expected both season cards, got: %s", got)
	}
	if !strings.Contains(got, "/season/s1/episodes") {
		t.Error("season card missing episodes link")
	}
	if !strings.Contains(got, `id="episodes"`) {
		t.Error("detail page missing the #episodes target")
	}
	if s.api.(*fakeAPI).seasonReq != "ABCDEFGHI" {
		t.Errorf("GetSeasons got %q, want ABCDEFGHI", s.api.(*fakeAPI).seasonReq)
	}
}

func TestSeriesDetail_NotConfigured(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	r, w := get("/series/ABCDEFGHI")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/browse" {
		t.Errorf("unconfigured detail should redirect to /browse; got %d %q", w.Code, w.Header().Get("Location"))
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
	// Audio/subtitle locale tags are intentionally not rendered on episode cards
	// (removed by request); only the Premium badge remains. Assert the locale
	// does NOT leak onto the card.
	if strings.Contains(got, ">en-US<") {
		t.Error("audio locale should not be rendered as a card badge")
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
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return nil }
	}
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
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return nil }
	}
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
	// The HX-Trigger header closes the modal, refreshes the queue, and fires
	// jobStarted so the page the user enqueued from shows a toast.
	trig := w.Header().Get("HX-Trigger")
	if !strings.Contains(trig, "closeDownloadModal") || !strings.Contains(trig, "downloadsUpdated") {
		t.Errorf("expected HX-Trigger to close modal + refresh queue, got: %q", trig)
	}
	if !strings.Contains(trig, "jobStarted") || !strings.Contains(trig, `"title"`) {
		t.Errorf("expected HX-Trigger to fire jobStarted with a title, got: %q", trig)
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

func TestDownloadNew_SeasonSummary(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{
		episodes: []media.SeasonEpisode{
			{ID: "ep1", EpisodeNumber: 1, SeasonNumber: 2, SeriesTitle: "Frieren"},
			{ID: "ep2", EpisodeNumber: 2, SeasonNumber: 2, SeriesTitle: "Frieren"},
		},
	}
	h := s.Handler()
	r, w := get("/downloads/new?kind=season&id=SEASON2")
	got := body(t, h, r, w)
	if !strings.Contains(got, "Download season 2 of Frieren") {
		t.Errorf("expected enriched season summary, got: %s", got)
	}
	if !strings.Contains(got, "2 episodes") {
		t.Errorf("expected episode count in summary, got: %s", got)
	}
}

func TestDownloadNew_SeriesSummary(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{
		seasons: []media.Season{
			{ID: "s1", SeasonNumber: 1, NumberOfEpisodes: 12},
			{ID: "s2", SeasonNumber: 2, NumberOfEpisodes: 24},
		},
		series: media.Series{Title: "Frieren"},
	}
	h := s.Handler()
	r, w := get("/downloads/new?kind=series&id=SERIES1")
	got := body(t, h, r, w)
	if !strings.Contains(got, "Download series Frieren") {
		t.Errorf("expected enriched series summary, got: %s", got)
	}
	if !strings.Contains(got, "2 seasons") || !strings.Contains(got, "36 episodes") {
		t.Errorf("expected season/episode counts in summary, got: %s", got)
	}
}

func TestDownloadPost_Season(t *testing.T) {
	s := newTestServer(t)
	s.buildSeasonTasks = func(seasonID string, opts DownloadOpts) ([]jobs.JobSpec, error) {
		if seasonID != "SEASON2" {
			t.Errorf("buildSeasonTasks got id %q, want SEASON2", seasonID)
		}
		return []jobs.JobSpec{
			{Label: "S02E01 — A", Title: "A", SeriesTitle: "Frieren", SeasonNumber: 2, EpisodeNumber: 1, GroupID: "SEASON2", GroupLabel: "Frieren — Season 2", Task: func(context.Context, download.Progress) error { return nil }},
			{Label: "S02E02 — B", Title: "B", SeriesTitle: "Frieren", SeasonNumber: 2, EpisodeNumber: 2, GroupID: "SEASON2", GroupLabel: "Frieren — Season 2", Task: func(context.Context, download.Progress) error { return nil }},
		}, nil
	}
	h := s.Handler()
	r, w := postForm("/downloads", "kind=season&id=SEASON2&audioLangs=ja-JP&subsLangs=en-US")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	if w.Body.Len() != 0 {
		t.Errorf("expected empty body on success, got: %s", w.Body.String())
	}
	// A season is one job per episode (2 here), not one batch parent.
	if n := len(s.manager.List()); n != 2 {
		t.Errorf("expected 2 jobs (one per episode), got %d", n)
	}
	if got := s.manager.List()[0].GroupLabel; got != "Frieren — Season 2" {
		t.Errorf("expected group label, got %q", got)
	}
}

func TestDownloadPost_Series(t *testing.T) {
	s := newTestServer(t)
	s.buildSeriesTasks = func(seriesID string, opts DownloadOpts) ([]jobs.JobSpec, error) {
		return []jobs.JobSpec{
			{Label: "a", Task: func(context.Context, download.Progress) error { return nil }},
			{Label: "b", Task: func(context.Context, download.Progress) error { return nil }},
			{Label: "c", Task: func(context.Context, download.Progress) error { return nil }},
		}, nil
	}
	h := s.Handler()
	r, w := postForm("/downloads", "kind=series&id=SERIES1&audioLangs=ja-JP")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body %s)", w.Code, w.Body.String())
	}
	// A series is one job per episode across all seasons (3 here).
	if n := len(s.manager.List()); n != 3 {
		t.Errorf("expected 3 jobs (one per episode), got %d", n)
	}
}

func TestDownloadPost_SeasonDiscoveryFails(t *testing.T) {
	s := newTestServer(t)
	s.buildSeasonTasks = func(seasonID string, opts DownloadOpts) ([]jobs.JobSpec, error) {
		return nil, fmt.Errorf("list season episodes: boom")
	}
	h := s.Handler()
	r, w := postForm("/downloads", "kind=season&id=SEASON2&audioLangs=ja-JP")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on discovery failure, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "boom") {
		t.Errorf("expected discovery error surfaced, got: %s", w.Body.String())
	}
	if len(s.manager.List()) != 0 {
		t.Error("failed discovery must not enqueue a job")
	}
}

func TestJobsList(t *testing.T) {
	s := newTestServer(t)
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return nil }
	}
	s.manager.Enqueue(jobs.JobSpec{Label: "ep1", Title: "Pilot", Task: func(context.Context, download.Progress) error { return nil }})

	h := s.Handler()
	r, w := get("/jobs/list")
	got := body(t, h, r, w)
	if !strings.Contains(got, "ep1") {
		t.Errorf("jobs list missing the enqueued job; got: %s", got)
	}
}

// TestJobsList_ControlButtons asserts the job card renders the per-card control
// buttons: Cancel while a job is queued/running, Restart + Delete once terminal.
// The data-controls attribute lets the SSE script toggle the two groups live.
func TestJobsList_ControlButtons(t *testing.T) {
	s := newTestServer(t)
	started := make(chan struct{})
	running := s.manager.Enqueue(jobs.JobSpec{
		Label: "running", Title: "R", RestartKind: "episode", RestartID: "epR",
		Task: func(ctx context.Context, p download.Progress) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	<-started
	done := s.manager.Enqueue(jobs.JobSpec{
		Label: "done", Title: "D", RestartKind: "episode", RestartID: "epD",
		Task: func(context.Context, download.Progress) error { return nil },
	})
	<-done.Done()

	h := s.Handler()
	r, w := get("/jobs/list")
	got := body(t, h, r, w)
	// Running card shows Cancel (data-controls="running"); terminal card shows
	// Restart + Delete (data-controls="terminal").
	if !strings.Contains(got, "/jobs/"+running.ID+"/cancel") {
		t.Errorf("running card missing Cancel button; got: %s", got)
	}
	if !strings.Contains(got, "/jobs/"+done.ID+"/restart") || !strings.Contains(got, "/jobs/"+done.ID+"/delete") {
		t.Errorf("terminal card missing Restart/Delete buttons; got: %s", got)
	}
	if !strings.Contains(got, `data-controls="running"`) || !strings.Contains(got, `data-controls="terminal"`) {
		t.Errorf("control groups missing data-controls tags; got: %s", got)
	}

	// Cancel the running job via its route so the test cleans up its goroutine.
	rc, wc := postForm("/jobs/"+running.ID+"/cancel", "")
	h.ServeHTTP(wc, rc)
	if wc.Code != http.StatusOK {
		t.Errorf("cancel route status = %d, want 200", wc.Code)
	}
	<-running.Done()
}

// TestJobsList_BothControlGroupsRendered guards the retry bug: a card fetched
// while a job is still running renders the Cancel group, and when that job then
// errors over SSE the page must already hold the Restart/Delete group in the
// DOM for the toggle to reveal it. So BOTH groups must always be rendered
// (one hidden via inline display), regardless of the job's current status —
// including a job that errored. An errored card must contain a Restart button.
func TestJobsList_BothControlGroupsRendered(t *testing.T) {
	s := newTestServer(t)
	failed := s.manager.Enqueue(jobs.JobSpec{
		Label: "boom", Title: "Boom", RestartKind: "episode", RestartID: "epBoom",
		Task: func(context.Context, download.Progress) error { return fmt.Errorf("PSSH not found") },
	})
	<-failed.Done()

	h := s.Handler()
	r, w := get("/jobs/list")
	got := body(t, h, r, w)
	// The errored card must offer a Retry (Restart) — the original bug left no
	// terminal group in the DOM for a job that errored after rendering as running.
	if !strings.Contains(got, "/jobs/"+failed.ID+"/restart") {
		t.Errorf("errored job card missing Restart button; got: %s", got)
	}
	// Both control groups are present in the DOM (the hidden one via display:none)
	// so the SSE toggle can reveal Restart/Delete when a running job errors live.
	if c := strings.Count(got, `data-controls="running"`); c < 1 {
		t.Errorf("expected a running control group in the DOM, got %d; %s", c, got)
	}
	if c := strings.Count(got, `data-controls="terminal"`); c < 1 {
		t.Errorf("expected a terminal control group in the DOM, got %d; %s", c, got)
	}
	// The hidden terminal group on a running card (and vice-versa) is hidden via
	// inline display:none, not omitted from the DOM. (templ emits "display:none;".)
	if !strings.Contains(got, `style="display:none`) {
		t.Errorf("expected an inline display:none to hide the inactive group; got: %s", got)
	}
}

// TestJobRoutes_Wiring exercises the cancel/delete/restart HTML routes end to
// end through the handler: cancel aborts a running job, delete removes a
// terminal one (and clears stored restart opts), restart re-enqueues from the
// stored opts. These are HTML POST routes (no CORS), scoped away from /api/*.
func TestJobRoutes_Wiring(t *testing.T) {
	s := newTestServer(t)
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(ctx context.Context, p download.Progress) error {
			<-ctx.Done()
			return ctx.Err()
		}
	}
	h := s.Handler()

	// Enqueue via the form so restartOpts is populated for the job id.
	r, w := postForm("/downloads", "kind=episode&id=ep1&videoQuality=720p&audioLangs=ja-JP&subsLangs=en-US&format=mkv")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("enqueue status = %d, body %s", w.Code, w.Body.String())
	}
	js := s.manager.List()
	if len(js) != 1 {
		t.Fatalf("expected 1 enqueued job, got %d", len(js))
	}
	id := js[0].ID

	// Cancel the running job via its route.
	rc, wc := postForm("/jobs/"+id+"/cancel", "")
	h.ServeHTTP(wc, rc)
	if wc.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", wc.Code)
	}
	<-js[0].Done()
	if j, ok := s.manager.Get(id); !ok || j.Status() != jobs.StatusCancelled {
		t.Fatalf("after cancel, job status = %v (ok=%v), want cancelled", statusOr(j), ok)
	}

	// Restart the (now cancelled) job via its route: storeRestart kept the opts,
	// so a fresh job is enqueued targeting the same episode.
	rr, wr := postForm("/jobs/"+id+"/restart", "")
	h.ServeHTTP(wr, rr)
	if wr.Code != http.StatusOK {
		t.Fatalf("restart status = %d, want 200 (body %s)", wr.Code, wr.Body.String())
	}
	if n := len(s.manager.List()); n != 2 {
		t.Errorf("after restart, expected 2 jobs (old + new), got %d", n)
	}
	// Cancel the newly-enqueued job so the test doesn't leak a goroutine.
	for _, j := range s.manager.List() {
		s.manager.Cancel(j.ID)
		<-j.Done()
	}

	// Delete the terminal job via its route.
	rd, wd := postForm("/jobs/"+id+"/delete", "")
	h.ServeHTTP(wd, rd)
	if wd.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", wd.Code)
	}
	if _, ok := s.manager.Get(id); ok {
		t.Errorf("job still present after delete")
	}
	if _, ok := s.restartOptsFor(id); ok {
		t.Errorf("restart opts not cleared after delete")
	}
}

// TestJobsCancelAll_Route enqueues a mix of running, queued, and terminal jobs and
// asserts the POST /jobs/cancel-all route cancels every non-terminal one (running
// jobs abort; queued jobs cancel when dispatched) while leaving terminal jobs
// untouched, and returns a jobsChanged HX-Trigger so the page refreshes.
func TestJobsCancelAll_Route(t *testing.T) {
	s := newTestServer(t)
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(ctx context.Context, p download.Progress) error {
			<-ctx.Done()
			return ctx.Err()
		}
	}
	h := s.Handler()

	running := s.manager.Enqueue(jobs.JobSpec{Label: "run", Title: "R", Task: func(ctx context.Context, p download.Progress) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	queued := s.manager.Enqueue(jobs.JobSpec{Label: "q", Title: "Q", Task: func(ctx context.Context, p download.Progress) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	done := s.manager.Enqueue(jobs.JobSpec{Label: "done", Title: "D", Task: func(context.Context, download.Progress) error { return nil }})
	<-done.Done()

	r, w := postForm("/jobs/cancel-all", "")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel-all status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("HX-Trigger"); !strings.Contains(got, "jobsChanged") {
		t.Errorf("cancel-all missing jobsChanged HX-Trigger, got %q", got)
	}
	<-running.Done()
	<-queued.Done()
	if j, _ := s.manager.Get(running.ID); j.Status() != jobs.StatusCancelled {
		t.Errorf("running job status = %v, want cancelled", statusOr(j))
	}
	if j, _ := s.manager.Get(queued.ID); j.Status() != jobs.StatusCancelled {
		t.Errorf("queued job status = %v, want cancelled", statusOr(j))
	}
	if j, _ := s.manager.Get(done.ID); j.Status() != jobs.StatusDone {
		t.Errorf("done job status = %v, want done (terminal jobs untouched)", statusOr(j))
	}
}

// TestJobsList_LocaleErrorSurfaced injects a buildTask that returns the
// unavailable-locale error the real pipeline now produces, and asserts the job
// ends StatusError and the message renders in the card's error line on /jobs/list
// (not a silent done-then-vanish).
func TestJobsList_LocaleErrorSurfaced(t *testing.T) {
	s := newTestServer(t)
	msg := "audio locale pt-BR not available for episode 3"
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return fmt.Errorf("%s", msg) }
	}
	r, w := postForm("/downloads", "kind=episode&id=ep3&videoQuality=1080p&audioLangs=pt-BR&subsLangs=en-US&format=mkv")
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("enqueue status = %d, body %s", w.Code, w.Body.String())
	}
	js := s.manager.List()
	if len(js) != 1 {
		t.Fatalf("expected 1 enqueued job, got %d", len(js))
	}
	<-js[0].Done()
	if js[0].Status() != jobs.StatusError {
		t.Fatalf("job status = %v, want error", js[0].Status())
	}

	h := s.Handler()
	rl, wl := get("/jobs/list")
	got := body(t, h, rl, wl)
	if !strings.Contains(got, msg) {
		t.Errorf("locale error message not surfaced in the card; got: %s", got)
	}
}

// statusOr returns the job's status string, or "<nil>" for a nil job (test helper).
func statusOr(j *jobs.Job) string {
	if j == nil {
		return "<nil>"
	}
	return string(j.Status())
}

// TestJobsList_PhaseRail asserts the job card renders the phase rail chips, the
// percentage span, and the phase-label line that the SSE script drives.
func TestJobsList_PhaseRail(t *testing.T) {
	s := newTestServer(t)
	s.manager.Enqueue(jobs.JobSpec{Label: "ep1", Task: func(context.Context, download.Progress) error { return nil }})
	h := s.Handler()
	r, w := get("/jobs/list")
	got := body(t, h, r, w)
	for _, want := range []string{
		`data-phase="subtitles"`,
		`data-phase="audio"`,
		`data-phase="video"`,
		`data-phase="mux"`,
		">SUB<",
		">AUDIO<",
		">VIDEO<",
		">MUX<",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("phase rail missing %q; got: %s", want, got)
		}
	}
}

// TestJobsList_StatusSections asserts the Jobs page lists jobs grouped into the
// four English status sections (Queued / Downloading / Completed / Errors) in
// order, each with a header and count, and that empty sections are omitted. A
// standalone episode (no GroupID) renders alongside grouped ones under the same
// status section — the old season group headers are gone.
func TestJobsList_StatusSections(t *testing.T) {
	s := newTestServer(t)
	// Two grouped episodes that complete (Completed section), a failed one
	// (Errors section), and a running one (Downloading section). Queued is left
	// empty so it's omitted.
	doneA := s.manager.Enqueue(jobs.JobSpec{Label: "S02E01 — A", Title: "A", GroupID: "SEASON2", GroupLabel: "Frieren — Season 2", Task: func(context.Context, download.Progress) error { return nil }})
	doneB := s.manager.Enqueue(jobs.JobSpec{Label: "S02E02 — B", Title: "B", GroupID: "SEASON2", GroupLabel: "Frieren — Season 2", Task: func(context.Context, download.Progress) error { return nil }})
	failed := s.manager.Enqueue(jobs.JobSpec{Label: "boom", Title: "Boom", Task: func(context.Context, download.Progress) error { return fmt.Errorf("PSSH not found") }})
	running := s.manager.Enqueue(jobs.JobSpec{Label: "running", Title: "R", Task: func(ctx context.Context, p download.Progress) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	<-doneA.Done()
	<-doneB.Done()
	<-failed.Done()

	h := s.Handler()
	r, w := get("/jobs/list")
	got := body(t, h, r, w)
	// Downloading + Completed + Errors headers appear; Queued is omitted (empty).
	for _, want := range []string{"Downloading", "Completed", "Errors"} {
		if !strings.Contains(got, want) {
			t.Errorf("sections missing %q header; got: %s", want, got)
		}
	}
	if strings.Contains(got, "Queued") {
		t.Errorf("empty Queued section should be omitted; got: %s", got)
	}
	// Section order: Downloading before Completed before Errors.
	if d, c, e := strings.Index(got, "Downloading"), strings.Index(got, "Completed"), strings.Index(got, "Errors"); !(d < c && c < e) {
		t.Errorf("sections out of order: d=%d c=%d e=%d", d, c, e)
	}
	// The season group label is no longer rendered as a section header.
	if strings.Contains(got, "Frieren — Season 2") {
		t.Errorf("season group header should be gone; got: %s", got)
	}
	// Each card is still present regardless of grouping.
	for _, want := range []string{running.ID, failed.ID, doneA.ID, doneB.ID} {
		if !strings.Contains(got, want) {
			t.Errorf("card %q missing from sections; got: %s", want, got)
		}
	}
	// Clean up the running job's goroutine.
	s.manager.Cancel(running.ID)
	<-running.Done()
}

// TestSSE_JobEvents enqueues a job that announces a phase, publishes a segment
// and a message, then connects to its SSE stream and asserts the events arrive.
func TestSSE_JobEvents(t *testing.T) {
	s := newTestServer(t)
	task := func(ctx context.Context, p download.Progress) error {
		p.Phase("audio")
		p.Segment(1, 2)
		p.Printf("Downloading ja-JP audio...\n")
		return nil
	}
	job := s.manager.Enqueue(jobs.JobSpec{Label: "ep1", Task: task})
	h := s.Handler()

	r, w := get("/jobs/" + job.ID + "/events")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("SSE status %d, body %s", w.Code, w.Body.String())
	}
	got := w.Body.String()
	// Phase("audio") jumps the bar to its base (5/100); Segment(1,2) maps to
	// 5 + 30*1/2 = 20/100. The terminal status event is emitted before done.
	for _, want := range []string{
		"event: status",
		"event: phase",
		`"phase":"audio"`,
		"event: segment",
		`"done":5`,
		`"done":20`,
		`"total":100`,
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

// postJSON builds a POST request with a JSON body for the /api/* tests.
func postJSON(target, body string) (*http.Request, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r, httptest.NewRecorder()
}

func TestAPIHealth(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	r, w := get("/api/health")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, `"ok"`) {
		t.Errorf("health body missing ok: %s", got)
	}
	if !strings.Contains(got, `"version"`) {
		t.Errorf("health body missing version: %s", got)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Error("health should return JSON content type")
	}
}

func TestAPIDownload_Happy(t *testing.T) {
	s := newTestServer(t)
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return nil }
	}
	h := s.Handler()
	r, w := postJSON(`/api/download`, `{"kind":"episode","id":"ep1","audio":["ja-JP"]}`)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body: %s)", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, `"jobId"`) {
		t.Errorf("expected jobId in body, got: %s", got)
	}
	if len(s.manager.List()) != 1 {
		t.Errorf("expected one enqueued job, got %d", len(s.manager.List()))
	}
}

func TestAPIDownload_NoAudio(t *testing.T) {
	s := newTestServer(t)
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return nil }
	}
	h := s.Handler()
	r, w := postJSON(`/api/download`, `{"kind":"episode","id":"ep1","audio":[]}`)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("expected error field, got: %s", w.Body.String())
	}
	if len(s.manager.List()) != 0 {
		t.Error("validation failure must not enqueue a job")
	}
}

func TestAPIDownload_NotConfigured(t *testing.T) {
	s := newTestServer(t)
	// No buildTask: server is unconfigured.
	h := s.Handler()
	r, w := postJSON(`/api/download`, `{"kind":"episode","id":"ep1","audio":["ja-JP"]}`)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "etp_rt") {
		t.Errorf("expected etp_rt in error, got: %s", w.Body.String())
	}
}

func TestAPIJobs_FoundAndMissing(t *testing.T) {
	s := newTestServer(t)
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return nil }
	}
	job := s.manager.Enqueue(jobs.JobSpec{Label: "ep1", Title: "Pilot", SeriesTitle: "Frieren", SeasonNumber: 1, EpisodeNumber: 3, Task: func(context.Context, download.Progress) error { return nil }})

	h := s.Handler()
	r, w := get("/api/jobs/" + job.ID)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, `"status"`) {
		t.Errorf("job body missing status: %s", got)
	}
	if !strings.Contains(got, job.ID) {
		t.Errorf("job body missing id: %s", got)
	}
	if !strings.Contains(got, `"seriesTitle"`) || !strings.Contains(got, "Frieren") {
		t.Errorf("job body missing display metadata: %s", got)
	}

	// Missing job -> 404.
	r2, w2 := get("/api/jobs/nope")
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown job, got %d", w2.Code)
	}
}

// TestAPIDownload_SeasonDualForm asserts a season enqueue returns jobId (the
// first episode's id) plus a jobs array (one entry per episode).
func TestAPIDownload_SeasonDualForm(t *testing.T) {
	s := newTestServer(t)
	s.buildSeasonTasks = func(string, DownloadOpts) ([]jobs.JobSpec, error) {
		return []jobs.JobSpec{
			{Label: "S02E01 — A", Title: "A", SeasonNumber: 2, EpisodeNumber: 1, Task: func(context.Context, download.Progress) error { return nil }},
			{Label: "S02E02 — B", Title: "B", SeasonNumber: 2, EpisodeNumber: 2, Task: func(context.Context, download.Progress) error { return nil }},
		}, nil
	}
	h := s.Handler()
	r, w := postJSON(`/api/download`, `{"kind":"season","id":"SEASON2","audio":["ja-JP"]}`)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body: %s)", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, `"jobId"`) {
		t.Errorf("expected jobId in body, got: %s", got)
	}
	if !strings.Contains(got, `"jobs"`) {
		t.Errorf("expected jobs array in body, got: %s", got)
	}
	if n := len(s.manager.List()); n != 2 {
		t.Errorf("expected 2 jobs, got %d", n)
	}
}

// TestEnqueue_EpisodeMetadataOnJob asserts the single-episode path populates the
// job's display metadata from GetEpisodeInfo so the card shows the title +
// thumbnail + series eyebrow.
func TestEnqueue_EpisodeMetadataOnJob(t *testing.T) {
	s := newTestServer(t)
	s.api = &fakeAPI{
		info: media.EpisodeInfo{
			Title: "Pilot",
			EpisodeMetadata: media.EpisodeMetadata{
				SeasonNumber:  1,
				EpisodeNumber: 1,
				Images: media.Images{Thumbnail: [][]media.Image{{{
					Source: "https://img/pilot.jpg",
				}}}},
			},
		},
	}
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return nil }
	}
	js, err := s.enqueue("episode", "ep1", DownloadOpts{})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if len(js) != 1 {
		t.Fatalf("expected 1 job, got %d", len(js))
	}
	j := js[0]
	if j.Title != "Pilot" || j.ImageURL != "https://img/pilot.jpg" || j.SeasonNumber != 1 || j.EpisodeNumber != 1 {
		t.Errorf("episode metadata not on job: %+v", j)
	}
}

// TestSettings_PersistMaxConcurrent asserts the Downloads form clamps and
// persists the max-concurrent value (and never drops the token).
func TestSettings_PersistMaxConcurrent(t *testing.T) {
	s := newTestServer(t)
	s.cfg = config{EtpRt: "tok-secret", MaxConcurrent: 3}
	if err := s.saveConfig(s.cfg); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	// 42 clamps to 8.
	r, w := postForm("/settings/downloads", "maxConcurrent=42")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	cfg, err := s.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EtpRt != "tok-secret" {
		t.Errorf("persist must keep etpRt, got %q", cfg.EtpRt)
	}
	if cfg.MaxConcurrent != 8 {
		t.Errorf("maxConcurrent should clamp to 8, got %d", cfg.MaxConcurrent)
	}
}

// TestPersistLastOpts_KeepsTokenAndStores asserts that a successful download
// persists the user's choices AND never drops the sensitive etpRt or the
// session output dir (read-modify-write, not a literal overwrite). This is the
// same s.cfg-merge shape configure uses to save a token, so it also covers the
// "re-saving a token must not wipe prefs" invariant.
func TestPersistLastOpts_KeepsTokenAndStores(t *testing.T) {
	s := newTestServer(t)
	s.cfg = config{EtpRt: "tok-secret", OutputDir: "/srv/media"}
	if err := s.saveConfig(s.cfg); err != nil {
		t.Fatal(err)
	}
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return nil }
	}
	h := s.Handler()
	r, w := postForm("/downloads", "kind=episode&id=ep1&videoQuality=720p&audioQuality=128k&audioLangs=en-US&subsLangs=pt-BR&format=mp4&outputDir=/out/x")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	cfg, err := s.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EtpRt != "tok-secret" {
		t.Errorf("persist must keep etpRt, got %q", cfg.EtpRt)
	}
	if cfg.OutputDir != "/srv/media" {
		t.Errorf("persist must keep session outputDir, got %q", cfg.OutputDir)
	}
	if cfg.LastVideoQuality != "720p" || cfg.LastAudioQuality != "128k" || cfg.LastFormat != "mp4" {
		t.Errorf("last-used scalar fields not stored: %+v", cfg)
	}
	if len(cfg.LastAudioLangs) != 1 || cfg.LastAudioLangs[0] != "en-US" {
		t.Errorf("last audio langs not stored: %v", cfg.LastAudioLangs)
	}
	if len(cfg.LastSubsLangs) != 1 || cfg.LastSubsLangs[0] != "pt-BR" {
		t.Errorf("last subs langs not stored: %v", cfg.LastSubsLangs)
	}
	if cfg.LastOutputDir != "/out/x" {
		t.Errorf("last output dir not stored: %q", cfg.LastOutputDir)
	}
}

// TestPersistLastOpts_APIPath asserts the JSON /api/download surface also
// persists choices (and keeps the token), since both surfaces share the helper.
func TestPersistLastOpts_APIPath(t *testing.T) {
	s := newTestServer(t)
	s.cfg = config{EtpRt: "tok-secret"}
	if err := s.saveConfig(s.cfg); err != nil {
		t.Fatal(err)
	}
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return nil }
	}
	h := s.Handler()
	r, w := postJSON(`/api/download`, `{"kind":"episode","id":"ep1","audio":["en-US"],"quality":"720p","format":"mp4","location":"/out/api"}`)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body: %s)", w.Code, w.Body.String())
	}
	cfg, err := s.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EtpRt != "tok-secret" {
		t.Errorf("api persist must keep etpRt, got %q", cfg.EtpRt)
	}
	if cfg.LastFormat != "mp4" || cfg.LastVideoQuality != "720p" || cfg.LastOutputDir != "/out/api" {
		t.Errorf("api persist lost fields: %+v", cfg)
	}
}

// TestDownloadNew_PreFillsFromSaved asserts the modal pre-selects the persisted
// last-used options instead of the hardcoded defaults.
func TestDownloadNew_PreFillsFromSaved(t *testing.T) {
	s := newTestServer(t)
	s.cfg = config{
		LastFormat:       "mp4",
		LastVideoQuality: "720p",
		LastAudioQuality: "128k",
		LastAudioLangs:   []string{"en-US", "pt-BR"},
		LastSubsLangs:    []string{"pt-BR"},
		LastOutputDir:    "/out/y",
	}
	h := s.Handler()
	r, w := get("/downloads/new?kind=episode&id=ep1")
	got := body(t, h, r, w)
	if !strings.Contains(got, `value="mp4" selected`) {
		t.Errorf("saved format mp4 should be pre-selected; got: %s", got)
	}
	if !strings.Contains(got, `value="720p" selected`) {
		t.Errorf("saved video quality 720p should be pre-selected; got: %s", got)
	}
	if strings.Contains(got, `value="1080p" selected`) {
		t.Error("1080p must not be selected when 720p is the saved quality")
	}
	if !strings.Contains(got, `value="/out/y"`) {
		t.Errorf("saved output dir should be pre-filled; got: %s", got)
	}
	// Two audio locales + one subtitle locale saved -> three checked boxes
	// (defaults would be one audio + one sub = two).
	if c := strings.Count(got, "checked"); c != 3 {
		t.Errorf("expected 3 checked boxes (2 audio + 1 sub), got %d", c)
	}
}

// TestConfig_RoundTrip asserts the new last-used fields survive a save/load
// cycle, so a restart pre-fills the modal.
func TestConfig_RoundTrip(t *testing.T) {
	s := newTestServer(t)
	in := config{
		EtpRt:            "tok",
		OutputDir:        "/srv",
		LastVideoQuality: "720p",
		LastAudioQuality: "128k",
		LastFormat:       "mp4",
		LastAudioLangs:   []string{"en-US", "pt-BR"},
		LastSubsLangs:    []string{"pt-BR"},
		LastOutputDir:    "/out",
	}
	if err := s.saveConfig(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if out.EtpRt != in.EtpRt || out.OutputDir != in.OutputDir ||
		out.LastVideoQuality != in.LastVideoQuality || out.LastAudioQuality != in.LastAudioQuality ||
		out.LastFormat != in.LastFormat || out.LastOutputDir != in.LastOutputDir {
		t.Errorf("round-trip lost scalar fields: in=%+v out=%+v", in, out)
	}
	if len(out.LastAudioLangs) != 2 || out.LastAudioLangs[0] != "en-US" || out.LastAudioLangs[1] != "pt-BR" {
		t.Errorf("audio langs not round-tripped: %v", out.LastAudioLangs)
	}
	if len(out.LastSubsLangs) != 1 || out.LastSubsLangs[0] != "pt-BR" {
		t.Errorf("subs langs not round-tripped: %v", out.LastSubsLangs)
	}
}

func TestAPI_CORSHeaders(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	// Same-origin preflight under /api/ -> 204 with that origin reflected.
	// The server is 127.0.0.1:8080; a same-origin browser fetch carries that
	// Origin (host:port equal to the request Host) and must be allowed + reflected.
	const sameOrigin = "http://127.0.0.1:8080"
	r, w := httptest.NewRequest(http.MethodOptions, "http://127.0.0.1:8080/api/download", nil), httptest.NewRecorder()
	r.Header.Set("Origin", sameOrigin)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != sameOrigin {
		t.Errorf("expected ACAO to reflect the same origin, got %q", got)
	}
	if got := w.Header().Get("Vary"); got != "Origin" {
		t.Errorf("expected Vary: Origin, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "POST, OPTIONS, GET" {
		t.Errorf("expected methods, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Errorf("expected headers, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got != "86400" {
		t.Errorf("expected max-age, got %q", got)
	}

	// A no-Origin request (curl / same-machine tool) is allowed and gets no
	// ACAO header (none is needed — it is not a browser).
	r2, w2 := httptest.NewRequest(http.MethodOptions, "/api/download", nil), httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for no-origin preflight, got %d", w2.Code)
	}
	if got := w2.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("no-origin request should not get an ACAO header, got %q", got)
	}

	// A drive-by https page is blocked: the preflight gets no CORS headers, so
	// the browser will refuse to read the eventual response.
	r3, w3 := httptest.NewRequest(http.MethodOptions, "/api/download", nil), httptest.NewRecorder()
	r3.Header.Set("Origin", "https://attacker.example")
	h.ServeHTTP(w3, r3)
	if w3.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on blocked preflight, got %d", w3.Code)
	}
	if got := w3.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("blocked origin must not get ACAO, got %q", got)
	}
	// And an actual blocked request is refused with 403.
	r4, w4 := postJSON(`/api/download`, `{"kind":"episode","id":"ep1","audio":["ja-JP"]}`)
	r4.Header.Set("Origin", "https://attacker.example")
	h.ServeHTTP(w4, r4)
	if w4.Code != http.StatusForbidden {
		t.Errorf("expected 403 for disallowed actual request, got %d (body: %s)", w4.Code, w4.Body.String())
	}
	if got := w4.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("blocked actual request must not get ACAO, got %q", got)
	}

	// Path normalization: /api/../settings cleans to /settings, so CORS must
	// NOT be applied (the mux would redirect this to the HTML /settings route).
	r5, w5 := httptest.NewRequest(http.MethodOptions, "/api/../settings", nil), httptest.NewRecorder()
	r5.Header.Set("Origin", sameOrigin)
	h.ServeHTTP(w5, r5)
	if got := w5.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("path-traversal /api/../settings must not get CORS headers, got ACAO=%q", got)
	}

	// CORS must NOT leak onto HTML routes.
	r6, w6 := get("/settings")
	h.ServeHTTP(w6, r6)
	if got := w6.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("CORS must be scoped to /api/*, but /settings got ACAO=%q", got)
	}
}

// waitUntil polls cond until it returns true or the timeout elapses, for tests
// that assert an asynchronous watcher goroutine eventually does its work.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true within timeout")
}

// TestSortSeasonEpisodesAscending asserts a season's episodes are sorted by
// episode number so a download starts at the smallest episode and climbs.
func TestSortSeasonEpisodesAscending(t *testing.T) {
	eps := []media.SeasonEpisode{{EpisodeNumber: 3}, {EpisodeNumber: 1}, {EpisodeNumber: 2}}
	sortSeasonEpisodesAscending(eps)
	for i, want := range []int{1, 2, 3} {
		if eps[i].EpisodeNumber != want {
			t.Errorf("eps[%d] = %d, want %d", i, eps[i].EpisodeNumber, want)
		}
	}
}

// TestSortSeasonsAscending asserts a series' seasons are sorted by season number
// so a series download starts at season 1 and climbs.
func TestSortSeasonsAscending(t *testing.T) {
	seasons := []media.Season{{SeasonNumber: 2}, {SeasonNumber: 1}, {SeasonNumber: 3}}
	sortSeasonsAscending(seasons)
	for i, want := range []int{1, 2, 3} {
		if seasons[i].SeasonNumber != want {
			t.Errorf("seasons[%d] = %d, want %d", i, seasons[i].SeasonNumber, want)
		}
	}
}

// TestEnqueue_KeepsDoneJob enqueues an episode that completes successfully and
// asserts it STAYS in the Manager — completed downloads are no longer
// auto-removed, so the Jobs page's Completed section is meaningful (the user
// deletes a finished card explicitly). The stored restart opts are retained too
// (restart is a session-only affordance cleared only by the explicit Delete route).
func TestEnqueue_KeepsDoneJob(t *testing.T) {
	s := newTestServer(t)
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return nil }
	}
	js, err := s.enqueue("episode", "EP1", DownloadOpts{AudioLangs: []string{"ja-JP"}})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	id := js[0].ID
	<-js[0].Done()
	// Give any would-be auto-remover ample time to (wrongly) fire; it must not.
	time.Sleep(100 * time.Millisecond)
	if _, ok := s.manager.Get(id); !ok {
		t.Errorf("done job should stay in the manager (no auto-removal), but it was removed")
	}
	if _, ok := s.restartOptsFor(id); !ok {
		t.Errorf("restart opts should be retained for a done job until the user deletes it")
	}
}

// TestEnqueue_KeepsFailedJob enqueues an episode that fails and asserts it is NOT
// removed — failed jobs stay so the user can see the failure and Restart or
// Delete. (Done jobs also stay now; this test guards the failure case.)
func TestEnqueue_KeepsFailedJob(t *testing.T) {
	s := newTestServer(t)
	s.buildTask = func(string, DownloadOpts) jobs.Task {
		return func(context.Context, download.Progress) error { return fmt.Errorf("boom") }
	}
	js, err := s.enqueue("episode", "EP1", DownloadOpts{AudioLangs: []string{"ja-JP"}})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	<-js[0].Done()
	time.Sleep(100 * time.Millisecond)
	if _, ok := s.manager.Get(js[0].ID); !ok {
		t.Errorf("failed job should NOT be removed, but it was")
	}
}

// enqueueDoneWithOutput enqueues a job whose task announces the given output
// file (name + path) via Progress.Output then returns nil, and blocks until the
// job reaches StatusDone. The output file is NOT created here — callers create
// it (or not) to set up the case under test.
func enqueueDoneWithOutput(t *testing.T, s *Server, name, path string) *jobs.Job {
	t.Helper()
	j := s.manager.Enqueue(jobs.JobSpec{
		Label: "ep",
		Task: func(ctx context.Context, p download.Progress) error {
			p.Output(name, path)
			return nil
		},
	})
	select {
	case <-j.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("job never reached a terminal state")
	}
	if j.Status() != jobs.StatusDone {
		t.Fatalf("status = %s, want done", j.Status())
	}
	return j
}

// jobFileReq builds a GET /jobs/{id}/file request with a configurable peer
// RemoteAddr (loopback for the host, a foreign IP for a remote client).
func jobFileReq(id, remoteAddr string) (*http.Request, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(http.MethodGet, "/jobs/"+id+"/file", nil)
	r.RemoteAddr = remoteAddr
	return r, httptest.NewRecorder()
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return true
}

// TestHandleJobFile_RemoteDeletes: a remote client grabs a done job's file → the
// file is streamed and then removed from the host, and the job's output pointer
// is cleared so a second grab 404s. This is the phone's "ship + delete" path.
func TestHandleJobFile_RemoteDeletes(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	fp := filepath.Join(dir, "ep.mkv")
	if err := os.WriteFile(fp, []byte("video-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	j := enqueueDoneWithOutput(t, s, "ep.mkv", fp)
	h := s.Handler()

	r, w := jobFileReq(j.ID, "192.168.99.99:4242")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "ep.mkv") {
		t.Errorf("Content-Disposition = %q, want filename ep.mkv", w.Header().Get("Content-Disposition"))
	}
	if fileExists(t, fp) {
		t.Error("remote grab should have deleted the file from the host")
	}
	if j.OutputPath() != "" {
		t.Error("output pointer should be cleared after the remote grab")
	}

	// Second grab 404s (file gone + pointer cleared).
	r2, w2 := jobFileReq(j.ID, "192.168.99.99:4242")
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusNotFound {
		t.Errorf("second grab status = %d, want 404", w2.Code)
	}
}

// TestHandleJobFile_LocalKeeps: the host (loopback, or its own LAN IP) grabs a
// done job's file → the file is served but KEPT, so the Mac's own downloads
// persist (no "ship + delete" for the server itself).
func TestHandleJobFile_LocalKeeps(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	fp := filepath.Join(dir, "ep.mkv")
	if err := os.WriteFile(fp, []byte("video-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	j := enqueueDoneWithOutput(t, s, "ep.mkv", fp)
	h := s.Handler()

	r, w := jobFileReq(j.ID, "127.0.0.1:4242")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !fileExists(t, fp) {
		t.Error("local grab should keep the file on the host")
	}
	if j.OutputPath() == "" {
		t.Error("output pointer should still be set after a local grab")
	}
}

// TestHandleJobFile_NoOutput: a done job that never announced an output file
// 404s (nothing to serve).
func TestHandleJobFile_NoOutput(t *testing.T) {
	s := newTestServer(t)
	j := enqueueDoneWithOutput(t, s, "", "") // no output announced
	h := s.Handler()
	r, w := jobFileReq(j.ID, "127.0.0.1:4242")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestHandleJobFile_MissingFile: a done job whose output file no longer exists
// 404s and clears the stale pointer.
func TestHandleJobFile_MissingFile(t *testing.T) {
	s := newTestServer(t)
	j := enqueueDoneWithOutput(t, s, "gone.mkv", "/no/such/path/gone.mkv")
	h := s.Handler()
	r, w := jobFileReq(j.ID, "127.0.0.1:4242")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if j.OutputPath() != "" {
		t.Error("stale output pointer should be cleared when the file is missing")
	}
}

// TestHandleJobFile_UnknownJob: a grab for an id the Manager does not know 404s.
func TestHandleJobFile_UnknownJob(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	r, w := jobFileReq("does-not-exist", "127.0.0.1:4242")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestIsLocalPeer: loopback is local; a foreign IP is remote; the host's own
// non-loopback IPs are local (the Mac reaching itself over its LAN IP).
func TestIsLocalPeer(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:1", "[::1]:1"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = addr
		if !isLocalPeer(r) {
			t.Errorf("isLocalPeer(%s) = false, want true (loopback is local)", addr)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.7:1"
	if isLocalPeer(r) {
		t.Error("isLocalPeer(203.0.113.7) = true, want false (foreign IP is remote)")
	}
}
