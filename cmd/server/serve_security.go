package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

// defaultServeHost is the interface the HTTP server binds to unless the
// operator asks otherwise.
//
// Loopback on purpose: the API can start Agent turns, and an Agent turn runs
// Bash and Write on the host. Binding 0.0.0.0 turned every `--serve` run into
// an unauthenticated remote command execution endpoint for anyone on the same
// network. An operator who really wants remote access passes --host and,
// because exposure is then deliberate, must also pass a token.
const defaultServeHost = "127.0.0.1"

// errServeExposure is returned when the server is asked to listen on a
// non-loopback interface without a bearer token.
var errServeExposure = errors.New("refusing to serve on a non-loopback interface without --auth-token")

// serveBindAddress resolves the host to bind, defaulting to loopback.
func serveBindAddress(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return defaultServeHost
	}
	return host
}

// isLoopbackHost reports whether binding to host keeps the listener off the
// network. A hostname other than "localhost" cannot be proven local without a
// DNS lookup, so it is treated as reachable.
func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	// Strip an IPv6 zone ("fe80::1%eth0") and any port, neither of which
	// changes whether the address is loopback.
	if index := strings.LastIndex(host, "%"); index >= 0 {
		host = host[:index]
	}
	if host == "::1" || host == "[::1]" {
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// validateServeExposure fails closed: a non-loopback bind without a token is a
// configuration error, not a warning. Serving the Agent API to the network
// without authentication is equivalent to handing out shell access.
func validateServeExposure(host, token string) error {
	if isLoopbackHost(host) || strings.TrimSpace(token) != "" {
		return nil
	}
	return fmt.Errorf("%w: host %q", errServeExposure, host)
}

// serveAuthToken reads the token from the flag, falling back to the
// environment so a service unit can keep it out of the process arguments.
func serveAuthToken(flagValue string) string {
	if token := strings.TrimSpace(flagValue); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("HERON_SERVE_TOKEN"))
}

// requireBearerToken wraps next so every request must carry
// "Authorization: Bearer <token>". When token is empty the handler is returned
// unchanged — callers only reach that state for a loopback bind.
//
// What stays open is deliberately narrow; see openWithoutToken.
func requireBearerToken(token string, next http.Handler) http.Handler {
	if strings.TrimSpace(token) == "" {
		return next
	}
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if openWithoutToken(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		presented := []byte(strings.TrimSpace(r.Header.Get("Authorization")))
		// Constant-time comparison: the token is a bearer credential, and
		// byte-wise early exit leaks its prefix to a timing observer.
		if subtle.ConstantTimeCompare(presented, expected) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="heron"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// openWithoutToken reports whether path is served before authentication.
//
// Two things stay open, and only these two:
//
//   - The viewer shell: index.html, /static/*, and the /s/<id> route that
//     serves index.html so a deep link loads. The shell carries no data and has
//     to load before the browser can be asked for a token — guarding it makes
//     the token field unreachable over HTTP, which is the state this function
//     exists to fix.
//   - /api/health, which answers liveness only (ready flag, pending wakeup
//     count, session count). Launchers and monitors poll it before they hold a
//     credential, and it leaks no conversation content.
//
// The default is guarded: anything under /api/ needs the token unless it is
// named here, so a new route cannot become public by accident.
func openWithoutToken(path string) bool {
	if path == "/api/health" {
		return true
	}
	if path == "/api" || strings.HasPrefix(path, "/api/") {
		return false
	}
	return true
}
