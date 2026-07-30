// Package drm wraps the Widevine CDM helpers that don't need the Crunchyroll
// HTTP/token layer: selecting the CONTENT key from a license key set, and
// loading a Widevine device (".wvd" or the client_id.bin/private_key.pem pair)
// from a set of search directories (the server's data-dir first, then the working
// directory) so the installed `crunchy` command finds the CDM no matter where it
// is run. The license exchange itself lives in the crunchy package because it
// needs the bearer token and the playback token.
package drm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/iyear/gowidevine"
	"github.com/iyear/gowidevine/widevinepb"
)

// ContentKey returns the Widevine CONTENT key from a license key set, matching
// the selection that widevine.DecryptMP4Auto performs internally. We decrypt
// segment-by-segment ourselves (instead of calling DecryptMP4Auto on the whole
// file) so the key has to be resolved here.
func ContentKey(ks []*widevine.Key) ([]byte, error) {
	for _, k := range ks {
		if k.Type == widevinepb.License_KeyContainer_CONTENT {
			return k.Key, nil
		}
	}
	return nil, errors.New("no CONTENT key type found in the provided key set")
}

// Widevine device file kinds surfaced by findWidevineFile.
const (
	kindNone = iota
	kindWVD  // a single *.wvd file
	kindRaw  // the client_id.bin + private_key.pem pair in one directory
)

// findWidevineFile resolves a Widevine CDM across the given search directories
// (scanned in order, then the working directory "." as a final fallback). It
// prefers a single ".wvd" file; otherwise it looks for the
// "client_id.bin" + "private_key.pem" pair in the same directory. Both files of
// the raw pair must live in the same directory, and they are opened by name so
// the result no longer depends on directory sort order (the previous single-pass
// loop would break early after private_key.pem and miss a client_id.bin that
// sorted later, leaving the client ID empty and silently returning "no device").
//
// Returns (target, kind, ok): for kindWVD, target is the path to the .wvd file;
// for kindRaw, target is the directory holding the pair. ok is false when no
// device files are present in any searched directory.
func findWidevineFile(dirs []string) (string, int, bool) {
	// Always fall back to the working directory last, resolved to an absolute
	// path so the returned target is unambiguous and stays valid even if the
	// caller's CWD shifts before the file is opened.
	search := append([]string{}, dirs...)
	if cwd, err := os.Getwd(); err == nil {
		search = append(search, cwd)
	} else {
		search = append(search, ".")
	}

	// Pass 1: prefer a .wvd file in any searched directory (first one wins).
	for _, dir := range search {
		if dir == "" {
			continue
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, file := range files {
			if !file.IsDir() && strings.HasSuffix(file.Name(), ".wvd") {
				return filepath.Join(dir, file.Name()), kindWVD, true
			}
		}
	}

	// Pass 2: the client_id.bin + private_key.pem pair in the same directory.
	for _, dir := range search {
		if dir == "" {
			continue
		}
		cid, errCID := os.Stat(filepath.Join(dir, "client_id.bin"))
		key, errKey := os.Stat(filepath.Join(dir, "private_key.pem"))
		if errCID == nil && errKey == nil && !cid.IsDir() && !key.IsDir() {
			return dir, kindRaw, true
		}
	}
	return "", kindNone, false
}

// LoadWidevineDevice loads a Widevine CDM from the given search directories
// (scanned in order, then the working directory). It prefers a single ".wvd"
// file; otherwise it looks for the "client_id.bin" + "private_key.pem" pair. The
// variadic dirs let the server pass its data-dir so the installed `crunchy`
// command finds the CDM regardless of the directory it is run from; the CLI
// downloader calls it with no dirs, which falls back to the working directory
// only (today's behavior).
//
// It returns (nil, nil) when no device files are present in any searched
// directory so the caller can surface a friendly "no widevine device provided"
// message.
func LoadWidevineDevice(dirs ...string) (*widevine.Device, error) {
	target, kind, ok := findWidevineFile(dirs)
	if !ok {
		return nil, nil
	}
	switch kind {
	case kindWVD:
		f, err := os.Open(target)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return widevine.NewDevice(widevine.FromWVD(f))
	case kindRaw:
		clientID, err := os.ReadFile(filepath.Join(target, "client_id.bin"))
		if err != nil {
			return nil, nil
		}
		privateKey, err := os.ReadFile(filepath.Join(target, "private_key.pem"))
		if err != nil {
			return nil, nil
		}
		return widevine.NewDevice(widevine.FromRaw(clientID, privateKey))
	}
	return nil, nil
}