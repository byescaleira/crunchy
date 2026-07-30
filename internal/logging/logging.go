// Package logging is a tiny structured logger for the crunchy-server control
// panel. One line per event, human-readable and greppable, with a timestamp, a
// level, a bracketed component tag, a message, and key=value fields:
//
//	2026-07-30 14:23:01.123 -0300 INFO  [jobs] started  job=7f3a4b21 title="Solo Leveling" ep=S01E03
//
// It is logfmt-ish on purpose (not JSON): the panel runs in a terminal the user
// tails, so readability beats machine-parseability. Field values that contain a
// space, "=" or quote are strconv.Quote'd, so a line always splits cleanly on
// spaces and "=". The zero-value Logger writes nowhere; New points it at an
// io.Writer (os.Stderr for the server).
package logging

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Level is the severity of a log line.
type Level string

const (
	Info  Level = "INFO"
	Warn  Level = "WARN"
	Error Level = "ERROR"
)

// Field is one key=value pair appended after the message.
type Field struct {
	Key string
	Val any
}

// F is the field constructor, so callers read log.Info("jobs", "started",
// logging.F("job", id), logging.F("title", title)).
func F(key string, val any) Field { return Field{Key: key, Val: val} }

// Logger writes structured lines to a single io.Writer. It is safe for
// concurrent use (one Mutex around the Write, so lines never interleave).
type Logger struct {
	mu  sync.Mutex
	out io.Writer
}

// New returns a Logger that writes to out. A nil out means lines are dropped
// (the zero-value Logger also drops), which keeps callers unguarded about
// whether logging is configured.
func New(out io.Writer) *Logger { return &Logger{out: out} }

// Log writes one structured line. Empty msg is allowed (the fields still
// carry the detail). A nil-out Logger is a no-op so this is safe to call from
// any path.
func (l *Logger) Log(level Level, component, msg string, fields ...Field) {
	if l == nil || l.out == nil {
		return
	}
	var b strings.Builder
	b.WriteString(time.Now().Format("2006-01-02 15:04:05.000 -0700"))
	b.WriteByte(' ')
	b.WriteString(padRight(string(level), 5))
	b.WriteByte(' ')
	b.WriteByte('[')
	b.WriteString(component)
	b.WriteByte(']')
	b.WriteByte(' ')
	b.WriteString(msg)
	for _, f := range fields {
		b.WriteByte(' ')
		b.WriteString(f.Key)
		b.WriteByte('=')
		b.WriteString(formatVal(f.Val))
	}
	b.WriteByte('\n')
	l.mu.Lock()
	_, _ = l.out.Write([]byte(b.String()))
	l.mu.Unlock()
}

// Info/Warn/Error are the level shortcuts.
func (l *Logger) Info(component, msg string, fields ...Field)  { l.Log(Info, component, msg, fields...) }
func (l *Logger) Warn(component, msg string, fields ...Field)  { l.Log(Warn, component, msg, fields...) }
func (l *Logger) Error(component, msg string, fields ...Field) { l.Log(Error, component, msg, fields...) }

// padRight pads s with trailing spaces to width (left-aligned, so levels line
// up: "INFO " / "WARN " / "ERROR").
func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// formatVal renders a field value: strings/fmt.Stringers are maybeQuote'd;
// everything else goes through fmt and is then treated as a string. Numbers,
// durations and bools usually need no quoting.
func formatVal(v any) string {
	switch x := v.(type) {
	case string:
		return maybeQuote(x)
	case fmt.Stringer:
		return maybeQuote(x.String())
	default:
		return maybeQuote(fmt.Sprintf("%v", v))
	}
}

// maybeQuote returns s as-is when it is a bareword (no space, "=", or quote,
// and non-empty); otherwise strconv.Quote'd so the line stays parseable.
func maybeQuote(s string) string {
	if s == "" {
		return "\"\""
	}
	if strings.ContainsAny(s, " =\"\n\t") {
		return strconv.Quote(s)
	}
	return s
}