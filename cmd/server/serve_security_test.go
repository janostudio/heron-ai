package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServeBindAddressDefaultsToLoopback(t *testing.T) {
	if got := serveBindAddress(""); got != defaultServeHost {
		t.Fatalf("serveBindAddress(\"\") = %q, want %q", got, defaultServeHost)
	}
	if defaultServeHost != "127.0.0.1" {
		t.Fatalf("defaultServeHost = %q, want loopback", defaultServeHost)
	}
	if got := serveBindAddress("0.0.0.0"); got != "0.0.0.0" {
		t.Fatalf("explicit host must be preserved, got %q", got)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	loopback := []string{"", "127.0.0.1", "127.0.0.53", "::1", "[::1]", "localhost", "LocalHost"}
	for _, host := range loopback {
		if !isLoopbackHost(host) {
			t.Fatalf("isLoopbackHost(%q) = false, want true", host)
		}
	}
	exposed := []string{"0.0.0.0", "::", "[::]", "192.168.1.10", "10.0.0.5", "example.com", "fe80::1%eth0"}
	for _, host := range exposed {
		if isLoopbackHost(host) {
			t.Fatalf("isLoopbackHost(%q) = true, want false", host)
		}
	}
}

func TestValidateServeExposureFailsClosed(t *testing.T) {
	if err := validateServeExposure("127.0.0.1", ""); err != nil {
		t.Fatalf("loopback without token must be allowed, got %v", err)
	}
	if err := validateServeExposure("0.0.0.0", "secret"); err != nil {
		t.Fatalf("non-loopback with token must be allowed, got %v", err)
	}
	err := validateServeExposure("0.0.0.0", "")
	if !errors.Is(err, errServeExposure) {
		t.Fatalf("non-loopback without token = %v, want errServeExposure", err)
	}
}

func TestRequireBearerTokenRejectsMissingAndWrong(t *testing.T) {
	handler := requireBearerToken("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{name: "missing", header: "", want: http.StatusUnauthorized},
		{name: "wrong token", header: "Bearer other", want: http.StatusUnauthorized},
		{name: "prefix only", header: "Bearer secre", want: http.StatusUnauthorized},
		{name: "raw token", header: "secret", want: http.StatusUnauthorized},
		{name: "correct", header: "Bearer secret", want: http.StatusOK},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/sessions/turn", nil)
			if testCase.header != "" {
				request.Header.Set("Authorization", testCase.header)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
			if testCase.want == http.StatusUnauthorized && recorder.Header().Get("WWW-Authenticate") == "" {
				t.Fatalf("401 must advertise the expected scheme")
			}
		})
	}
}

func TestRequireBearerTokenDisabledWithoutToken(t *testing.T) {
	handler := requireBearerToken("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("loopback mode must not require a token, got %d", recorder.Code)
	}
}

func TestServeAuthTokenPrefersFlagThenEnv(t *testing.T) {
	t.Setenv("HERON_SERVE_TOKEN", "from-env")
	if got := serveAuthToken("from-flag"); got != "from-flag" {
		t.Fatalf("flag should win, got %q", got)
	}
	if got := serveAuthToken(""); got != "from-env" {
		t.Fatalf("env fallback = %q, want from-env", got)
	}
	t.Setenv("HERON_SERVE_TOKEN", "")
	if got := serveAuthToken(""); got != "" {
		t.Fatalf("no token configured = %q, want empty", got)
	}
}

// The viewer shell stays open so a browser can load the page and be asked for
// the token. Every API route — health included — stays guarded.
func TestRequireBearerTokenLeavesOnlyTheShellOpen(t *testing.T) {
	handler := requireBearerToken("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	open := []string{"/", "/s/fs_abc123", "/static/app.js", "/static/app.css"}
	for _, path := range open {
		t.Run("open "+path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("%s without token = %d, want 200: the page has to load before anyone can type a token", path, recorder.Code)
			}
		})
	}

	guarded := []string{"/api", "/api/", "/api/sessions", "/api/sessions/fs_abc123/record", "/api/status", "/api/stream", "/api/health"}
	for _, path := range guarded {
		t.Run("guarded "+path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("%s without token = %d, want 401", path, recorder.Code)
			}
		})
	}
}

// Anything new under /api/ is guarded unless it is named, so adding a route
// cannot silently widen what an unauthenticated caller can reach.
func TestOpenWithoutTokenFailsClosed(t *testing.T) {
	// Health is guarded like any other API route: it reports how many sessions
	// are loaded and whether wake-ups are pending, which is deployment activity,
	// not a liveness byte. The probe that polls it holds the token already.
	if openWithoutToken("/api/health") {
		t.Fatal("/api/health must require the token")
	}
	if !openWithoutToken("/") || !openWithoutToken("/static/app.js") {
		t.Fatal("the viewer shell must stay open")
	}
	for _, path := range []string{"/api", "/api/sessions", "/api/tasks", "/api/anything-new"} {
		if openWithoutToken(path) {
			t.Fatalf("%s must require the token", path)
		}
	}
}
