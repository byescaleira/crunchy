package manifest

import (
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/unki2aut/go-mpd"
)

func strPtr(s string) *string  { return &s }
func u64Ptr(v uint64) *uint64  { return &v }
func i64Ptr(v int64) *int64    { return &v }
func psshPtr(s string) *string { return &s }
func ctPtr(s string) *string   { return &s }

// captureStdout runs fn with os.Stdout redirected to a pipe and returns the
// captured text. Used to keep fmt.Printf fallback messages out of test output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	fn()
	w.Close()
	os.Stdout = orig
	return <-done
}

func TestExpandTimeline(t *testing.T) {
	tests := []struct {
		name     string
		timeline []*mpd.SegmentTimelineS
		start    int64
		want     []int64
	}{
		{"empty timeline", nil, 1, nil},
		{"single S no R", []*mpd.SegmentTimelineS{{D: 1000}}, 1, []int64{1}},
		{"R nil is one segment", []*mpd.SegmentTimelineS{{D: 1000, R: nil}}, 1, []int64{1}},
		{"R=0 is one segment", []*mpd.SegmentTimelineS{{D: 1000, R: i64Ptr(0)}}, 1, []int64{1}},
		{"R=3 is four segments", []*mpd.SegmentTimelineS{{D: 1000, R: i64Ptr(3)}}, 1, []int64{1, 2, 3, 4}},
		{"start offset", []*mpd.SegmentTimelineS{{D: 1000, R: i64Ptr(1)}}, 5, []int64{5, 6}},
		{"multiple S mixed R", []*mpd.SegmentTimelineS{
			{D: 1000, R: i64Ptr(1)}, // 2 segs: 1,2
			{D: 1000},               // 1 seg: 3
			{D: 1000, R: i64Ptr(2)}, // 3 segs: 4,5,6
		}, 1, []int64{1, 2, 3, 4, 5, 6}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandTimeline(tc.timeline, tc.start)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ExpandTimeline = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFindAdaptationSet(t *testing.T) {
	audioMime := "audio/mp4"
	videoMime := "video/mp4"
	contentTypeAudio := "audio"

	t.Run("no period returns error", func(t *testing.T) {
		_, err := FindAdaptationSet(&mpd.MPD{}, "audio")
		if err == nil {
			t.Fatal("expected error for empty manifest")
		}
	})

	t.Run("matches by mimeType prefix", func(t *testing.T) {
		m := &mpd.MPD{Period: []*mpd.Period{{
			AdaptationSets: []*mpd.AdaptationSet{
				{MimeType: videoMime},
				{MimeType: audioMime},
			},
		}}}
		got, err := FindAdaptationSet(m, "audio")
		if err != nil {
			t.Fatal(err)
		}
		if got.MimeType != audioMime {
			t.Errorf("got mimeType %q, want %q", got.MimeType, audioMime)
		}
	})

	t.Run("matches by contentType prefix", func(t *testing.T) {
		m := &mpd.MPD{Period: []*mpd.Period{{
			AdaptationSets: []*mpd.AdaptationSet{
				{MimeType: "video/mp4"},
				{ContentType: ctPtr(contentTypeAudio)},
			},
		}}}
		got, err := FindAdaptationSet(m, "audio")
		if err != nil {
			t.Fatal(err)
		}
		if got.ContentType == nil || *got.ContentType != contentTypeAudio {
			t.Errorf("did not match the contentType-based set")
		}
	})

	t.Run("no match returns error", func(t *testing.T) {
		m := &mpd.MPD{Period: []*mpd.Period{{
			AdaptationSets: []*mpd.AdaptationSet{{MimeType: "video/mp4"}},
		}}}
		if _, err := FindAdaptationSet(m, "audio"); err == nil {
			t.Fatal("expected error when no audio set present")
		}
	})
}

func TestGetBaseUrl(t *testing.T) {
	t.Run("video height match", func(t *testing.T) {
		set := &mpd.AdaptationSet{
			Representations: []mpd.Representation{
				{ID: strPtr("v0"), Height: u64Ptr(720), BaseURL: []*mpd.BaseURL{{Value: "720"}}},
				{ID: strPtr("v1"), Height: u64Ptr(1080), BaseURL: []*mpd.BaseURL{{Value: "1080"}}},
			},
		}
		base, id := GetBaseURL(set, true, "1080p")
		if base == nil || *base != "1080" {
			t.Errorf("video base = %v, want 1080", base)
		}
		if id == nil || *id != "v1" {
			t.Errorf("video id = %v, want v1", id)
		}
	})

	t.Run("audio id branch matches by quality substring", func(t *testing.T) {
		set := &mpd.AdaptationSet{
			Representations: []mpd.Representation{
				{ID: strPtr("audio/ja-JP-128"), BaseURL: []*mpd.BaseURL{{Value: "a128"}}},
				{ID: strPtr("audio/ja-JP-192"), BaseURL: []*mpd.BaseURL{{Value: "a192"}}},
			},
		}
		base, id := GetBaseURL(set, false, "ja-JP-192")
		if base == nil || *base != "a192" {
			t.Errorf("audio base = %v, want a192", base)
		}
		if id == nil || *id != "audio/ja-JP-192" {
			t.Errorf("audio id = %v, want audio/ja-JP-192", id)
		}
	})

	t.Run("audio bandwidth branch", func(t *testing.T) {
		set := &mpd.AdaptationSet{
			Representations: []mpd.Representation{
				{ID: strPtr("rep-96"), Bandwidth: u64Ptr(96000), BaseURL: []*mpd.BaseURL{{Value: "a96"}}},
				{ID: strPtr("rep-192"), Bandwidth: u64Ptr(192002), BaseURL: []*mpd.BaseURL{{Value: "a192"}}},
			},
		}
		base, id := GetBaseURL(set, false, "192k")
		if base == nil || *base != "a192" {
			t.Errorf("bandwidth base = %v, want a192", base)
		}
		if id == nil || *id != "rep-192" {
			t.Errorf("bandwidth id = %v, want rep-192", id)
		}
	})

	t.Run("no match defers to first representation", func(t *testing.T) {
		set := &mpd.AdaptationSet{
			Representations: []mpd.Representation{
				{ID: strPtr("only"), Height: u64Ptr(480), BaseURL: []*mpd.BaseURL{{Value: "first"}}},
			},
		}
		var base, id *string
		out := captureStdout(t, func() {
			base, id = GetBaseURL(set, true, "2160p")
		})
		if base == nil || *base != "first" {
			t.Errorf("fallback base = %v, want first", base)
		}
		if id == nil || *id != "only" {
			t.Errorf("fallback id = %v, want only", id)
		}
		if out == "" {
			t.Errorf("expected a fallback notice on stdout, got nothing")
		}
	})

	t.Run("empty reps returns nil nil", func(t *testing.T) {
		set := &mpd.AdaptationSet{}
		base, id := GetBaseURL(set, true, "1080p")
		if base != nil || id != nil {
			t.Errorf("empty reps = (%v,%v), want (nil,nil)", base, id)
		}
	})
}

func TestGetPSSH(t *testing.T) {
	t.Run("no period returns nil", func(t *testing.T) {
		if got := GetPSSH(&mpd.MPD{}); got != nil {
			t.Errorf("GetPSSH(empty) = %v, want nil", got)
		}
	})

	t.Run("finds pssh in a non-first adaptation set", func(t *testing.T) {
		m := &mpd.MPD{Period: []*mpd.Period{{
			AdaptationSets: []*mpd.AdaptationSet{
				{ContentProtections: []mpd.Descriptor{{}}}, // no pssh
				{ContentProtections: []mpd.Descriptor{{CencPSSH: psshPtr("AAAA")}}},
			},
		}}}
		got := GetPSSH(m)
		if got == nil || *got != "AAAA" {
			t.Errorf("GetPSSH = %v, want AAAA", got)
		}
	})

	t.Run("nil when no cenc pssh anywhere", func(t *testing.T) {
		m := &mpd.MPD{Period: []*mpd.Period{{
			AdaptationSets: []*mpd.AdaptationSet{
				{ContentProtections: []mpd.Descriptor{{SchemeIDURI: strPtr("widevine")}}},
			},
		}}}
		if got := GetPSSH(m); got != nil {
			t.Errorf("GetPSSH = %v, want nil", got)
		}
	})
}
