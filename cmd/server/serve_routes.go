package main

import (
	"fmt"
	"net/http"
	"strings"
)

// routeMethods declares what each API path answers.
//
// Why a table instead of the mux's own method patterns: the static handler is
// registered on "/" and therefore matches every path, so an unmatched method
// would fall through to it and answer 404 from the embedded FS instead of
// saying the method is wrong. Guarding before the mux keeps 405 honest and
// keeps `Allow` accurate.
//
// A path absent from the table is passed through untouched: static assets, the
// viewer shell, and /api/sessions/{id}/record (which the mux registers with its
// own GET pattern).
var routeMethods = map[string][]string{
	// One path, two meanings: GET reads the session index, POST starts a turn.
	"/api/run":      {http.MethodGet, http.MethodPost},
	"/api/sessions": {http.MethodGet, http.MethodPost},

	// Turn entry points and state changes. These have side effects — a turn runs
	// the Agent, cancel stops work, approvals resume it — so they answer POST
	// only. A GET that reaches them would be a read request with a write effect.
	"/api/sessions/turn": {http.MethodPost},
	"/api/resume":        {http.MethodPost},
	"/api/approvals":     {http.MethodPost},
	"/api/cancel":        {http.MethodPost},
	"/api/recovery":      {http.MethodPost},
	"/api/tasks/cancel":  {http.MethodPost},

	// Read-only views.
	"/api/status":          {http.MethodGet},
	"/api/result":          {http.MethodGet},
	"/api/stream":          {http.MethodGet},
	"/api/tasks":           {http.MethodGet},
	"/api/tasks/stream":    {http.MethodGet},
	"/api/recovery/status": {http.MethodGet},
	"/api/health":          {http.MethodGet},
}

// allowedMethods reports the methods a path declares, and whether the path is
// declared at all.
func allowedMethods(path string) ([]string, bool) {
	if methods, ok := routeMethods[path]; ok {
		return methods, true
	}
	// The session record is the one path with a variable segment; it serves
	// history and must not be usable to trigger anything.
	if strings.HasPrefix(path, "/api/sessions/") && strings.HasSuffix(path, "/record") {
		return []string{http.MethodGet}, true
	}
	return nil, false
}

// withMethodGuard rejects a request whose method the path does not declare.
//
// Answering such a request by running the handler is how a "read" turns into a
// write: GET /api/cancel with a session id is a perfectly valid request to the
// mux, and it cancels. The guard is the only place that knows the difference
// between "this path exists" and "this path does this for this method".
func withMethodGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, ok := allowedMethods(r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		for _, method := range allowed {
			if r.Method == method {
				next.ServeHTTP(w, r)
				return
			}
		}
		// HEAD is implicit for a GET route: clients probe with it and expect the
		// headers, not a 405.
		if r.Method == http.MethodHead {
			for _, method := range allowed {
				if method == http.MethodGet {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		http.Error(w, "method "+r.Method+" is not allowed for "+r.URL.Path, http.StatusMethodNotAllowed)
	})
}

// maxRequestBodyBytes caps how much of one request body the server will buffer.
//
// Attachments are inlined base64 in the JSON body, so the cap has to fit a few
// files (base64 costs about a third more than the raw bytes) while staying far
// below the point where one client can make the engine allocate without bound.
// 32 MiB holds roughly 24 MiB of decoded attachments.
const maxRequestBodyBytes = 32 << 20

// withBodyLimit refuses an oversized body before a handler buffers it.
//
// The up-front check uses Content-Length, which a chunked request does not send.
// MaxBytesReader is installed for that case: it fails the read instead, and the
// handler turns *http.MaxBytesError into 413 (see view.writeDecodeError). A
// decode error alone would be reported as 400, which tells the client its JSON
// was malformed when the real answer is "send less".
func withBodyLimit(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > limit {
			http.Error(w,
				fmt.Sprintf("request body is %d bytes; the limit is %d", r.ContentLength, limit),
				http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
