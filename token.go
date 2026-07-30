package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type CrunchyrollTokenResponse struct {
	AccessToken string `json:"access_token"`
}

// GetAccessToken fetches an access token from Crunchyroll using the etp-rt
// cookie. It uses c.Doer.Do directly (not c.Do) so a 401 here cannot recurse into
// another token refresh.
func (c *CrunchyClient) GetAccessToken() (string, error) {
	body := url.Values{}
	body.Set("device_id", c.DeviceID)
	body.Set("device_type", "Firefox on Linux")
	body.Set("grant_type", "etp_rt_cookie")

	req, err := http.NewRequest(http.MethodPost, "https://www.crunchyroll.com/auth/v1/token", strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Basic bm9haWhkZXZtXzZpeWcwYThsMHE6")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	req.AddCookie(&http.Cookie{Name: "device_id", Value: c.DeviceID})
	req.AddCookie(&http.Cookie{Name: "etp_rt", Value: c.EtpRt})

	resp, err := c.Doer.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Parse JSON response
	res, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read access token response: %w", err)
	}
	var result CrunchyrollTokenResponse
	if err := json.Unmarshal(res, &result); err != nil {
		return "", fmt.Errorf("failed to get access token: %w", err)
	}

	return result.AccessToken, nil
}
