package main

import (
	"fmt"
	"net/http"
)

// sharedClient is the single *http.Client used for all Crunchyroll requests.
// Reusing one client keeps connection pooling; creating one per call (as the
// old DoRequest did) defeated keep-alive.
var sharedClient = &http.Client{}

// DoRequest executes req with the shared HTTP client. On a 401 it refreshes the
// access token once and retries. The refreshed flag bounds the recursion to a
// single refresh so a persistently-unauthorized request cannot loop forever
// (the previous unbounded recursion would call DoRequest again on every 401).
func DoRequest(req *http.Request) (*http.Response, error) {
	return doRequest(req, false)
}

func doRequest(req *http.Request, refreshed bool) (*http.Response, error) {
	resp, err := sharedClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && !refreshed {
		fmt.Println("Access token expired. Refetching one...")
		token = GetAccessToken(*etpRt)
		req.Header.Set("Authorization", "Bearer "+token)
		return doRequest(req, true)
	}
	return resp, err
}
