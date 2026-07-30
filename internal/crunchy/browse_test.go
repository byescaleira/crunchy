package crunchy

import (
	"encoding/json"
	"testing"
)

// TestDecodeSearch_Tolerant covers the community-documented search response
// shapes: a typed-groups body (pick the "series" group), a flat hit list, empty
// data, and an unrecognizable shape.
func TestDecodeSearch_Tolerant(t *testing.T) {
	seriesItems := []map[string]any{
		{"id": "S1", "title": "Frieren", "slug_title": "frieren"},
	}
	groupsBody, _ := json.Marshal(map[string]any{
		"data": []map[string]any{
			{"type": "movie", "count": 0, "items": []map[string]any{}},
			{"type": "series", "count": 1, "items": seriesItems},
		},
	})
	flatBody, _ := json.Marshal(map[string]any{"data": seriesItems})
	emptyBody := []byte(`{"data":[]}`)
	nullBody := []byte(`{"data":null}`)

	// Typed groups → the series group's items.
	hits, err := decodeSearch(groupsBody)
	if err != nil || len(hits) != 1 || hits[0].Title != "Frieren" {
		t.Errorf("groups shape: hits=%v err=%v", hits, err)
	}

	// Flat hit list.
	hits, err = decodeSearch(flatBody)
	if err != nil || len(hits) != 1 || hits[0].ID != "S1" {
		t.Errorf("flat shape: hits=%v err=%v", hits, err)
	}

	// Empty data → no hits, no error.
	hits, err = decodeSearch(emptyBody)
	if err != nil || len(hits) != 0 {
		t.Errorf("empty data: hits=%v err=%v", hits, err)
	}

	// null data → no hits, no error.
	hits, err = decodeSearch(nullBody)
	if err != nil || len(hits) != 0 {
		t.Errorf("null data: hits=%v err=%v", hits, err)
	}

	// Unrecognizable shape → error (caller shows "no results").
	_, err = decodeSearch([]byte(`{"data":"not-an-array"}`))
	if err == nil {
		t.Error("expected error for unrecognizable shape")
	}
}

// TestDecodeSearch_GroupsNoSeries covers a groups response with no "series"
// bucket: it best-effort returns the first non-empty group's items.
func TestDecodeSearch_GroupsNoSeries(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"data": []map[string]any{
			{"type": "movie", "count": 1, "items": []map[string]any{{"id": "M1", "title": "Film"}}},
		},
	})
	hits, err := decodeSearch(body)
	if err != nil || len(hits) != 1 || hits[0].ID != "M1" {
		t.Errorf("no-series fallback: hits=%v err=%v", hits, err)
	}
}