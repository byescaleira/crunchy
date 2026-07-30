package download

import "fmt"

// Progress is the seam for the human-facing progress messages a download emits.
// The CLI wires stdoutProgress (fmt to stdout); the server wires a
// channelProgress that publishes the same messages into a job's SSE channel.
// Routing every message through this interface lets the server observe progress
// without scraping stdout, and lets tests run silently.
type Progress interface {
	// Printf replaces the fmt.Printf status messages ("Downloading %s audio...",
	// "Cleaning up...", etc.). The format strings and arguments are exactly those
	// the CLI used to pass to fmt.Printf, so stdoutProgress keeps CLI output
	// byte-identical.
	Printf(format string, args ...any)
	// Segment replaces the "\rDownloaded N of M segments (P%%)" line emitted as
	// each media segment finishes.
	Segment(done, total int)
	// Phase names the download phase that is starting ("subtitles", "audio",
	// "video", "mux"). The server uses it to drive a phase-weighted progress
	// bar; stdoutProgress ignores it so the CLI output stays byte-identical.
	Phase(name string)
	// Output announces the final output file as soon as its name + path are
	// known (before the download begins), so the server can later serve + ship
	// it to a remote client and delete it after delivery. stdoutProgress
	// ignores it so the CLI output stays byte-identical.
	Output(name, path string)
}

// stdoutProgress writes to stdout via fmt, mirroring the pre-refactor CLI output
// byte-for-byte.
type stdoutProgress struct{}

func (stdoutProgress) Printf(format string, args ...any) { fmt.Printf(format, args...) }

func (stdoutProgress) Segment(done, total int) {
	if total == 0 {
		return
	}
	fmt.Printf("\rDownloaded %v of %v segments (%v%%)", done, total, (100*done)/total)
}

// Phase is a no-op for the CLI: phases are a server/UI concern, and emitting
// anything here would change the byte-identical stdout the CLI preserves.
func (stdoutProgress) Phase(name string) {}

// Output is a no-op for the CLI: the CLI writes the file locally and keeps it,
// so it has no use for the serve/ship/delete flow the server wires up.
func (stdoutProgress) Output(name, path string) {}
