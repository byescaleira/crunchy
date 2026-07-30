package download

import (
	"context"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/iyear/gowidevine"
	"github.com/iyear/gowidevine/widevinepb"
	"github.com/unki2aut/go-mpd"

	"crunchyroll-downloader/internal/media"
	"crunchyroll-downloader/internal/mux"
)

// event is one step of the Episode orchestration, recorded in order so the
// keys-ordering invariant can be asserted.
type event struct {
	kind string // "license", "audio", "video", "merge"
	keys []*widevine.Key
}

type fakeAPI struct {
	mu       sync.Mutex
	events   []event
	manifest *mpd.MPD
}

func (f *fakeAPI) record(e event) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

func (f *fakeAPI) GetEpisode(id string) (media.Episode, error) {
	return media.Episode{ManifestURL: "manifest", Token: "tok-" + id}, nil
}
func (f *fakeAPI) GetEpisodeInfo(id string) (media.EpisodeInfo, error) {
	return media.EpisodeInfo{}, nil
}
func (f *fakeAPI) GetSeasons(string, string, string) ([]media.Season, error) {
	return nil, nil
}
func (f *fakeAPI) GetSeasonEpisodes(string, string, string) ([]media.SeasonEpisode, error) {
	return nil, nil
}
func (f *fakeAPI) GetSeries(string) (media.Series, error) {
	return media.Series{}, nil
}
func (f *fakeAPI) DownloadImage(string) (string, error) {
	return "", nil
}
func (f *fakeAPI) DeleteStream(string, string) (bool, error) { return true, nil }
func (f *fakeAPI) ParseManifest(string) (*mpd.MPD, error)    { return f.manifest, nil }
func (f *fakeAPI) GetLicense(psshData, contentId, videoToken string) ([]*widevine.Key, error) {
	idx := len(f.events) // before recording
	keys := []*widevine.Key{{Type: widevinepb.License_KeyContainer_CONTENT, Key: []byte{byte(idx)}}}
	f.record(event{kind: "license", keys: keys})
	return keys, nil
}

// TestDownloadEpisode_VersionKeyOrdering pins the Widevine keys-ordering
// invariant: audio for version i is downloaded (and therefore decrypted) with
// version i's keys, and that happens before version i+1's GetLicense. The
// invariant is now structural (versionKeys is loop-local, downloads are
// synchronous), so this test guards against anyone reintroducing shared keys or
// parallelizing across versions.
func TestDownloadEpisode_VersionKeyOrdering(t *testing.T) {
	// Isolate the series directory Episode creates.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	pssh := "AAAA"
	videoID := "v1080"
	audioID := "audio/ja-JP-192"
	manifestData := &mpd.MPD{Period: []*mpd.Period{{
		AdaptationSets: []*mpd.AdaptationSet{
			{
				MimeType:           "video/mp4",
				ContentProtections: []mpd.Descriptor{{CencPSSH: &pssh}},
				Representations: []mpd.Representation{
					{ID: &videoID, Height: u64Ptr(1080), BaseURL: []*mpd.BaseURL{{Value: "v"}}},
				},
			},
			{
				MimeType: "audio/mp4",
				Representations: []mpd.Representation{
					{ID: &audioID, BaseURL: []*mpd.BaseURL{{Value: "a"}}},
				},
			},
		},
	}}}

	api := &fakeAPI{manifest: manifestData}

	d := &Downloader{
		API:          api,
		VideoQuality: "1080p",
		AudioQuality: "192k",
		AudioLangs:   []string{"ja-JP", "en-US"},
		// no subtitles: keeps the test off the subtitle I/O path
	}
	d.downloadTrack = func(ctx context.Context, baseUrl, representationId *string, set *mpd.AdaptationSet, keys []*widevine.Key) (string, error) {
		kind := "audio"
		if strings.HasPrefix(set.MimeType, "video") {
			kind = "video"
		}
		api.record(event{kind: kind, keys: keys})
		return "track-" + kind, nil
	}
	d.downloadSubtitles = func(ctx context.Context, url string) (string, error) { return "subs.ass", nil }
	d.merge = func(ctx context.Context, videoFile string, audioTracks, subTracks []mux.MediaTrack, outputFile, coverFile, format string, info media.EpisodeInfo) error {
		api.record(event{kind: "merge"})
		return nil
	}

	info := media.EpisodeInfo{
		Title: "Ep",
		EpisodeMetadata: media.EpisodeMetadata{
			SeriesTitle:   "TestSeries",
			SeasonNumber:  1,
			EpisodeNumber: 1,
			AudioLocale:   "ja-JP",
			Versions:      []*media.DubVersion{{AudioLocale: "en-US", GUID: "guid2"}},
		},
	}

	if err := d.Episode(context.Background(), "baseId", info); err != nil {
		t.Fatalf("Episode returned error: %v (events: %v)", err, eventKinds(api.events))
	}

	events := api.events
	// Expected order: license0, audio0, video0, license1, audio1, merge.
	wantKinds := []string{"license", "audio", "video", "license", "audio", "merge"}
	if len(events) < len(wantKinds) {
		t.Fatalf("got %d events, want at least %d: %+v", len(events), len(wantKinds), eventKinds(events))
	}
	for i, want := range wantKinds {
		if events[i].kind != want {
			t.Fatalf("event %d = %q, want %q (full order: %v)", i, events[i].kind, want, eventKinds(events))
		}
	}

	license0 := events[0].keys
	audio0 := events[1].keys
	video0 := events[2].keys
	license1 := events[3].keys
	audio1 := events[4].keys

	// Audio and video for version 0 decrypt with version 0's keys...
	if !reflect.DeepEqual(audio0, license0) {
		t.Errorf("audio0 keys = %x, want version0 keys %x", keyBytes(audio0), keyBytes(license0))
	}
	if !reflect.DeepEqual(video0, license0) {
		t.Errorf("video0 keys = %x, want version0 keys %x", keyBytes(video0), keyBytes(license0))
	}
	// ...audio for version 1 with version 1's keys...
	if !reflect.DeepEqual(audio1, license1) {
		t.Errorf("audio1 keys = %x, want version1 keys %x", keyBytes(audio1), keyBytes(license1))
	}
	// ...and the two versions got distinct keys.
	if reflect.DeepEqual(license0, license1) {
		t.Errorf("version 0 and version 1 share the same keys; expected distinct keys per version")
	}
	// The invariant itself: audio0 (event 1) precedes license1 (event 3), i.e.
	// version 0's audio is downloaded before version 1 is even licensed.
	if !(events[1].kind == "audio" && events[3].kind == "license") {
		t.Errorf("expected audio0 before license1; got order %v", eventKinds(events))
	}
}

func eventKinds(es []event) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.kind
	}
	return out
}

func keyBytes(ks []*widevine.Key) []byte {
	for _, k := range ks {
		return k.Key
	}
	return nil
}
