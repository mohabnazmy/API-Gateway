package middleware

import (
	"net"
	"net/http"
	"strings"
)

// RealIP resolves the client IP used for rate limiting and logging. It only
// trusts X-Forwarded-For when the immediate peer (RemoteAddr) is in the
// configured set of trusted proxy networks; otherwise it uses RemoteAddr and
// ignores XFF. With no trusted proxies (the secure default), XFF is never
// trusted, so a client cannot spoof its identity to evade per-IP limits.
type RealIP struct {
	trusted []*net.IPNet
}

// NewRealIP builds a resolver trusting the given proxy networks. Pass nil/empty
// to trust no proxies (XFF ignored).
func NewRealIP(trusted []*net.IPNet) *RealIP {
	return &RealIP{trusted: trusted}
}

// From returns the effective client IP for the request.
func (ri *RealIP) From(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ri.isTrusted(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Left-most entry is the originating client (set by the trusted edge).
			first, _, _ := strings.Cut(xff, ",")
			if first = strings.TrimSpace(first); first != "" {
				return first
			}
		}
	}
	return host
}

func (ri *RealIP) isTrusted(host string) bool {
	if len(ri.trusted) == 0 {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range ri.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
