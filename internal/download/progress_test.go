package download

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// captureStdout runs fn with os.Stdout replaced by a pipe and returns whatever
// was written. The download package's tests don't run in parallel, so swapping
// the process-global os.Stdout is safe here.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// TestStdoutProgress_Printf pins the contract that stdoutProgress passes the
// format string and arguments straight through to fmt.Printf unchanged, so the
// CLI's status messages stay byte-identical to the pre-refactor output.
func TestStdoutProgress_Printf(t *testing.T) {
	var p stdoutProgress
	got := captureStdout(t, func() {
		p.Printf("Downloading %s audio...\n", "ja-JP")
		p.Printf("Cleaning up...")
	})
	want := "Downloading ja-JP audio...\nCleaning up..."
	if got != want {
		t.Errorf("Printf output mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

// TestStdoutProgress_Segment pins the exact segment-progress line, including the
// leading carriage return and the trailing percent sign, that the CLI prints as
// each media segment finishes.
func TestStdoutProgress_Segment(t *testing.T) {
	var p stdoutProgress
	got := captureStdout(t, func() { p.Segment(3, 5) })
	want := "\rDownloaded 3 of 5 segments (60%)"
	if got != want {
		t.Errorf("Segment output mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

// TestStdoutProgress_SegmentZeroTotal guards against a divide-by-zero if a track
// ever has no segments: nothing should be printed.
func TestStdoutProgress_SegmentZeroTotal(t *testing.T) {
	var p stdoutProgress
	got := captureStdout(t, func() { p.Segment(0, 0) })
	if got != "" {
		t.Errorf("Segment(0,0) printed %q, want empty", got)
	}
}