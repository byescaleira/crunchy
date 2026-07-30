package main

import (
	"reflect"
	"testing"
)

func TestParseLangs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"ja-JP", []string{"ja-JP"}},
		{"ja-JP,en-US", []string{"ja-JP", "en-US"}},
		{"ja-JP, en-US", []string{"ja-JP", "en-US"}}, // trims spaces
		{"  ja-JP  ,  en-US  ", []string{"ja-JP", "en-US"}},
		{",", nil},  // only empties
		{"  ", nil}, // whitespace only
		{"ja-JP,", []string{"ja-JP"}},
		{"all", []string{"all"}}, // "all" is only special-cased later in Episode
		{",,ja-JP,,en-US,,", []string{"ja-JP", "en-US"}},
	}
	for _, tc := range tests {
		got := parseLangs(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseLangs(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
