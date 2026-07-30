package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// fakeDoer is a test Doer that records every request and answers via respond.
type fakeDoer struct {
	mu      sync.Mutex
	calls   []*http.Request
	respond func(req *http.Request) (*http.Response, error)
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	return f.respond(req)
}

func bodyResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Header:     make(http.Header),
	}
}

func isTokenEndpoint(req *http.Request) bool {
	return strings.Contains(req.URL.Path, "auth/v1/token")
}

func TestGetAccessToken_Success(t *testing.T) {
	doer := &fakeDoer{respond: func(req *http.Request) (*http.Response, error) {
		return bodyResp(200, `{"access_token":"abc123"}`), nil
	}}
	c := &CrunchyClient{Doer: doer, DeviceID: "dev", EtpRt: "etp"}
	tok, err := c.GetAccessToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "abc123" {
		t.Errorf("token = %q, want abc123", tok)
	}
	// the token request must carry the etp_rt cookie and the device id
	req := doer.calls[0]
	if req.Header.Get("Authorization") != "Basic bm9haWhkZXZtXzZpeWcwYThsMHE6" {
		t.Errorf("missing basic auth header")
	}
	found := false
	for _, ck := range req.Cookies() {
		if ck.Name == "etp_rt" && ck.Value == "etp" {
			found = true
		}
	}
	if !found {
		t.Errorf("etp_rt cookie not set on token request")
	}
}

func TestGetAccessToken_UnmarshalError(t *testing.T) {
	doer := &fakeDoer{respond: func(req *http.Request) (*http.Response, error) {
		return bodyResp(200, `not json`), nil
	}}
	c := &CrunchyClient{Doer: doer, DeviceID: "dev", EtpRt: "etp"}
	if _, err := c.GetAccessToken(); err == nil {
		t.Fatal("expected an unmarshal error, got nil")
	}
}

func TestDo_RefreshesOnceOn401(t *testing.T) {
	playbackCalls := 0
	doer := &fakeDoer{respond: func(req *http.Request) (*http.Response, error) {
		if isTokenEndpoint(req) {
			return bodyResp(200, `{"access_token":"fresh"}`), nil
		}
		playbackCalls++
		if playbackCalls == 1 {
			return bodyResp(http.StatusUnauthorized, ""), nil
		}
		return bodyResp(200, "ok"), nil
	}}
	c := &CrunchyClient{Doer: doer, Token: "old"}

	req, _ := http.NewRequest(http.MethodGet, "https://www.crunchyroll.com/playback/v3/G4PH0WXVJ/web/firefox/play", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 after one refresh", resp.StatusCode)
	}
	if c.Token != "fresh" {
		t.Errorf("token = %q, want refreshed to fresh", c.Token)
	}
	if playbackCalls != 2 {
		t.Errorf("playback calls = %d, want 2 (initial + one retry)", playbackCalls)
	}
}

func TestDo_NoInfiniteRefreshLoop(t *testing.T) {
	playbackCalls := 0
	doer := &fakeDoer{respond: func(req *http.Request) (*http.Response, error) {
		if isTokenEndpoint(req) {
			return bodyResp(200, `{"access_token":"fresh"}`), nil
		}
		playbackCalls++
		return bodyResp(http.StatusUnauthorized, ""), nil // always 401
	}}
	c := &CrunchyClient{Doer: doer, Token: "old"}

	req, _ := http.NewRequest(http.MethodGet, "https://www.crunchyroll.com/playback/v3/G4PH0WXVJ/web/firefox/play", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After one refresh the 401 must be returned, not retried forever.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (refreshed once then gave up)", resp.StatusCode)
	}
	if playbackCalls != 2 {
		t.Errorf("playback calls = %d, want 2 (bounded to a single refresh)", playbackCalls)
	}
}
