package browser

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// scanFirefox reads every Firefox profile's cookies.sqlite and returns the
// etp_rt for crunchyroll.com (or "" if none). Firefox stores cookie values as
// plaintext, so there is no decryption. Expiry is deliberately not filtered:
// etp_rt is long-lived / session, and skipping it avoids the seconds-vs-ms
// ambiguity across moz_cookies schema versions.
func scanFirefox() (value, name string, err error) {
	matches, _ := filepath.Glob(filepath.Join(appSupport(), "Firefox", "Profiles", "*.default-release*", "cookies.sqlite"))
	var errs []string
	for _, dbPath := range matches {
		val, e := readFirefoxProfile(dbPath)
		if e != nil {
			errs = append(errs, e.Error())
			continue
		}
		if val != "" {
			return val, "Firefox", nil
		}
	}
	if len(errs) > 0 {
		return "", "", fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return "", "", nil
}

// readFirefoxProfile reads one profile's cookies.sqlite (a copy, so the live
// Firefox lock isn't contended) and returns the etp_rt for crunchyroll.com.
func readFirefoxProfile(dbPath string) (string, error) {
	tmp, err := copyToTemp(dbPath)
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

	var val string
	err = db.QueryRow(
		`SELECT value FROM moz_cookies WHERE host LIKE '%crunchyroll.com' AND name='etp_rt' LIMIT 1`,
	).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return val, nil
}