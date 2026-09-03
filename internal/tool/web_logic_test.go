package tool

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	require.NotNil(t, ip, "invalid IP %q", s)
	return ip
}

func TestClamp(t *testing.T) {
	require.Equal(t, 1, clamp(0, 1, 20))
	require.Equal(t, 20, clamp(99, 1, 20))
	require.Equal(t, 10, clamp(10, 1, 20))
	require.Equal(t, 5, clamp(5, 5, 5))
}

func TestIntParam(t *testing.T) {
	require.Equal(t, 0, intParam(nil, "x"))
	require.Equal(t, 0, intParam(map[string]any{}, "x"))
	require.Equal(t, 42, intParam(map[string]any{"x": 42}, "x"))
	require.Equal(t, 42, intParam(map[string]any{"x": int64(42)}, "x"))
	require.Equal(t, 42, intParam(map[string]any{"x": float64(42.9)}, "x"))
	require.Equal(t, 0, intParam(map[string]any{"x": "42"}, "x"))
}

func TestStringParam(t *testing.T) {
	require.Equal(t, "", stringParam(nil, "x"))
	require.Equal(t, "hi", stringParam(map[string]any{"x": "hi"}, "x"))
	require.Equal(t, "", stringParam(map[string]any{"x": 42}, "x"))
}

func TestBoolParam(t *testing.T) {
	require.False(t, boolParam(nil, "x"))
	require.True(t, boolParam(map[string]any{"x": true}, "x"))
	require.False(t, boolParam(map[string]any{"x": "true"}, "x"))
}

func TestReadLimitedBody(t *testing.T) {
	t.Run("under limit", func(t *testing.T) {
		body, truncated, err := readLimitedBody(strings.NewReader("hello"), 10)
		require.NoError(t, err)
		require.False(t, truncated)
		require.Equal(t, "hello", string(body))
	})
	t.Run("exact limit", func(t *testing.T) {
		body, truncated, err := readLimitedBody(strings.NewReader("hello"), 5)
		require.NoError(t, err)
		require.False(t, truncated)
		require.Equal(t, "hello", string(body))
	})
	t.Run("over limit", func(t *testing.T) {
		body, truncated, err := readLimitedBody(strings.NewReader("hello world"), 5)
		require.NoError(t, err)
		require.True(t, truncated)
		require.Equal(t, "hello", string(body))
	})
	t.Run("empty body", func(t *testing.T) {
		body, truncated, err := readLimitedBody(strings.NewReader(""), 5)
		require.NoError(t, err)
		require.False(t, truncated)
		require.Empty(t, body)
	})
	t.Run("zero max defaults", func(t *testing.T) {
		body, truncated, err := readLimitedBody(strings.NewReader("x"), 0)
		require.NoError(t, err)
		require.False(t, truncated)
		require.Equal(t, "x", string(body))
	})
	t.Run("reader error", func(t *testing.T) {
		_, _, err := readLimitedBody(errorReader{}, 10)
		require.Error(t, err)
	})
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestExtractHTMLText(t *testing.T) {
	t.Run("strips script", func(t *testing.T) {
		got := extractHTMLText(`<p>keep</p><script>drop()</script>`)
		require.Contains(t, got, "keep")
		require.NotContains(t, got, "drop()")
	})
	t.Run("strips style", func(t *testing.T) {
		got := extractHTMLText(`<p>keep</p><style>.x{}</style>`)
		require.Contains(t, got, "keep")
		require.NotContains(t, got, ".x{}")
	})
	t.Run("strips noscript", func(t *testing.T) {
		got := extractHTMLText(`<p>keep</p><noscript>fallback</noscript>`)
		require.Contains(t, got, "keep")
		require.NotContains(t, got, "fallback")
	})
	t.Run("strips template", func(t *testing.T) {
		got := extractHTMLText(`<p>keep</p><template>hidden</template>`)
		require.Contains(t, got, "keep")
		require.NotContains(t, got, "hidden")
	})
	t.Run("converts block breaks", func(t *testing.T) {
		got := extractHTMLText("<p>A</p><div>B</div><br>C<br/>D")
		// breaks become newlines which are then collapsed to single spaces
		require.Equal(t, "A B C D", got)
	})
	t.Run("unescapes entities", func(t *testing.T) {
		got := extractHTMLText(`<p>&amp; &lt;tag&gt;</p>`)
		require.Contains(t, got, "&")
		require.Contains(t, got, "<tag>")
	})
	t.Run("collapses whitespace", func(t *testing.T) {
		got := extractHTMLText("<p>  a   b\n\t c  </p>")
		require.Equal(t, "a b c", got)
	})
	t.Run("empty source", func(t *testing.T) {
		require.Equal(t, "", extractHTMLText(""))
	})
}

func TestIsPrivateIP(t *testing.T) {
	t.Run("loopback", func(t *testing.T) {
		require.True(t, isPrivateIP(parseIP(t, "127.0.0.1")))
		require.True(t, isPrivateIP(parseIP(t, "::1")))
	})
	t.Run("private v4", func(t *testing.T) {
		require.True(t, isPrivateIP(parseIP(t, "10.0.0.1")))
		require.True(t, isPrivateIP(parseIP(t, "192.168.1.1")))
		require.True(t, isPrivateIP(parseIP(t, "172.16.0.1")))
	})
	t.Run("link local", func(t *testing.T) {
		require.True(t, isPrivateIP(parseIP(t, "169.254.0.1")))
		require.True(t, isPrivateIP(parseIP(t, "fe80::1")))
	})
	t.Run("unspecified", func(t *testing.T) {
		require.True(t, isPrivateIP(parseIP(t, "0.0.0.0")))
		require.True(t, isPrivateIP(parseIP(t, "::")))
	})
	t.Run("multicast", func(t *testing.T) {
		require.True(t, isPrivateIP(parseIP(t, "224.0.0.1")))
	})
	t.Run("public v4", func(t *testing.T) {
		require.False(t, isPrivateIP(parseIP(t, "8.8.8.8")))
		require.False(t, isPrivateIP(parseIP(t, "1.1.1.1")))
	})
}

func TestIsBlockedHost(t *testing.T) {
	ctx := context.Background()
	t.Run("localhost", func(t *testing.T) {
		require.True(t, isBlockedHost(ctx, "localhost"))
		require.True(t, isBlockedHost(ctx, "sub.localhost"))
	})
	t.Run("dot local", func(t *testing.T) {
		require.True(t, isBlockedHost(ctx, "foo.local"))
	})
	t.Run("dot internal", func(t *testing.T) {
		require.True(t, isBlockedHost(ctx, "foo.internal"))
	})
	t.Run("trailing dot normalized", func(t *testing.T) {
		require.True(t, isBlockedHost(ctx, "localhost."))
	})
	t.Run("private ip literal", func(t *testing.T) {
		require.True(t, isBlockedHost(ctx, "127.0.0.1"))
		require.True(t, isBlockedHost(ctx, "10.0.0.1"))
		require.True(t, isBlockedHost(ctx, "::1"))
	})
	t.Run("public host", func(t *testing.T) {
		require.False(t, isBlockedHost(ctx, "example.com"))
	})
}

func TestValidateHTTPURL(t *testing.T) {
	ctx := context.Background()
	t.Run("valid https", func(t *testing.T) {
		u, err := validateHTTPURL(ctx, "https://example.com/path", false)
		require.NoError(t, err)
		require.Equal(t, "https", u.Scheme)
	})
	t.Run("valid http", func(t *testing.T) {
		_, err := validateHTTPURL(ctx, "http://example.com", false)
		require.NoError(t, err)
	})
	t.Run("rejects ftp", func(t *testing.T) {
		_, err := validateHTTPURL(ctx, "ftp://example.com", false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "only http and https")
	})
	t.Run("rejects empty scheme", func(t *testing.T) {
		_, err := validateHTTPURL(ctx, "example.com", false)
		require.Error(t, err)
	})
	t.Run("rejects missing host", func(t *testing.T) {
		_, err := validateHTTPURL(ctx, "https:///path", false)
		require.Error(t, err)
	})
	t.Run("rejects user credentials", func(t *testing.T) {
		_, err := validateHTTPURL(ctx, "https://user:pass@example.com", false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "credentials")
	})
	t.Run("blocks private host", func(t *testing.T) {
		_, err := validateHTTPURL(ctx, "https://127.0.0.1", false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "private")
	})
	t.Run("allows private when enabled", func(t *testing.T) {
		_, err := validateHTTPURL(ctx, "https://127.0.0.1", true)
		require.NoError(t, err)
	})
}

// TestWebFetchRelativeRedirect proves a relative Location header (e.g.
// "/final") is resolved against the request URL before CheckRedirect runs, so
// it is NOT rejected by the http/https scheme check.
func TestWebFetchRelativeRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "final content")
	}))
	defer srv.Close()

	tool := NewWebFetchTool(srv.Client(), WebFetchConfig{AllowPrivateHosts: true, MaxRedirects: 5})
	result, err := tool.Execute(context.Background(), map[string]any{"url": srv.URL + "/redirect"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, "final content", result.Content)
}

func TestWebFetchRejectsTooManyRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer srv.Close()

	tool := NewWebFetchTool(srv.Client(), WebFetchConfig{AllowPrivateHosts: true, MaxRedirects: 2})
	result, err := tool.Execute(context.Background(), map[string]any{"url": srv.URL + "/loop"})
	require.NoError(t, err)
	require.False(t, result.Success)
}
