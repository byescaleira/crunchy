// Command crunchy is the Crunchyroll downloader: a single-binary web control panel
// (HTMX + templ + DaisyUI, assets embedded) that browses series, selects episodes,
// and runs downloads through the same internal/download pipeline as the CLI. Run
// `crunchy` and the panel opens in your browser. By default it binds all
// interfaces and surfaces the machine's LAN IP so a phone on the same network can
// drive downloads — pass -addr 127.0.0.1:<port> to restrict it to localhost. The
// panel never echoes the etp_rt cookie, but a LAN peer can trigger downloads and
// reconfigure with their own token, so prefer the loopback form on untrusted
// networks.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"crunchyroll-downloader/internal/server"
)

func main() {
	addr := flag.String("addr", "0.0.0.0:8080", "Address to listen on (default 0.0.0.0 = all interfaces / LAN; 127.0.0.1 = localhost only)")
	etpRt := flag.String("etp-rt", "", "The \"etp_rt\" cookie value of your account (optional; can be set in the UI instead)")
	dataDir := flag.String("data-dir", "", "Directory for the persisted config (0600); defaults to $HOME/.crunchy-data so crunchy works run from anywhere")
	debug := flag.Bool("debug-manifest", false, "Log raw episode playback JSON and manifest XML")
	noBrowser := flag.Bool("no-browser", false, "Do not open the browser on start")
	flag.Parse()

	bind, display := resolveAddr(*addr)

	// Default the data-dir to a stable home-based path so the installed `crunchy`
	// command finds its config + Widevine device regardless of the working dir it
	// is launched from. An explicit -data-dir (relative or absolute) is honored as-is.
	dir := *dataDir
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".crunchy-data")
		} else {
			dir = ".crunchy-data"
		}
	}

	srv, err := server.New(dir, *etpRt, *debug)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	httpServer := &http.Server{Addr: bind, Handler: srv.Handler()}
	log.Printf("Crunchy Downloader control panel: http://%s", display)
	if !*noBrowser {
		go openBrowser("http://" + display)
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("Server stopped: %v", err)
	}
}

// resolveAddr turns the -addr flag into (bind, display): bind is what
// http.Server listens on; display is the URL a user/phone should open. The
// default and any explicit loopback host stay localhost-only (the safe choice).
// A host of 0.0.0.0 binds every interface and displays the machine's LAN IP so
// the printed URL is actually reachable from a phone; any other non-loopback
// host is bound as-is and surfaced verbatim. Network exposure is always logged.
func resolveAddr(addr string) (bind, display string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1:8080", "127.0.0.1:8080"
	}
	switch host {
	case "", "127.0.0.1", "localhost":
		return net.JoinHostPort("127.0.0.1", port), net.JoinHostPort("127.0.0.1", port)
	case "0.0.0.0", "[::]", "::":
		lan := server.LocalIP()
		if lan == "" {
			lan = "127.0.0.1"
		}
		log.Printf("warning: binding all interfaces (:%s) — panel is reachable from the LAN; a peer can drive downloads but cannot read your etp_rt cookie", port)
		return net.JoinHostPort("0.0.0.0", port), net.JoinHostPort(lan, port)
	default:
		log.Printf("warning: binding %s — panel is reachable from the network; a peer can drive downloads but cannot read your etp_rt cookie", addr)
		return addr, addr
	}
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
