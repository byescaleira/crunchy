package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/iyear/gowidevine"
	"github.com/iyear/gowidevine/widevinepb"
	"github.com/unki2aut/go-mpd"
)

var keys []*widevine.Key

// contentKey returns the Widevine CONTENT key from a license key set, matching
// the selection that widevine.DecryptMP4Auto performs internally. We decrypt
// segment-by-segment ourselves (instead of calling DecryptMP4Auto on the whole
// file) so the key has to be resolved here.
func contentKey(ks []*widevine.Key) ([]byte, error) {
	for _, k := range ks {
		if k.Type == widevinepb.License_KeyContainer_CONTENT {
			return k.Key, nil
		}
	}
	return nil, errors.New("no CONTENT key type found in the provided key set")
}

// getPssh finds the PSSH in the MPD manifest by scanning every adaptation set
// in the first period, instead of assuming it lives at AdaptationSets[0].
func getPssh(mpd *mpd.MPD) *string {
	if len(mpd.Period) == 0 {
		return nil
	}
	for _, set := range mpd.Period[0].AdaptationSets {
		for _, contentProtection := range set.ContentProtections {
			if contentProtection.CencPSSH != nil {
				return contentProtection.CencPSSH
			}
		}
	}

	return nil
}

type CrunchyrollWidevineLicenseResponse struct {
	License string `json:"license"`
}

func (c *CrunchyClient) sendChallenge(contentId, videoToken string, challenge []byte) ([]byte, error) {
	req, err := c.crunchyRequest(http.MethodPost, "https://www.crunchyroll.com/license/v1/license/widevine", bytes.NewReader(challenge), true)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Cr-Content-Id", contentId)
	req.Header.Set("X-Cr-Video-Token", videoToken)
	req.Header.Set("Origin", "https://static.crunchyroll.com")
	req.Header.Set("Referer", "https://static.crunchyroll.com/")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Parse JSON response
	res, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var result CrunchyrollWidevineLicenseResponse
	if err = json.Unmarshal(res, &result); err != nil {
		return nil, err
	}

	decoded, err := base64.StdEncoding.DecodeString(result.License)
	if err != nil {
		return nil, err
	}

	return decoded, nil
}

// getWidevineDevice loads a Widevine CDM from the working directory. It prefers
// a single ".wvd" file; otherwise it looks for the "client_id.bin" +
// "private_key.pem" pair, opening both by name so the result no longer depends
// on directory sort order (the previous single-pass loop would break early
// after private_key.pem and miss a client_id.bin that sorted later, leaving the
// client ID empty and silently returning "no device").
func getWidevineDevice() (*widevine.Device, error) {
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

func (c *CrunchyClient) getLicense(psshData, contentId, videoToken string) error {
	device, err := getWidevineDevice()
	if device == nil {
		return errors.New("no widevine device provided. You either need:\n- a \".wvd\" file,\n- or \"client_id.bin\" and \"private_key.pem\" files.\nI'm not sharing links for obvious reasons, but search \"ready to use cdms\" on Google :)\n")
	} else if err != nil {
		return err
	}
	cdm := widevine.NewCDM(device)
	decodedPssh, err := base64.StdEncoding.DecodeString(psshData)
	if err != nil {
		return err
	}
	pssh, err := widevine.NewPSSH(decodedPssh)
	if err != nil {
		return err
	}

	challenge, parseLicense, err := cdm.GetLicenseChallenge(pssh, widevinepb.LicenseType_AUTOMATIC, false)
	if err != nil {
		return err
	}
	resp, err := c.sendChallenge(contentId, videoToken, challenge)
	if err != nil {
		return err
	}
	keys, err = parseLicense(resp)
	if err != nil {
		return err
	}

	return nil
}
