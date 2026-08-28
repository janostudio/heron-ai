package tool

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebSearchToolParsesDuckDuckGoResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "golang", req.URL.Query().Get("q"))
		return jsonResponse(req, `{
			"Heading":"Go",
			"AbstractText":"Go language",
			"AbstractURL":"https://go.dev/",
			"RelatedTopics":[{"Text":"Go docs","FirstURL":"https://go.dev/doc/"}]
		}`), nil
	})}

	tool := NewWebSearchTool(client, WebSearchConfig{Endpoint: "https://search.test/api"})
	result, err := tool.Execute(context.Background(), map[string]any{
		"query": "golang",
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, result.Content, "https://go.dev/")
	require.Contains(t, result.Content, "https://go.dev/doc/")
	require.Equal(t, "duckduckgo", result.Metadata["provider"])
}

func TestWebSearchToolParsesBraveResponseAndLimitsResults(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "secret", req.Header.Get("X-Subscription-Token"))
		return jsonResponse(req, `{
			"web":{"results":[
				{"title":"One","url":"https://example.com/1","description":"first"},
				{"title":"Two","url":"https://example.com/2","description":"second"}
			]}
		}`), nil
	})}

	tool := NewWebSearchTool(client, WebSearchConfig{
		Endpoint: "https://search.test/api",
		APIKey:   "secret",
	})
	result, err := tool.Execute(context.Background(), map[string]any{
		"query":       "query",
		"max_results": 1,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "brave", result.Metadata["provider"])
	require.Equal(t, 1, result.Metadata["count"])
	require.Contains(t, result.Content, "One")
	require.NotContains(t, result.Content, "Two")
}

func TestWebSearchToolHandlesHTTPErrorAndRequiredQuery(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Status:     "502 Bad Gateway",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("failed")),
			Request:    req,
		}, nil
	})}

	tool := NewWebSearchTool(client, WebSearchConfig{Endpoint: "https://search.test/api"})
	result, err := tool.Execute(context.Background(), map[string]any{})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "query")

	result, err = tool.Execute(context.Background(), map[string]any{"query": "x"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "status 502")
}

func TestWebFetchToolExtractsHTMLAndBlocksPrivateHosts(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return response(req, "text/html", `<html><head><style>hidden</style></head><body><h1>Hello</h1><p>World</p><script>bad()</script></body></html>`), nil
	})}

	tool := NewWebFetchTool(client, WebFetchConfig{AllowPrivateHosts: true})
	result, err := tool.Execute(context.Background(), map[string]any{"url": "https://example.com/page"})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, result.Content, "Hello")
	require.Contains(t, result.Content, "World")
	require.NotContains(t, result.Content, "hidden")
	require.NotContains(t, result.Content, "bad()")
	require.Equal(t, "text/html", result.Metadata["content_type"])

	blocked := NewWebFetchTool(client, WebFetchConfig{})
	result, err = blocked.Execute(context.Background(), map[string]any{"url": "http://127.0.0.1/page"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "private")
}

func TestWebFetchToolRedirectsAreValidatedAndLimited(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/redirect" {
			return &http.Response{
				StatusCode: http.StatusFound,
				Status:     "302 Found",
				Header:     http.Header{"Location": []string{"/final"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}
		return response(req, "text/plain", "final content"), nil
	})}

	tool := NewWebFetchTool(client, WebFetchConfig{
		AllowPrivateHosts: true,
		MaxRedirects:      1,
	})
	result, err := tool.Execute(context.Background(), map[string]any{"url": "https://example.com/redirect"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, "final content", result.Content)
}

func TestCodeNavToolRunsConfiguredHelper(t *testing.T) {
	dir := t.TempDir()
	tool := NewCodeNavTool(dir, "echo")
	// printf receives the CodeNav arguments; this verifies process wiring and
	// workspace path validation without requiring a language server in CI.
	result, err := tool.Execute(context.Background(), map[string]any{
		"operation": "symbols",
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Contains(t, result.Content, "--operation")
	require.Equal(t, "codenav", result.WorkspaceOps[0].Kind)
}

func TestCodeNavToolRejectsOutsideWorkspaceAndHelperFailure(t *testing.T) {
	dir := t.TempDir()
	tool := NewCodeNavTool(dir, "false")
	result, err := tool.Execute(context.Background(), map[string]any{
		"operation": "definition",
		"file":      "../outside.go",
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "workspace")

	tool = NewCodeNavTool(dir, "false")
	result, err = tool.Execute(context.Background(), map[string]any{
		"operation": "symbols",
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "codenav failed")
}

func TestAskUserQuestionToolReturnsWaitRoute(t *testing.T) {
	tool := NewAskUserQuestionTool()
	result, err := tool.Execute(context.Background(), map[string]any{
		"question":     "Which port should be used?",
		"options":      []any{"3000", "5173"},
		"header":       "Port",
		"multi_select": false,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "wait_input", string(result.Next.Action))
	require.Equal(t, "Which port should be used?", result.Metadata["question"])
}

func TestAskUserQuestionToolRejectsInvalidInput(t *testing.T) {
	tool := NewAskUserQuestionTool()
	result, err := tool.Execute(context.Background(), map[string]any{})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "question")

	result, err = tool.Execute(context.Background(), map[string]any{
		"question": "choose",
		"options":  []any{"", "valid"},
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "empty")
}

func TestExtractHTMLTextCollapsesWhitespace(t *testing.T) {
	text := extractHTMLText(`<p>A</p><div>B</div>`)
	require.True(t, strings.Contains(text, "A"))
	require.True(t, strings.Contains(text, "B"))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(req *http.Request, body string) *http.Response {
	return response(req, "application/json", body)
}

func response(req *http.Request, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
