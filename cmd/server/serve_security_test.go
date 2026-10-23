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
