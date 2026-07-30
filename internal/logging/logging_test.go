package logging

import (
	"strings"
	"testing"
	"time"
)

func TestLogFormat(t *testing.T) {
	var b strings.Builder
	l := New(&b)
	l.Info("jobs", "started", F("job", "7f3a4b21"), F("title", "Solo Leveling"), F("ep", "S01E03"))

	line := b.String()
	if !strings.HasSuffix(line, "\n") {
		t.Errorf("line not newline-terminated: %q", line)
	}
	line = strings.TrimRight(line, "\n")

	// Every line leads with a parseable timestamp + level + [component] + msg.
	for _, want := range []string{" INFO  [jobs] started", "job=7f3a4b21", "title=", "ep=S01E03"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q missing %q", line, want)
		}
	}
	// A title with spaces must be quoted so the line splits on spaces.
	if !strings.Contains(line, `title="Solo Leveling"`) {
		t.Errorf("title not space-quoted in %q", line)
	}
	// Timestamp must carry a date (YYYY-MM-DD), not just a time.
	ts := line[:10]
	if _, err := time.Parse("2006-01-02", ts); err != nil {
		t.Errorf("leading %q is not a YYYY-MM-DD date in %q", ts, line)
	}
}

func TestQuoting(t *testing.T) {
	cases := []struct {
		in   string
		want string // bareword or strconv.Quote'd
	}{
		{"plain", "plain"},
		{"", "\"\""},
		{"two words", `"two words"`},
		{`a=b`, `"a=b"`},
		{`he said "hi"`, `"he said \"hi\""`},
		{"tab\there", `"tab\there"`},
	}
	for _, c := range cases {
		got := maybeQuote(c.in)
		if got != c.want {
			t.Errorf("maybeQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNilLoggerAndNilOutAreNoOps(t *testing.T) {
	var l *Logger // nil pointer
	l.Info("x", "y", F("k", "v")) // must not panic
	l = New(nil)
	l.Info("x", "y", F("k", "v")) // nil out → drop, no panic
}

func TestLevelsAlign(t *testing.T) {
	var b strings.Builder
	New(&b).Log(Info, "c", "m")
	New(&b).Log(Warn, "c", "m")
	New(&b).Log(Error, "c", "m")
	for _, lvl := range []string{"INFO ", "WARN ", "ERROR"} {
		if !strings.Contains(b.String(), lvl) {
			t.Errorf("level %q not aligned in output", lvl)
		}
	}
}