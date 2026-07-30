// Package manifest contains pure helpers for parsing and inspecting DASH
// manifests (go-mpd): decoding manifest bytes, locating the PSSH, resolving a
// representation's base URL for a requested quality, finding an adaptation set
// by content type, and expanding a SegmentTimeline into segment numbers.
// Nothing here performs I/O — the HTTP fetch lives in the crunchy package.
package manifest

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/unki2aut/go-mpd"
)

// ParseMPD decodes raw manifest bytes into an *mpd.MPD.
func ParseMPD(body []byte) (*mpd.MPD, error) {
	m := new(mpd.MPD)
	if err := m.Decode(body); err != nil {
		return nil, err
	}
	return m, nil
}

// GetPSSH finds the PSSH in the MPD manifest by scanning every adaptation set
// in the first period, instead of assuming it lives at AdaptationSets[0].
func GetPSSH(m *mpd.MPD) *string {
	if len(m.Period) == 0 {
		return nil
	}
	for _, set := range m.Period[0].AdaptationSets {
		for _, contentProtection := range set.ContentProtections {
			if contentProtection.CencPSSH != nil {
				return contentProtection.CencPSSH
			}
		}
	}

	return nil
}

// GetBaseURL resolves the base URL and representation id for a requested
// quality within an adaptation set. For video it matches the representation
// height; for audio it matches by id substring or by bandwidth bucket. When no
// representation matches it defers to the first one (printing a notice).
func GetBaseURL(set *mpd.AdaptationSet, isVideoSet bool, quality string) (*string, *string) {
	for _, representation := range set.Representations {
		if isVideoSet {
			toInt, _ := strconv.ParseInt(strings.ReplaceAll(quality, "p", ""), 10, 64)
			if *representation.Height == uint64(toInt) {
				return &representation.BaseURL[0].Value, representation.ID
			}
		} else {
			if strings.Contains(*representation.ID, "audio/") {
				if strings.Contains(*representation.ID, quality) {
					return &representation.BaseURL[0].Value, representation.ID
				}
			} else if representation.Bandwidth != nil {
				num := strings.ReplaceAll(quality, "k", "")

				// Crunchyroll MPDs are weird on the "bandwidth" value, it can be 192002 (not just 192000) on certain manifests
				if num == "192" && *representation.Bandwidth >= 192000 {
					return &representation.BaseURL[0].Value, representation.ID
				} else if num == "128" && *representation.Bandwidth >= 128000 {
					return &representation.BaseURL[0].Value, representation.ID
				} else if num == "96" && *representation.Bandwidth >= 96000 {
					return &representation.BaseURL[0].Value, representation.ID
				}
			}
		}
	}
	if len(set.Representations) == 0 {
		return nil, nil
	}
	firstRep := set.Representations[0]
	fmt.Printf("Audio quality %s not found, deferring to %s\n", quality, *firstRep.ID)
	return &firstRep.BaseURL[0].Value, firstRep.ID
}

// FindAdaptationSet returns the first adaptation set of the given type
// ("video" or "audio") in the first period, matched by mimeType/contentType.
// This replaces hard-coded indices (AdaptationSets[0]/[1]) which break on
// manifests whose adaptation sets are ordered differently or come in a
// different count (e.g. movies/specials).
func FindAdaptationSet(m *mpd.MPD, want string) (*mpd.AdaptationSet, error) {
	if len(m.Period) == 0 {
		return nil, fmt.Errorf("manifest has no Period")
	}
	for _, set := range m.Period[0].AdaptationSets {
		if strings.HasPrefix(set.MimeType, want) {
			return set, nil
		}
		if set.ContentType != nil && strings.HasPrefix(*set.ContentType, want) {
			return set, nil
		}
	}
	return nil, fmt.Errorf("no %s adaptation set found in manifest", want)
}

// ExpandTimeline expands a SegmentTimeline's S entries (each with an optional
// repeat count R) into the full ordered list of segment numbers, starting at
// startNumber.
func ExpandTimeline(timeline []*mpd.SegmentTimelineS, startNumber int64) []int64 {
	var result []int64
	segNum := startNumber

	for _, s := range timeline {
		repeat := int64(0)
		if s.R != nil && *s.R > 0 {
			repeat = *s.R
		}

		total := repeat + 1 // DASH rule: total segments = r + 1

		for i := int64(0); i < total; i++ {
			result = append(result, segNum)
			segNum++
		}
	}

	return result
}
