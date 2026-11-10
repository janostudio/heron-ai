package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWithMethodGuard covers the gate that keeps a read request from having a
// write effect: every mutating path answers POST only, and a wrong method gets
// 405 with the methods it does answer.
func TestWithMethodGuard(t *testing.T) {
	reached := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	})
	guard := withMethodGuard(next)

	t.Run("wrong method is refused with Allow", func(t *testing.T) {
		reached = 0
		rr := httptest.NewRecorder()
		guard.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cancel?session_id=fs_x", nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
		}
		if allow := rr.Header().Get("Allow"); allow != http.MethodPost {
			t.Fatalf("Allow = %q, want POST", allow)
		}
		if reached != 0 {
			t.Fatalf("handler ran %d times; a refused method must not reach it", reached)
		}
	})

	t.Run("declared method passes through", func(t *testing.T) {
		reached = 0
		rr := httptest.NewRecorder()
		guard.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/cancel?session_id=fs_x", nil))
		if rr.Code != http.StatusOK || reached != 1 {
			t.Fatalf("status = %d reached = %d, want 200/1", rr.Code, reached)
		}
	})

	t.Run("unlisted path passes through", func(t *testing.T) {
		reached = 0
		rr := httptest.NewRecorder()
		guard.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/static/app.js", nil))
		if rr.Code != http.StatusOK || reached != 1 {
			t.Fatalf("status = %d reached = %d, want 200/1", rr.Code, reached)
		}
	})

	t.Run("session record is read only", func(t *testing.T) {
		reached = 0
		rr := httptest.NewRecorder()
		guard.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/sessions/fs_x/record", nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
		}
		if reached != 0 {
			t.Fatalf("handler ran %d times", reached)
		}
	})
}

// TestWithBodyLimit covers the cap that keeps one request from making the engine
// buffer without bound. The up-front Content-Length check answers 413 before a
// handler allocates; the chunked path has no length to check and is enforced on
// read instead.
func TestWithBodyLimit(t *testing.T) {
	const limit = 1024

	t.Run("oversized Content-Length is refused before the handler", func(t *testing.T) {
		called := false
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})
		h := withBodyLimit(limit, next)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(strings.Repeat("x", limit*2)))
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusRequestEntityTooLarge)
		}
		if called {
			t.Fatal("handler ran on a body that was already refused")
		}
	})

	t.Run("read fails past the limit when no length is sent", func(t *testing.T) {
		var readErr error
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, readErr = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		})
		h := withBodyLimit(limit, next)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(strings.Repeat("x", limit*2)))
		req.ContentLength = -1 // chunked: nothing to check up front
		h.ServeHTTP(rr, req)
		if readErr == nil {
			t.Fatal("read succeeded past the limit")
		}
	})

	t.Run("a body inside the limit passes through unchanged", func(t *testing.T) {
		var got string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			got = string(data)
			w.WriteHeader(http.StatusOK)
		})
		h := withBodyLimit(limit, next)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader("small")))
		if rr.Code != http.StatusOK || got != "small" {
			t.Fatalf("status = %d body = %q, want 200/small", rr.Code, got)
		}
	})
}
