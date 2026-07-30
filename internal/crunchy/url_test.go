package crunchy

import (
	"strings"
	"testing"
)

func TestParseContentURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		wantType    string
		wantID      string
		wantErr     bool
		errContains string
	}{
		{
			name:     "series url with slug",
			url:      "https://www.crunchyroll.com/series/G4PH0WXVJ/rule-of-the-nakama",
			wantType: "series",
			wantID:   "G4PH0WXVJ",
		},
		{
			name:     "watch url",
			url:      "https://www.crunchyroll.com/watch/G4PH0WXVJ",
			wantType: "watch",
			wantID:   "G4PH0WXVJ",
		},
		{
			name:        "id too short (<9) is invalid",
			url:         "https://www.crunchyroll.com/series/SHORTID",
			wantErr:     true,
			errContains: "Invalid URL format",
		},
		{
			name:        "id too long (>14) is invalid",
			url:         "https://www.crunchyroll.com/series/THISIDISWAYTOOLONG12345",
			wantErr:     true,
			errContains: "Invalid URL format",
		},
		{
			name:        "wrong content type",
			url:         "https://www.crunchyroll.com/other/G4PH0WXVJ",
			wantErr:     true,
			errContains: "must be /watch/ or /series/",
		},
		{
			name:        "too few path segments",
			url:         "https://www.crunchyroll.com/series",
			wantErr:     true,
			errContains: "Invalid URL format",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ct, id, err := ParseContentURL(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseContentURL(%q) = (%q,%q,nil), want error", tc.url, ct, id)
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("err = %q, want substring %q", err.Error(), tc.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseContentURL(%q) unexpected error: %v", tc.url, err)
			}
			if ct != tc.wantType {
				t.Errorf("contentType = %q, want %q", ct, tc.wantType)
			}
			if id != tc.wantID {
				t.Errorf("contentId = %q, want %q", id, tc.wantID)
			}
		})
	}
}
