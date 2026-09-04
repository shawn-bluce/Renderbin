package handlers

import (
	"net/http"
	"strings"
)

// This file holds the two things every handler needs to know about how a
// request reached us, both of which have to work identically for three
// deployments: the binary opened directly at http://localhost:8080, the same
// binary on a LAN at http://192.168.x.x:8080, and a container behind an
// HTTPS-terminating Nginx/Caddy that speaks plain HTTP to the backend.
//
// Only the last case has r.TLS == nil while the browser's scheme is https, and
// the only signal for it is the proxy's X-Forwarded-Proto header. We trust that
// header: nothing here is a security decision that an attacker gains from
// claiming https (a forged "https" can only turn the session cookie's Secure
// flag on, which is strictly more restrictive), and the alternative -- a
// configured base URL -- is one more thing to get wrong in every deployment.

// forwardedProto returns the client-visible scheme claimed by a reverse proxy,
// or "" when the header is absent. A chain of proxies appends to the header
// ("https, http"), and the first entry is the one the client actually used.
func forwardedProto(r *http.Request) string {
	v := r.Header.Get("X-Forwarded-Proto")
	if v == "" {
		return ""
	}
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.ToLower(strings.TrimSpace(v))
}

// requestIsSecure reports whether the browser reached us over HTTPS, directly
// or through a terminating proxy. It decides the session cookie's Secure flag;
// see auth.SetSessionCookie for why that must not be a constant.
func requestIsSecure(r *http.Request) bool {
	if proto := forwardedProto(r); proto != "" {
		return proto == "https"
	}
	return r.TLS != nil
}

// requestBaseURL derives the externally visible origin of this request, so
// URLs we hand out (currently the MCP tools' share links) are correct without a
// configured base URL. Behind a proxy that rewrites the origin, both
// X-Forwarded-Proto and X-Forwarded-Host need to be set for this to be right.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if requestIsSecure(r) {
		scheme = "https"
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = v
	}
	return scheme + "://" + host
}

// publicShareBaseURL uses the configured share origin when one is present and
// otherwise preserves the upstream request-derived behavior.
func publicShareBaseURL(r *http.Request, configured string) string {
	if configured != "" {
		return configured
	}
	return requestBaseURL(r)
}
