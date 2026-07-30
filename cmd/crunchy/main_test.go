package main

import (
	"net"
	"testing"

	"crunchyroll-downloader/internal/server"
)

func TestResolveAddr(t *testing.T) {
	lan := server.LocalIP()
	if lan == "" {
		lan = "127.0.0.1"
	}
	type pair struct{ bind, display string }
	cases := map[string]pair{
		"127.0.0.1:8080": {"127.0.0.1:8080", "127.0.0.1:8080"},
		"localhost:9000": {"127.0.0.1:9000", "127.0.0.1:9000"},
		":7777":          {"127.0.0.1:7777", "127.0.0.1:7777"},
		"0.0.0.0:8080":   {"0.0.0.0:8080", net.JoinHostPort(lan, "8080")}, // LAN-exposed; display the reachable IP
		"10.0.0.1:8080":  {"10.0.0.1:8080", "10.0.0.1:8080"},             // bound as-is
		"garbage":        {"127.0.0.1:8080", "127.0.0.1:8080"},           // fallback
	}
	for in, want := range cases {
		bind, display := resolveAddr(in)
		if bind != want.bind || display != want.display {
			t.Errorf("resolveAddr(%q) = (%q, %q), want (%q, %q)", in, bind, display, want.bind, want.display)
		}
	}
}