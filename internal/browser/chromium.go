package browser

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// chromiumVendor describes one Chromium-based browser: the directory under
// ~/Library/Application Support that holds its profiles, whether profiles are
// nested under a "User Data" dir (Arc), and the macOS keychain entry holding its
// cookie-encryption password.
type chromiumVendor struct {
	name     string        // display name surfaced to the UI
	appDir   string        // profiles dir under ~/Library/Application Support
	userData bool          // profiles are under <appDir>/User Data (Arc)
	keychain keychainEntry // the "Safe Storage" keychain item
}

type keychainEntry struct {
	service string
	account string
}

var chromiumVendors = []chromiumVendor{
	{"Chrome", "Google/Chrome", false, keychainEntry{"Chrome Safe Storage", "Chrome"}},
	{"Brave", "BraveSoftware/Brave-Browser", false, keychainEntry{"Brave Safe Storage", "Brave"}},
	{"Edge", "Microsoft Edge", false, keychainEntry{"Microsoft Edge Safe Storage", "Microsoft Edge"}},
	{"Arc", "Arc", true, keychainEntry{"Arc Safe Storage", "Arc"}},
}

// scanChromium tries every Chromium-based browser installed on this machine and
// returns the first etp_rt it decrypts, with that browser's name. A browser that
// isn't installed (no profile dir) is silently skipped; a browser whose DB exists
// but can't be read yields a non-sensitive error so the caller can report it.
func scanChromium() (value, name string, err error) {
	var errs []string
	for _, v := range chromiumVendors {
		val, e := v.scan()
		if e == os.ErrNotExist {
			continue // browser not installed — not an error
		}
		if e != nil {
			errs = append(errs, v.name+": "+e.Error())
			continue
		}
		if val != "" {
			return val, v.name, nil
		}
	}
	if len(errs) > 0 {
		return "", "", fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return "", "", nil
}

// scan reads every profile of one Chromium browser and returns its etp_rt (or "").
func (v chromiumVendor) scan() (string, error) {
	cookiesFound := false
	for _, p := range v.profileDirs() {
		cookies := filepath.Join(p, "Cookies")
		if _, err := os.Stat(cookies); err != nil {
			continue // profile has no cookies file (or no such profile)
		}
		cookiesFound = true
		val, err := v.readProfile(cookies)
		if err != nil {
			return "", err // a real read error (permission / decrypt / keychain)
		}
		if val != "" {
			return val, nil
		}
	}
	if !cookiesFound {
		return "", os.ErrNotExist // browser not installed → skip silently
	}
	return "", nil
}

// profileDirs lists a browser's profile dirs, "Default" first then any
// "Profile N" dirs, so the common case is checked before globbing.
func (v chromiumVendor) profileDirs() []string {
	base := filepath.Join(appSupport(), v.appDir)
	if v.userData {
		base = filepath.Join(base, "User Data")
	}
	dirs := []string{filepath.Join(base, "Default")}
	matches, _ := filepath.Glob(filepath.Join(base, "Profile *"))
	dirs = append(dirs, matches...)
	return dirs
}

// readProfile reads one profile's Cookies DB, decrypts v10 entries, and returns
// the etp_rt for crunchyroll.com (or "" if none). It reads a copy so the live
// browser's DB lock is never contended.
func (v chromiumVendor) readProfile(cookies string) (string, error) {
	tmp, err := copyToTemp(cookies)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	defer os.Remove(tmp + "-wal")

	db, err := openDB(tmp)
	if err != nil {
		return "", err
	}
	defer db.Close()

	version := chromiumDBVersion(db)
	password, err := v.keychainPassword()
	if err != nil {
		return "", err
	}

	rows, err := db.Query(`SELECT name, host_key, encrypted_value FROM cookies WHERE host_key LIKE '%crunchyroll.com'`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var name, host string
		var blob []byte
		if err := rows.Scan(&name, &host, &blob); err != nil {
			return "", err
		}
		if name != "etp_rt" || !hasCrunchyrollHost(host) {
			continue
		}
		val, err := decryptChromium(blob, password, version)
		if err != nil {
			return "", err
		}
		if val != "" {
			return val, nil
		}
	}
	return "", rows.Err()
}

// keychainPassword reads the browser's "Safe Storage" password from the macOS
// keychain via the system `security` CLI (no CGO). The item is a generic
// password; reading it usually succeeds without a prompt, but if access is
// denied or the item is missing, decryption can't proceed, so an error is
// returned for the caller to surface (the other browsers / scanners still run).
func (v chromiumVendor) keychainPassword() ([]byte, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-w", "-s", v.keychain.service, "-a", v.keychain.account).Output()
	if err != nil {
		return nil, fmt.Errorf("read keychain %q: %w", v.keychain.service, err)
	}
	// `security -w` prints the password with a trailing newline; trim it.
	return bytes.TrimRight(out, "\n"), nil
}

// decryptChromium decrypts one Chromium cookie blob. macOS Chromium stores use
// the v10 scheme: a 3-byte "v10" prefix, then AES-128-CBC ciphertext keyed by
// PBKDF2(password, "saltysalt", 1003, 16, sha1) with a 16-space IV. Chrome 127+
// (dbVersion >= 24) prepends a 32-byte SHA-256 of the domain to the plaintext;
// that prefix is stripped when present. v11/v20 blobs (Windows App-Bound) are
// not present on macOS and are skipped. A non-v10 or undecryptable blob yields "".
func decryptChromium(blob, password []byte, dbVersion int) (string, error) {
	if len(blob) < 4 || !bytes.HasPrefix(blob, []byte("v10")) {
		return "", nil // not a v10 cookie — skip
	}
	ct := blob[3:]
	if len(ct) == 0 || len(ct)%16 != 0 {
		return "", fmt.Errorf("bad v10 ciphertext length %d", len(ct))
	}
	key := pbkdf2.Key(password, []byte("saltysalt"), 1003, 16, sha1.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, bytes.Repeat([]byte{0x20}, 16)).CryptBlocks(pt, ct)
	pt = pkcs7Unpad(pt)
	// Chrome 127+ prepends a 32-byte SHA-256 domain hash to each encrypted value.
	if dbVersion >= 24 && len(pt) > 32 {
		pt = pt[32:]
	}
	return string(pt), nil
}

// pkcs7Unpad strips standard PKCS#7 padding, returning b unchanged if the
// padding is invalid (so a mis-decrypted blob just yields garbage, not a crash).
func pkcs7Unpad(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	n := int(b[len(b)-1])
	if n < 1 || n > 16 || n > len(b) {
		return b
	}
	return b[:len(b)-n]
}

// chromiumDBVersion reads the cookie DB's meta.version (0 if absent). Chrome 127+
// writes "24" here, which gates the 32-byte prefix strip in decryptChromium.
func chromiumDBVersion(db *sql.DB) int {
	var v string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='version'`).Scan(&v); err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(v))
	return n
}
