package drm

import (
	"bytes"
	"testing"

	"github.com/iyear/gowidevine"
	"github.com/iyear/gowidevine/widevinepb"
)

func TestContentKey(t *testing.T) {
	t.Run("returns the CONTENT key when present", func(t *testing.T) {
		content := []byte{0xAB, 0xCD}
		keys := []*widevine.Key{
			{Type: widevinepb.License_KeyContainer_SIGNING, Key: []byte{1}},
			{Type: widevinepb.License_KeyContainer_CONTENT, Key: content},
		}
		got, err := ContentKey(keys)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("ContentKey = %x, want %x", got, content)
		}
	})

	t.Run("error when only non-CONTENT types", func(t *testing.T) {
		keys := []*widevine.Key{
			{Type: widevinepb.License_KeyContainer_SIGNING, Key: []byte{1}},
		}
		if _, err := ContentKey(keys); err == nil {
			t.Fatal("expected error when no CONTENT key present")
		}
	})

	t.Run("error on empty slice", func(t *testing.T) {
		if _, err := ContentKey(nil); err == nil {
			t.Fatal("expected error for empty key set")
		}
	})
}
