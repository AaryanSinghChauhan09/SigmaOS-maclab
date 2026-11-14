package server

import (
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
)

// sameOriginOK refuses a request that can change something (any method but
// GET, HEAD and OPTIONS) when a browser sent it from another site's page,
// answering w, and says whether it may go on. Changes need a Maintainer's
// token, which a page can't send, so this is a second guard: once a
// browser carries access by itself, no page open in it may POST to
// /api/devices/{name}/exec. Browsers name the sending page's
// origin in Origin on such requests, and newer ones say in Sec-Fetch-Site how
// it relates to labd; a page can set neither. The Origin must be the very
// origin the request reached labd at (see externalOrigin): scheme, host and
// port, so an http:// page on labd's own host name is another origin than
// labd at https://. The lab CLI, agents, builders and curl send neither
// header and are let through: what guards them is the token or device secret
// they come with.
func (s *Server) sameOriginOK(w http.ResponseWriter, r *http.Request, origin string) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	o := r.Header.Get("Origin")
	if o != "" && !sameOriginAs(o, origin) {
		httpErr(w, 403, "refused: a page from %s asked labd at %s to change something. Use labd's own UI at its address, or the lab CLI", o, origin)
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); o == "" && (site == "cross-site" || site == "same-site") {
		httpErr(w, 403, "refused: a %s page asked labd at %s to change something. Use labd's own UI at its address, or the lab CLI", site, origin)
		return false
	}
	return true
}

// loopbackName reports whether host, a request's Host header, names this
// machine as a caller on it does: localhost or a loopback address.
func loopbackName(host string) bool {
	name := hostName(host)
	if name == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(name)
	return err == nil && a.IsLoopback()
}

// hostName is the name in a Host header: without its port or an IPv6
// address's brackets, in lower case, without a trailing dot.
func hostName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

var reHostName = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_.-]*[a-z0-9_])?$`)
