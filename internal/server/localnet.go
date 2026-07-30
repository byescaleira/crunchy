package server

import (
	"net"
	"net/http"
)

// localIPSet returns the set of IP addresses bound to this machine's own
// interfaces — loopback plus every interface's IPv4/IPv6 address. It is
// recomputed on each call (cheap: a single syscall) so a DHCP renewal mid-
// session is picked up instead of trusting a stale snapshot. Used to tell the
// host apart from a remote client: a request from one of these IPs is "the
// server itself" (the Mac reaching its own panel over its LAN IP) and keeps
// downloaded files; anything else is a remote client (a phone on the LAN) whose
// grab ships the file off the host and deletes it.
func localIPSet() map[string]bool {
	set := make(map[string]bool)
	set["127.0.0.1"] = true
	set["::1"] = true
	ifaces, err := net.Interfaces()
	if err != nil {
		return set
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			set[ip.String()] = true
		}
	}
	return set
}

// LocalIP returns the machine's primary non-loopback IPv4 address (the address
// other devices on the LAN use to reach this host), or "" if none is found. It
// is the single source of truth for the LAN-IP URL surfaced to the user by the
// crunchy command so the host and a phone on the network use the same address.
// Best-effort: a host with multiple LAN IPs just returns the first.
func LocalIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return ""
}

// isLocalPeer reports whether the request's peer is the host itself (loopback
// or one of the host's own interface IPs). Used by the file endpoint (delete the
// served file only when a remote client grabs it) and the SSE handler (offer
// the auto-download payload only to remote subscribers). On any parse failure it
// falls back to treating only loopback as local, which keeps the CLI server
// (bound to 127.0.0.1) behaving exactly as before — every peer is local, files
// are never deleted.
func isLocalPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "127.0.0.1" || host == "::1" {
		return true
	}
	return localIPSet()[host]
}
