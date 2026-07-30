package drm

import (
	"bytes"
	"os"
	"path/filepath"
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

// TestFindWidevineFile covers the directory search (data-dir first, then CWD)
// without needing a real CDM — only the file-discovery logic is exercised. Each
// subtest chdir's into a fresh temp dir (t.Chdir) so the repo's own drm.wvd in the
// process CWD can't leak into the results.
func TestFindWidevineFile(t *testing.T) {
	t.Run("wvd in data-dir wins", func(t *testing.T) {
		dataDir := t.TempDir()
		wvd := filepath.Join(dataDir, "foo.wvd")
		if err := os.WriteFile(wvd, []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(t.TempDir()) // isolated CWD, no device files
		target, kind, ok := findWidevineFile([]string{dataDir})
		if !ok || kind != kindWVD || target != wvd {
			t.Errorf("findWidevineFile = (%q, %v, %v), want (%q, kindWVD, true)", target, kind, ok, wvd)
		}
	})

	t.Run("falls back to CWD when data-dir has no device", func(t *testing.T) {
		dataDir := t.TempDir() // empty
		cwd := t.TempDir()
		wvd := filepath.Join(cwd, "bar.wvd")
		if err := os.WriteFile(wvd, []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(cwd)
		target, kind, ok := findWidevineFile([]string{dataDir})
		if !ok || kind != kindWVD || target != wvd {
			t.Errorf("findWidevineFile = (%q, %v, %v), want (%q, kindWVD, true)", target, kind, ok, wvd)
		}
	})

	t.Run("empty dirs falls back to CWD only", func(t *testing.T) {
		cwd := t.TempDir()
		wvd := filepath.Join(cwd, "x.wvd")
		if err := os.WriteFile(wvd, []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(cwd)
		target, kind, ok := findWidevineFile(nil)
		if !ok || kind != kindWVD || target != wvd {
			t.Errorf("findWidevineFile = (%q, %v, %v), want (%q, kindWVD, true)", target, kind, ok, wvd)
		}
	})

	t.Run("raw pair in data-dir", func(t *testing.T) {
		dataDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dataDir, "client_id.bin"), []byte("cid"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, "private_key.pem"), []byte("key"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(t.TempDir()) // isolated CWD
		target, kind, ok := findWidevineFile([]string{dataDir})
		if !ok || kind != kindRaw || target != dataDir {
			t.Errorf("findWidevineFile = (%q, %v, %v), want (%q, kindRaw, true)", target, kind, ok, dataDir)
		}
	})

	t.Run("none when no device anywhere", func(t *testing.T) {
		dataDir := t.TempDir() // empty
		t.Chdir(t.TempDir())   // isolated CWD, empty
		_, _, ok := findWidevineFile([]string{dataDir})
		if ok {
			t.Fatal("expected no device, found one")
		}
	})

	t.Run("wvd preferred over raw pair", func(t *testing.T) {
		// data-dir holds a raw pair; CWD holds a .wvd. The .wvd (anywhere) must win.
		dataDir := t.TempDir()
		os.WriteFile(filepath.Join(dataDir, "client_id.bin"), []byte("cid"), 0o644)
		os.WriteFile(filepath.Join(dataDir, "private_key.pem"), []byte("key"), 0o644)
		cwd := t.TempDir()
		wvd := filepath.Join(cwd, "pick.wvd")
		os.WriteFile(wvd, []byte("dummy"), 0o644)
		t.Chdir(cwd)
		target, kind, ok := findWidevineFile([]string{dataDir})
		if !ok || kind != kindWVD || target != wvd {
			t.Errorf("findWidevineFile = (%q, %v, %v), want (%q, kindWVD, true)", target, kind, ok, wvd)
		}
	})
}
