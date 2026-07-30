package browser

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"database/sql"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

// pkcs7Pad pads b to a multiple of blockSize with standard PKCS#7, matching how
// Chromium pads cookie plaintexts before AES-CBC encryption.
func pkcs7Pad(b []byte, blockSize int) []byte {
	n := blockSize - len(b)%blockSize
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// TestReadFirefoxProfile_FindsEtpRt seeds a minimal moz_cookies table with an
// etp_rt row and a decoy, then asserts readFirefoxProfile returns only the
// crunchyroll etp_rt. Firefox stores cookie values as plaintext, so no
// decryption is involved — this exercises the SQLite-copy + query path.
func TestReadFirefoxProfile_FindsEtpRt(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cookies.sqlite")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	mustExec(t, db, `CREATE TABLE moz_cookies (id INTEGER PRIMARY KEY, host TEXT, name TEXT, value TEXT)`)
	mustExec(t, db, `INSERT INTO moz_cookies (host, name, value) VALUES ('.crunchyroll.com', 'etp_rt', 'SECRET-TOKEN-123')`)
	mustExec(t, db, `INSERT INTO moz_cookies (host, name, value) VALUES ('.crunchyroll.com', 'session', 'not-the-one')`)
	mustExec(t, db, `INSERT INTO moz_cookies (host, name, value) VALUES ('.example.com', 'etp_rt', 'wrong-domain')`)
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	got, err := readFirefoxProfile(dbPath)
	if err != nil {
		t.Fatalf("readFirefoxProfile: %v", err)
	}
	if got != "SECRET-TOKEN-123" {
		t.Fatalf("got %q, want SECRET-TOKEN-123", got)
	}
}

// TestReadFirefoxProfile_NoEtpRt asserts a profile without the cookie returns
// "" with no error (a missing cookie is not an error).
func TestReadFirefoxProfile_NoEtpRt(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cookies.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustExec(t, db, `CREATE TABLE moz_cookies (id INTEGER PRIMARY KEY, host TEXT, name TEXT, value TEXT)`)
	mustExec(t, db, `INSERT INTO moz_cookies (host, name, value) VALUES ('.crunchyroll.com', 'other', 'x')`)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, err := readFirefoxProfile(dbPath)
	if err != nil {
		t.Fatalf("readFirefoxProfile: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestDecryptChromium_RoundTrip encrypts a known plaintext the way Chromium
// does (v10 prefix + AES-128-CBC with the PBKDF2 key + 16-space IV) and asserts
// decryptChromium recovers it. It covers both the pre-127 (no prefix) and 127+
// (32-byte prefix stripped when dbVersion >= 24) cases.
func TestDecryptChromium_RoundTrip(t *testing.T) {
	password := []byte("Chrome")
	key := pbkdf2.Key(password, []byte("saltysalt"), 1003, 16, sha1.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte{0x20}, 16)
	encrypt := func(plain []byte) []byte {
		padded := pkcs7Pad(plain, 16)
		ct := make([]byte, len(padded))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
		return append([]byte("v10"), ct...)
	}

	cases := []struct {
		name    string
		plain   []byte
		version int
	}{
		{"no-prefix", []byte("etp_rt_value"), 0},
		{"with-prefix-v24", append(make([]byte, 32), []byte("etp_rt_value")...), 24},
		{"with-prefix-v25", append(make([]byte, 32), []byte("tok")...), 25},
		{"empty-after-strip", make([]byte, 32), 24},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blob := encrypt(c.plain)
			got, err := decryptChromium(blob, password, c.version)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if c.version >= 24 && len(c.plain) > 32 {
				if got != string(c.plain[32:]) {
					t.Fatalf("got %q, want %q", got, string(c.plain[32:]))
				}
			} else if c.version < 24 {
				if got != string(c.plain) {
					t.Fatalf("got %q, want %q", got, string(c.plain))
				}
			}
		})
	}
}

// TestDecryptChromium_NonV10Skipped asserts a blob without the v10 prefix is
// skipped (returns "") rather than erroring — macOS Chromium stores don't use
// v11/v20, and an unknown scheme must not break the scan.
func TestDecryptChromium_NonV10Skipped(t *testing.T) {
	got, err := decryptChromium(append([]byte("v11"), bytes.Repeat([]byte{0}, 16)...), []byte("Chrome"), 0)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty for non-v10 blob", got)
	}
}

// TestParseSafariCookies_FindsEtpRt builds a minimal Cookies.binarycookies
// blob with one page and one cookie record for crunchyroll.com / etp_rt, then
// asserts parseSafariCookies recovers the value. This pins the fragile binary
// layout (page-offset table, record header offsets, NUL-terminated strings).
func TestParseSafariCookies_FindsEtpRt(t *testing.T) {
	want := "safari-secret-token"
	domain := ".crunchyroll.com\x00"
	name := "etp_rt\x00"
	path := "/\x00"
	value := want + "\x00"

	// Build one cookie record. The fixed header is 44 bytes (0x2C); the string
	// data follows at record-relative offsets.
	headerSize := 0x2C
	domainOff := headerSize
	nameOff := domainOff + len(domain)
	pathOff := nameOff + len(name)
	valueOff := pathOff + len(path)

	rec := make([]byte, headerSize)
	// size (filled after we know total length) at 0x00
	// flags at 0x04, unknown at 0x08
	le32 := func(b []byte, v uint32) { b[0] = byte(v); b[1] = byte(v >> 8); b[2] = byte(v >> 16); b[3] = byte(v >> 24) }
	le32(rec[0x0C:], uint32(domainOff))
	le32(rec[0x10:], uint32(nameOff))
	le32(rec[0x14:], uint32(pathOff))
	le32(rec[0x18:], uint32(valueOff))
	// expiry (0x1C, 8 bytes) + creation (0x24, 8 bytes) left zero.
	rec = append(rec, []byte(domain)...)
	rec = append(rec, []byte(name)...)
	rec = append(rec, []byte(path)...)
	rec = append(rec, []byte(value)...)
	le32(rec[0x00:], uint32(len(rec)))

	// Page: 4-byte signature (0x00000100) + 4-byte numCookies (1) + 4-byte
	// cookie offset (headerSize), then the record. The cookie offset is relative
	// to the page start, so it equals the page-header length (12 bytes).
	pageHeader := 12
	page := make([]byte, pageHeader)
	le32(page[0:], 0x00000100)
	le32(page[4:], 1)
	le32(page[8:], uint32(pageHeader)) // record immediately after the header
	page = append(page, rec...)

	// File: "cook" + numPages (1) + page-offset table (one u32 pointing at 8+4=12),
	// then the page at that offset.
	pageOffset := 8 + 4 // after magic(4) + numPages(4) + one offset(4)
	file := make([]byte, 0, 4+4+4+len(page))
	file = append(file, []byte("cook")...)
	n := make([]byte, 4)
	le32(n, 1)
	file = append(file, n...)
	off := make([]byte, 4)
	le32(off, uint32(pageOffset))
	file = append(file, off...)
	file = append(file, page...)

	got, err := parseSafariCookies(file)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestParseSafariCookies_BadMagic asserts a non-Safari blob returns an error
// rather than panicking.
func TestParseSafariCookies_BadMagic(t *testing.T) {
	if _, err := parseSafariCookies([]byte("nope")); err == nil {
		t.Fatal("expected error for bad magic")
	}
}