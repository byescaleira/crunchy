package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"
)

// userAgent is the Firefox UA presented to every Crunchyroll endpoint. It was
// duplicated across ~9 call sites before this consolidation.
const userAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:147.0) Gecko/20100101 Firefox/147.0"

// sharedClient is the single *http.Client used as the default Doer. Reusing one
// client keeps connection pooling; creating one per call defeated keep-alive.
var sharedClient = &http.Client{}

// Doer is the HTTP seam: anything that can execute an *http.Request. The real
// implementation is *http.Client (sharedClient); tests substitute a fake that
// returns canned responses, so the API methods can be exercised without network.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// CrunchyClient holds the per-session state that used to live in package globals
// (token, device id, etp-rt, debug) plus the Doer used for every HTTP request.
type CrunchyClient struct {
	Doer     Doer
	Token    string
	EtpRt    string
	DeviceID string
	Debug    bool
}

// NewClient builds a client with a fresh device id and fetches the initial
// access token using etpRt. Returns an error if the token fetch fails (instead
// of panicking, so main can report it cleanly).
func NewClient(etpRt string, debug bool) (*CrunchyClient, error) {
	c := &CrunchyClient{
		Doer:     sharedClient,
		EtpRt:    etpRt,
		DeviceID: uuid.NewString(),
		Debug:    debug,
	}
	tok, err := c.GetAccessToken()
	if err != nil {
		return nil, err
	}
	c.Token = tok
	return c, nil
}

// Do executes req through the Doer, refreshing the access token once on a 401.
// The refreshed flag bounds the recursion to a single refresh so a
// persistently-unauthorized request cannot loop forever.
func (c *CrunchyClient) Do(req *http.Request) (*http.Response, error) {
	return c.do(req, false)
}

func (c *CrunchyClient) do(req *http.Request, refreshed bool) (*http.Response, error) {
	resp, err := c.Doer.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && !refreshed {
		fmt.Println("Access token expired. Refetching one...")
		tok, err := c.GetAccessToken()
		if err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("refresh access token: %w", err)
		}
		c.Token = tok
		req.Header.Set("Authorization", "Bearer "+tok)
		return c.do(req, true)
	}
	return resp, err
}

// crunchyRequest builds a request to a Crunchyroll endpoint with the User-Agent
// header every call duplicates, and the bearer token when auth is set. Callers
// that need extra headers (Content-Type, X-Cr-*, Origin, Referer, cookies) add
// them on the returned request. This preserves each call's exact wire headers —
// only the UA (+ optional auth) boilerplate is shared.
func (c *CrunchyClient) crunchyRequest(method, url string, body io.Reader, auth bool) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return req, nil
}
