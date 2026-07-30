package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unki2aut/go-mpd"
)

func TestBuildURL(t *testing.T) {
	rep := "repID"
	num := int64(7)

	tests := []struct {
		name    string
		base    string
		repId   string
		file    string
		partNum *int64
		want    string
	}{
		{
			name:    "number substitution with nil partNum keeps placeholder",
			base:    "https://cdn/",
			repId:   rep,
			file:    "seg-$Number$-$RepresentationID$.mp4",
			partNum: nil,
			want:    "https://cdn/seg-$Number$-repID.mp4",
		},
		{
			name:    "number substitution with partNum",
			base:    "https://cdn/",
			repId:   rep,
			file:    "seg-$Number$-$RepresentationID$.mp4",
			partNum: &num,
			want:    "https://cdn/seg-00007-repID.mp4",
		},
		{
			name:    "explicit %05d variant",
			base:    "https://cdn/",
			repId:   rep,
			file:    "seg-$Number%05d$-$RepresentationID$.mp4",
			partNum: &num,
			want:    "https://cdn/seg-00007-repID.mp4",
		},
		{
			name:    "only representation id",
			base:    "https://cdn/",
			repId:   rep,
			file:    "init-$RepresentationID$.mp4",
			partNum: nil,
			want:    "https://cdn/init-repID.mp4",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildUrl(tc.base, tc.repId, tc.file, tc.partNum)
			if got != tc.want {
				t.Errorf("buildUrl = %q, want %q", got, tc.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
func u64Ptr(v uint64) *uint64 { return &v }

func TestGetFilename(t *testing.T) {
	t.Run("nil set returns subs temp", func(t *testing.T) {
		name := getFilename(nil)
		t.Cleanup(func() { os.Remove(name) })
		if !strings.HasSuffix(name, ".ass") {
			t.Errorf("nil set filename = %q, want .ass suffix", name)
		}
	})

	t.Run("set with Height rep returns video temp", func(t *testing.T) {
		set := &mpd.AdaptationSet{
			Representations: []mpd.Representation{
				{Height: u64Ptr(1080), BaseURL: []*mpd.BaseURL{{Value: "v"}}},
			},
		}
		name := getFilename(set)
		t.Cleanup(func() { os.Remove(name) })
		if !strings.HasSuffix(name, ".mp4") {
			t.Errorf("video filename = %q, want .mp4 suffix", name)
		}
		if !strings.Contains(filepath.Base(name), "crdl-video-") {
			t.Errorf("video filename = %q, want crdl-video- prefix", name)
		}
	})

	t.Run("set with Bandwidth rep (no Height) returns audio temp", func(t *testing.T) {
		set := &mpd.AdaptationSet{
			Representations: []mpd.Representation{
				{Bandwidth: u64Ptr(192000), BaseURL: []*mpd.BaseURL{{Value: "a"}}},
			},
		}
		name := getFilename(set)
		t.Cleanup(func() { os.Remove(name) })
		if !strings.Contains(filepath.Base(name), "crdl-audio-") {
			t.Errorf("audio filename = %q, want crdl-audio- prefix", name)
		}
		if !strings.HasSuffix(name, ".m4a") {
			t.Errorf("audio filename = %q, want .m4a suffix (AAC in MP4)", name)
		}
	})

	t.Run("set with no reps returns empty", func(t *testing.T) {
		set := &mpd.AdaptationSet{}
		if name := getFilename(set); name != "" {
			t.Errorf("empty set filename = %q, want empty", name)
		}
	})
}

func TestRemoveFiles(t *testing.T) {
	t.Run("empty slice is a no-op", func(t *testing.T) {
		removeFiles(nil) // must not panic
		removeFiles([]string{})
	})

	t.Run("removes real files and ignores empty/missing entries", func(t *testing.T) {
		f1, err := os.CreateTemp("", "rmtest-*.tmp")
		if err != nil {
			t.Fatal(err)
		}
		f2, err := os.CreateTemp("", "rmtest-*.tmp")
		if err != nil {
			t.Fatal(err)
		}
		f1.Close()
		f2.Close()

		removeFiles([]string{"", f1.Name(), "/nonexistent/path/here", f2.Name()})

		if _, err := os.Stat(f1.Name()); !os.IsNotExist(err) {
			t.Errorf("f1 still exists after removeFiles: %v", err)
		}
		if _, err := os.Stat(f2.Name()); !os.IsNotExist(err) {
			t.Errorf("f2 still exists after removeFiles: %v", err)
		}
	})
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "Unknown"},
		{"plain", "plain"},
		{`a\b/c:d*e?f<g>h|i`, "a_b_c_d_e_f_g_h_i"},
		{`"q"`, "_q_"},         // straight double quotes both replaced
		{`“smart”`, "_smart_"}, // curly double quotes both replaced
		{`'apos'`, "_apos_"},   // straight apostrophes replaced
		{"trailing space  ", "trailing space"},
		{"trailing dot...", "trailing dot"}, // dots are not illegal, but trimmed
		{"double__underscore", "double_underscore"},
		{"many____underscores", "many_underscores"},
		{"a__b___c", "a_b_c"},
		{"  leading kept  ", "  leading kept"}, // TrimRight keeps leading spaces
	}
	for _, tc := range tests {
		got := sanitize(tc.in)
		if got != tc.want {
			t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
