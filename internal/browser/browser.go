// Package browser reads the etp_rt cookie out of the user's local browser
// cookie stores so the Settings page can save it without a manual copy-paste.
// It supports the Chromium family (Chrome, Brave, Edge, Arc), Firefox, and
// Safari, in that order, and returns the first hit.
//
// It is pure-Go and CGO-free: SQLite is read via modernc.org/sqlite, the macOS
// keychain (for the Chromium "Safe Storage" password) is read by shelling out to
// the system `security` CLI, and Safari's binary format is parsed by hand — so
// the single binary stays pure-Go and cross-compilable.
//
// The returned cookie value is a secret. It must never be logged or put in an
// error message; only the browser name it came from is safe to surface to the UI.
package browser

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// FindEtpRt scans the user's local browser cookie stores for the etp_rt cookie
// on crunchyroll.com, trying the Chromium family, then Firefox, then Safari.
// It returns the first non-empty value found along with the browser name it came
// from ("Chrome", "Firefox", …). A non-sensitive error is returned only if every
// scanner failed to read its store; a store that simply has no etp_rt is not an
// error (value == "" with err == nil). The returned value is the etp_rt secret
// and must never be logged or included in an error.
func FindEtpRt() (value, browser string, err error) {
	scans := []func() (string, string, error){
		scanChromium,
		scanFirefox,
		scanSafari,
	}
	var errs []string
	for _, s := range scans {
		v, name, e := s()
		if e != nil {
			if name == "" {
				name = "browser"
			}
			errs = append(errs, name+": "+e.Error())
			continue
		}
		if v != "" {
			return v, name, nil
		}
	}
	if len(errs) == len(scans) {
		return "", "", fmt.Errorf("could not read any browser's cookies (%s)", strings.Join(errs, "; "))
	}
	return "", "", nil
}

// home returns the user's home directory, or "" if it can't be resolved.
func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// appSupport returns ~/Library/Application Support.
func appSupport() string {
	return filepath.Join(home(), "Library", "Application Support")
}

// copyToTemp copies src (and a sibling -wal sidecar if present) to a temp file
// and returns its path. Browsers lock their live cookie DB and keep recent
// writes in a WAL, so reading a copy of both the DB and its WAL avoids
// "database is locked" errors and surfaces committed-but-uncheckpointed rows
// without ever touching the live store. The caller must remove the returned
// path (and its -wal).
func copyToTemp(src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp("", "crdl-cookie-*.db")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	// Best-effort: copy the WAL sidecar so recently-written cookies are visible.
	// Absence is fine — the main DB may already hold the row.
	if wal, e := os.ReadFile(src + "-wal"); e == nil {
		_ = os.WriteFile(tmp.Name()+"-wal", wal, 0o600)
	}
	return tmp.Name(), nil
}

// openDB opens the SQLite database at path (the temp copy from copyToTemp) and
// pings it. modernc.org/sqlite registers the "sqlite" driver.
func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// hasCrunchyrollHost reports whether a cookie host string matches crunchyroll.com
// (covers both "crunchyroll.com" and ".crunchyroll.com").
func hasCrunchyrollHost(host string) bool {
	return strings.Contains(host, "crunchyroll.com")
}
