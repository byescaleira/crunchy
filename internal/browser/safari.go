package browser

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// scanSafari reads Safari's Cookies.binarycookies and returns the etp_rt for
// crunchyroll.com. Safari's store lives in its App Container and requires Full
// Disk Access for the process to read; a permission failure (or a missing file
// when Safari isn't used) is returned as a non-sensitive error naming Safari so
// the overall scan degrades gracefully — the Chromium and Firefox scanners
// already ran. Parsing is best-effort; a malformed file yields "" rather than a
// crash. The binary format is reverse-engineered and the most fragile scanner.
func scanSafari() (value, name string, err error) {
	path := filepath.Join(home(), "Library", "Containers", "com.apple.Safari",
		"Data", "Library", "Cookies", "Cookies.binarycookies")
	data, err := os.ReadFile(path)
	if err != nil {
		// Permission denied (no Full Disk Access) or Safari not installed.
		return "", "Safari", err
	}
	val, err := parseSafariCookies(data)
	if err != nil {
		return "", "Safari", err
	}
	return val, "Safari", nil
}

// parseSafariCookies parses the Cookies.binarycookies binary format and returns
// the etp_rt value for a crunchyroll.com host, or "" if none. Layout: a "cook"
// header with a page-offset table; each page has a header, a per-page cookie
// offset table, and cookie records whose domain/name/path/value are
// null-terminated strings at record-relative offsets. Expiry/creation are 8-byte
// big-endian doubles that we don't need, so they're skipped.
func parseSafariCookies(data []byte) (string, error) {
	if len(data) < 8 || string(data[:4]) != "cook" {
		return "", fmt.Errorf("bad safari cookies magic")
	}
	numPages := int(binary.LittleEndian.Uint32(data[4:8]))
	if 8+numPages*4 > len(data) {
		return "", fmt.Errorf("bad safari cookies header")
	}
	for i := 0; i < numPages; i++ {
		pageOff := int(binary.LittleEndian.Uint32(data[8+i*4 : 12+i*4]))
		if pageOff+8 > len(data) {
			continue
		}
		numCookies := int(binary.LittleEndian.Uint32(data[pageOff+4 : pageOff+8]))
		cookieOffsStart := pageOff + 8
		for c := 0; c < numCookies; c++ {
			o := cookieOffsStart + c*4
			if o+4 > len(data) {
				continue
			}
			recStart := pageOff + int(binary.LittleEndian.Uint32(data[o:o+4]))
			if recStart+0x1C > len(data) {
				continue
			}
			size := int(binary.LittleEndian.Uint32(data[recStart : recStart+4]))
			domainOff := int(binary.LittleEndian.Uint32(data[recStart+0x0C : recStart+0x10]))
			nameOff := int(binary.LittleEndian.Uint32(data[recStart+0x10 : recStart+0x14]))
			valueOff := int(binary.LittleEndian.Uint32(data[recStart+0x18 : recStart+0x1C]))
			recEnd := recStart + size
			if size <= 0 || recEnd > len(data) {
				recEnd = len(data)
			}
			rec := data[recStart:recEnd]
			domain := readCString(rec, domainOff)
			cname := readCString(rec, nameOff)
			cval := readCString(rec, valueOff)
			if cname == "etp_rt" && hasCrunchyrollHost(domain) {
				return strings.TrimRight(cval, "\x00"), nil
			}
		}
	}
	return "", nil
}

// readCString reads a NUL-terminated string from rec starting at off. A bad
// offset yields "" rather than panicking.
func readCString(rec []byte, off int) string {
	if off < 0 || off >= len(rec) {
		return ""
	}
	end := off
	for end < len(rec) && rec[end] != 0 {
		end++
	}
	return string(rec[off:end])
}