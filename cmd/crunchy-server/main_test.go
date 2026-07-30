package main

import "testing"

func TestLoopbackAddr(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8080": "127.0.0.1:8080",
		"localhost:9000": "127.0.0.1:9000",
		":7777":          "127.0.0.1:7777",
		"0.0.0.0:8080":   "127.0.0.1:8080", // forced loopback
		"10.0.0.1:8080":  "127.0.0.1:8080", // forced loopback
		"garbage":        "127.0.0.1:8080", // fallback
	}
	for in, want := range cases {
		if got := loopbackAddr(in); got != want {
			t.Errorf("loopbackAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
