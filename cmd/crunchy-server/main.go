// Command crunchy-server runs the Crunchyroll downloader as a localhost control
// panel: a single-binary web UI (HTMX + templ + DaisyUI, assets embedded) that
// browses series, selects episodes, and runs downloads through the same
// internal/download pipeline as the CLI. It binds to 127.0.0.1 only and opens
// the panel in the user's browser on start.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"

	"crunchyroll-downloader/internal/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "Address to listen on (host is forced to 127.0.0.1)")
	etpRt := flag.String("etp-rt", "", "The \"etp_rt\" cookie value of your account (optional; can be set in the UI instead)")
	dataDir := flag.String("data-dir", ".crunchy-data", "Directory for the persisted config (0600)")
	debug := flag.Bool("debug-manifest", false, "Log raw episode playback JSON and manifest XML")
	noBrowser := flag.Bool("no-browser", false, "Do not open the browser on start")
	flag.Parse()

	bind := loopbackAddr(*addr)

	srv, err := server.New(*dataDir, *etpRt, *debug)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	httpServer := &http.Server{Addr: bind, Handler: srv.Handler()}
	log.Printf("Crunchy Downloader control panel: http://%s", bind)
	if !*noBrowser {
		go openBrowser("http://" + bind)
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("Server stopped: %v", err)
	}
}

// loopbackAddr enforces the localhost-only security constraint: it takes the port
// from addr but forces the host to 127.0.0.1, refusing to bind a publicly
// reachable interface even if -addr asked for one. The etp-rt cookie is too
// sensitive to expose on the network.
func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1:8080"
	}
	if host != "127.0.0.1" && host != "localhost" && host != "" {
		log.Printf("warning: refusing to bind %q; forcing 127.0.0.1 (localhost-only)", addr)
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// openBrowser opens url in the user's default browser. Failures are logged but
// non-fatal (the URL is printed above).
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default: // linux, *bsd
		cmd = exec.Command("xdg-open", url)
	}
	if cmd == nil {
		return
	}
	if err := cmd.Run(); err != nil {
		log.Printf("could not open browser: %v (open %s manually)", err, url)
	}
}
