// Package crunchy wraps the Crunchyroll HTTP API and the Widevine license
// exchange. Client holds the per-session state that used to live in package
// globals (token, device id, etp-rt, debug) plus the Doer used for every HTTP
// request, so callers (CLI and server) share one authenticated client and
// tests can substitute a fake Doer.
package crunchy

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// UserAgent is the Firefox UA presented to every Crunchyroll endpoint. It was
// duplicated across ~9 call sites before this consolidation.
const UserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:147.0) Gecko/20100101 Firefox/147.0"

// sharedTransport is the custom Transport every SharedClient request flows
// through. The zero-value Transport keeps only 2 idle connections per host
// (DefaultMaxIdleConnsPerHost); the segment downloader runs up to 16 workers
// against a single CDN host, so without raising this knob keep-alive can't
// serve the pool and most segment fetches open a brand-new TLS connection — the
// dominant cause of slow "download segments". 100 idle conns/host lets the pool
// actually reuse connections; HTTP/2 is attempted so a single multiplexed conn
// can carry many concurrent requests.
var sharedTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	MaxIdleConns:          256,
	MaxIdleConnsPerHost:   100,
	IdleConnTimeout:       90 * time.Second,
	ForceAttemptHTTP2:     true,
	TLSHandshakeTimeout:   15 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// SharedClient is the single *http.Client used as the default Doer. Reusing one
// client keeps connection pooling; creating one per call defeated keep-alive.
// It carries sharedTransport so the segment worker pool reuses keep-alive
// connections instead of opening a new TLS handshake per fetch.
var SharedClient = &http.Client{Transport: sharedTransport}

// Doer is the HTTP seam: anything that can execute an *http.Request. The real
// implementation is *http.Client (sharedClient); tests substitute a fake that
// returns canned responses, so the API methods can be exercised without network.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client holds the per-session state that used to live in package globals
// (token, device id, etp-rt, debug) plus the Doer used for every HTTP request.
type Client struct {
	Doer     Doer
	Token    string
	EtpRt    string
	DeviceID string
	Debug    bool
	// WvdDir is an extra directory to search for the Widevine CDM (.wvd or the
	// client_id.bin/private_key.pem pair), ahead of the working directory. The
	// server sets this to its data-dir so the installed `crunchy` command finds
	// the CDM no matter where it is run; the CLI downloader leaves it empty, which
	// falls back to the working directory only (today's behavior).
	WvdDir string
}

// NewClient builds a client with a fresh device id and fetches the initial
// access token using etpRt. Returns an error if the token fetch fails (instead
// of panicking, so callers can report it cleanly).
func NewClient(etpRt string, debug bool) (*Client, error) {
	c := &Client{
		Doer:     SharedClient,
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
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.do(req, false)
}

func (c *Client) do(req *http.Request, refreshed bool) (*http.Response, error) {
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

// CrunchyRequest builds a request to a Crunchyroll endpoint with the User-Agent
// header every call duplicates, and the bearer token when auth is set. Callers
// that need extra headers (Content-Type, X-Cr-*, Origin, Referer, cookies) add
// them on the returned request. This preserves each call's exact wire headers —
// only the UA (+ optional auth) boilerplate is shared.
func (c *Client) CrunchyRequest(method, url string, body io.Reader, auth bool) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return req, nil
}
