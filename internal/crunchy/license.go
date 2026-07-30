package crunchy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/iyear/gowidevine"
	"github.com/iyear/gowidevine/widevinepb"

	"crunchyroll-downloader/internal/drm"
)

// CrunchyrollWidevineLicenseResponse is the body of the widevine license endpoint.
type CrunchyrollWidevineLicenseResponse struct {
	License string `json:"license"`
}

// SendChallenge POSTs a Widevine challenge to the Crunchyroll license endpoint
// and returns the decoded license bytes.
func (c *Client) SendChallenge(contentId, videoToken string, challenge []byte) ([]byte, error) {
	req, err := c.CrunchyRequest(http.MethodPost, "https://www.crunchyroll.com/license/v1/license/widevine", bytes.NewReader(challenge), true)
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

// GetLicense performs the Widevine license exchange for a content id + playback
// token and returns the resolved key set. The keys are returned (rather than
// stashed in a global) so each audio version's keys stay local to the loop that
// downloads that version — preserving the invariant that audio for version i is
// decrypted with version i's keys before version i+1's license is fetched.
func (c *Client) GetLicense(psshData, contentId, videoToken string) ([]*widevine.Key, error) {
	device, err := drm.LoadWidevineDevice(c.WvdDir)
	if device == nil {
		return nil, errors.New("no widevine device provided. You either need:\n- a \".wvd\" file,\n- or \"client_id.bin\" and \"private_key.pem\" files.\nI'm not sharing links for obvious reasons, but search \"ready to use cdms\" on Google :)\n")
	} else if err != nil {
		return nil, err
	}
	cdm := widevine.NewCDM(device)
	decodedPssh, err := base64.StdEncoding.DecodeString(psshData)
	if err != nil {
		return nil, err
	}
	pssh, err := widevine.NewPSSH(decodedPssh)
	if err != nil {
		return nil, err
	}

	challenge, parseLicense, err := cdm.GetLicenseChallenge(pssh, widevinepb.LicenseType_AUTOMATIC, false)
	if err != nil {
		return nil, err
	}
	resp, err := c.SendChallenge(contentId, videoToken, challenge)
	if err != nil {
		return nil, err
	}
	keys, err := parseLicense(resp)
	if err != nil {
		return nil, err
	}

	return keys, nil
}
