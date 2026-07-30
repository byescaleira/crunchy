// Package drm wraps the Widevine CDM helpers that don't need the Crunchyroll
// HTTP/token layer: selecting the CONTENT key from a license key set, and
// loading a Widevine device (".wvd" or the client_id.bin/private_key.pem pair)
// from the working directory. The license exchange itself lives in the crunchy
// package because it needs the bearer token and the playback token.
package drm

import (
	"errors"
	"os"
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

// LoadWidevineDevice loads a Widevine CDM from the working directory. It prefers
// a single ".wvd" file; otherwise it looks for the "client_id.bin" +
// "private_key.pem" pair, opening both by name so the result no longer depends
// on directory sort order (the previous single-pass loop would break early
// after private_key.pem and miss a client_id.bin that sorted later, leaving the
// client ID empty and silently returning "no device").
//
// It returns (nil, nil) when no device files are present so the caller can
// surface a friendly "no widevine device provided" message.
func LoadWidevineDevice() (*widevine.Device, error) {
	files, err := os.ReadDir(".")
	if err != nil {
		return nil, nil
	}
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".wvd") {
			f, err := os.Open(file.Name())
			if err != nil {
				return nil, err
			}
			defer f.Close()
			return widevine.NewDevice(widevine.FromWVD(f))
		}
	}

	// No .wvd: load the raw client_id.bin + private_key.pem pair by name.
	clientID, err := os.ReadFile("client_id.bin")
	if err != nil {
		return nil, nil
	}
	privateKey, err := os.ReadFile("private_key.pem")
	if err != nil {
		return nil, nil
	}
	return widevine.NewDevice(widevine.FromRaw(clientID, privateKey))
}
