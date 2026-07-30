package main

import (
	"bytes"
	"testing"

	"github.com/iyear/gowidevine"
	"github.com/iyear/gowidevine/widevinepb"
	"github.com/unki2aut/go-mpd"
)

func TestContentKey(t *testing.T) {
	t.Run("returns the CONTENT key when present", func(t *testing.T) {
		content := []byte{0xAB, 0xCD}
		keys := []*widevine.Key{
			{Type: widevinepb.License_KeyContainer_SIGNING, Key: []byte{1}},
			{Type: widevinepb.License_KeyContainer_CONTENT, Key: content},
		}
		got, err := contentKey(keys)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("contentKey = %x, want %x", got, content)
		}
	})

	t.Run("error when only non-CONTENT types", func(t *testing.T) {
		keys := []*widevine.Key{
			{Type: widevinepb.License_KeyContainer_SIGNING, Key: []byte{1}},
		}
		if _, err := contentKey(keys); err == nil {
			t.Fatal("expected error when no CONTENT key present")
		}
	})

	t.Run("error on empty slice", func(t *testing.T) {
		if _, err := contentKey(nil); err == nil {
			t.Fatal("expected error for empty key set")
		}
	})
}

func psshPtr(s string) *string { return &s }

func TestGetPSSH(t *testing.T) {
	t.Run("no period returns nil", func(t *testing.T) {
		if got := getPssh(&mpd.MPD{}); got != nil {
			t.Errorf("getPssh(empty) = %v, want nil", got)
		}
	})

	t.Run("finds pssh in a non-first adaptation set", func(t *testing.T) {
		m := &mpd.MPD{Period: []*mpd.Period{{
			AdaptationSets: []*mpd.AdaptationSet{
				{ContentProtections: []mpd.Descriptor{{}}}, // no pssh
				{ContentProtections: []mpd.Descriptor{{CencPSSH: psshPtr("AAAA")}}},
			},
		}}}
		got := getPssh(m)
		if got == nil || *got != "AAAA" {
			t.Errorf("getPssh = %v, want AAAA", got)
		}
	})

	t.Run("nil when no cenc pssh anywhere", func(t *testing.T) {
		m := &mpd.MPD{Period: []*mpd.Period{{
			AdaptationSets: []*mpd.AdaptationSet{
				{ContentProtections: []mpd.Descriptor{{SchemeIDURI: strPtr("widevine")}}},
			},
		}}}
		if got := getPssh(m); got != nil {
			t.Errorf("getPssh = %v, want nil", got)
		}
	})
}
