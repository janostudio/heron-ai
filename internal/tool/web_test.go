package tool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
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

	tool := NewWebSearchTool(client, WebSearchConfig{
		Provider: types.WebProviderDuckDuckGo,
		Endpoint: "https://search.test/api",
	})
	result, err := tool.Execute(context.Background(), map[string]any{
		"query": "golang",
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, result.Content, "https://go.dev/")
	require.Contains(t, result.Content, "https://go.dev/doc/")
	require.Equal(t, "duckduckgo", result.Metadata["provider"])
}

// TestWebSearchToolBraveSendsBraveParams 锁定现状 bug 的修复：brave 只收
// q / count / freshness，不再带 DuckDuckGo 那套 format / no_html / skip_disambig。
func TestWebSearchToolBraveSendsBraveParams(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "secret", req.Header.Get("X-Subscription-Token"))
		query := req.URL.Query()
		require.Equal(t, "query site:example.com", query.Get("q"))
		require.Equal(t, "1", query.Get("count"))
		require.Equal(t, "7d", query.Get("freshness"))
		require.Empty(t, query.Get("format"))
		require.Empty(t, query.Get("no_html"))
		require.Empty(t, query.Get("no_redirect"))
		require.Empty(t, query.Get("skip_disambig"))
		return jsonResponse(req, `{
			"web":{"results":[
				{"title":"One","url":"https://example.com/1","description":"first"},
				{"title":"Two","url":"https://example.com/2","description":"second"}
			]}
		}`), nil
	})}

	tool := NewWebSearchTool(client, WebSearchConfig{
		Provider: types.WebProviderBrave,
		Endpoint: "https://search.test/api",
		APIKey:   "secret",
	})
	result, err := tool.Execute(context.Background(), map[string]any{
		"query":        "query",
		"domains":      []any{"example.com"},
		"max_results":  1,
		"recency_days": 7,
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

	tool := NewWebSearchTool(client, WebSearchConfig{
		Provider: types.WebProviderDuckDuckGo,
		Endpoint: "https://search.test/api",
	})
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

	tool := NewWebFetchTool(client, WebFetchConfig{
		Provider:          types.WebProviderDirect,
		AllowPrivateHosts: true,
	})
	result, err := tool.Execute(context.Background(), map[string]any{"url": "https://example.com/page"})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, result.Content, "Hello")
	require.Contains(t, result.Content, "World")
	require.NotContains(t, result.Content, "hidden")
	require.NotContains(t, result.Content, "bad()")
	require.Equal(t, "text/html", result.Metadata["content_type"])

	blocked := NewWebFetchTool(client, WebFetchConfig{Provider: types.WebProviderDirect})
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
		Provider:          types.WebProviderDirect,
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
	tool := NewCodeNavTool(newTestWorkspace(t, dir), "echo")
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
	tool := NewCodeNavTool(newTestWorkspace(t, dir), "false")
	result, err := tool.Execute(context.Background(), map[string]any{
		"operation": "definition",
		"file":      "../outside.go",
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "workspace")

	tool = NewCodeNavTool(newTestWorkspace(t, dir), "false")
	result, err = tool.Execute(context.Background(), map[string]any{
		"operation": "symbols",
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "codenav failed")
}

func TestAskUserQuestionToolReturnsPendingInput(t *testing.T) {
	tool := NewAskUserQuestionTool()
	result, err := tool.Execute(context.Background(), map[string]any{
		"question":     "Which port should be used?",
		"options":      []any{"3000", "5173"},
		"header":       "Port",
		"multi_select": false,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Nil(t, result.Next)
	require.NotNil(t, result.PendingInput)
	require.Equal(t, "Which port should be used?", result.PendingInput.Question)
	require.Equal(t, []string{"3000", "5173"}, result.PendingInput.Options)
	require.Equal(t, "Port", result.PendingInput.Header)
	require.False(t, result.PendingInput.MultiSelect)
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

// ---- provider 可插拔相关用例 ----

func TestNewSearchProvider(t *testing.T) {
	for _, name := range []string{
		types.WebProviderTinyFish,
		types.WebProviderBrave,
		types.WebProviderDuckDuckGo,
		types.WebProviderTavily,
	} {
		provider, err := newSearchProvider(name)
		require.NoError(t, err, name)
		require.Equal(t, name, provider.Name())
	}

	provider, err := newSearchProvider("bing")
	require.Error(t, err)
	require.Nil(t, provider)
	require.Contains(t, err.Error(), "unknown web search provider")

	provider, err = newSearchProvider("")
	require.Error(t, err)
	require.Nil(t, provider)
}

func TestNewFetchProvider(t *testing.T) {
	for name, want := range map[string]string{
		types.WebProviderTinyFish:   types.WebProviderTinyFish,
		types.WebProviderTavily:     types.WebProviderTavily,
		types.WebProviderDirect:     types.WebProviderDirect,
		types.WebProviderBrave:      types.WebProviderDirect,
		types.WebProviderDuckDuckGo: types.WebProviderDirect,
	} {
		provider, err := newFetchProvider(name)
		require.NoError(t, err, name)
		require.Equal(t, want, provider.Name(), name)
	}

	provider, err := newFetchProvider("jina")
	require.Error(t, err)
	require.Nil(t, provider)
	require.Contains(t, err.Error(), "unknown web fetch provider")
}

// TestWebSearchToolDefaultsToDuckDuckGo 锁定「开箱即用」：不配任何东西时默认
// 走免 key 的 duckduckgo，search 能直接发请求，fetch 因为没有 duckduckgo 的
// fetch API 而落到 direct（本地直连），不能报错。
func TestWebSearchToolDefaultsToDuckDuckGo(t *testing.T) {
	tool := NewWebSearchTool(nil, WebSearchConfig{})
	require.Equal(t, types.WebProviderDuckDuckGo, tool.config.Provider)
	require.Equal(t, types.DefaultWebSearchURL(types.WebProviderDuckDuckGo), tool.config.Endpoint)
	require.Equal(t, 10, tool.config.MaxResults)
	require.True(t, tool.config.TimeoutMs > 0)

	fetch := NewWebFetchTool(nil, WebFetchConfig{})
	require.Equal(t, types.WebProviderDuckDuckGo, fetch.config.Provider)
	require.Equal(t, types.DefaultWebFetchURL(types.WebProviderDuckDuckGo), fetch.config.Endpoint)
	require.Equal(t, "markdown", fetch.config.Format)
	require.True(t, fetch.config.TimeoutMs > 0)

	// duckduckgo 没有 fetch API，默认配置必须退化成 direct 而不是 unknown provider。
	fetchProvider, err := newFetchProvider(fetch.config.Provider)
	require.NoError(t, err)
	require.Equal(t, types.WebProviderDirect, fetchProvider.Name())
	require.False(t, fetchProvider.RequiresAPIKey())
}

// TestWebSearchToolDefaultProviderWorksWithoutAPIKey 校验默认 provider 在完全
// 不配 key 的情况下也能发出请求（免 key 放行）。
func TestWebSearchToolDefaultProviderWorksWithoutAPIKey(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Heading":"Go","AbstractText":"lang","AbstractURL":"https://go.dev/"}`)
	}))
	defer srv.Close()

	tool := NewWebSearchTool(srv.Client(), WebSearchConfig{Endpoint: srv.URL})
	result, err := tool.Execute(context.Background(), map[string]any{"query": "golang"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.True(t, called, "默认 provider 免 key，应当发出请求")
	require.Equal(t, types.WebProviderDuckDuckGo, result.Metadata["provider"])
}

// TestWebSearchToolTinyFishRequest 用假 TinyFish 校验请求形状：X-API-Key 头、
// include_domains、recency_days→recency_minutes 换算，且不发 max_results
// （该 API 没有这个参数，条数在客户端截断）。
func TestWebSearchToolTinyFishRequest(t *testing.T) {
	var (
		gotQuery   url.Values
		gotAPIKey  string
		gotDomains string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		gotAPIKey = r.Header.Get("X-API-Key")
		gotDomains = r.URL.Query().Get("include_domains")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"query":"nvidia",
			"results":[
				{"position":1,"site_name":"NVIDIA","title":"Q3 FY2026","snippet":"record revenue","url":"https://investor.nvidia.com/q3"},
				{"position":2,"site_name":"Reuters","title":"Nvidia beats","snippet":"demand","url":"reuters.com/tech/nvidia"},
				{"position":3,"site_name":"Bloomberg","title":"Chips","snippet":"ai","url":"https://bloomberg.com/chips"}
			],
			"total_results":3,
			"page":0
		}`)
	}))
	defer srv.Close()

	tool := NewWebSearchTool(srv.Client(), WebSearchConfig{
		Provider: types.WebProviderTinyFish,
		Endpoint: srv.URL,
		APIKey:   "tf-secret",
		Location: "US",
		Language: "en",
	})
	result, err := tool.Execute(context.Background(), map[string]any{
		"query":        "nvidia earnings",
		"domains":      []any{"investor.nvidia.com", "reuters.com"},
		"recency_days": 3,
		"max_results":  2,
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	require.Equal(t, "tf-secret", gotAPIKey)
	require.Equal(t, "nvidia earnings", gotQuery.Get("query"))
	require.Equal(t, "investor.nvidia.com,reuters.com", gotDomains)
	require.Equal(t, "4320", gotQuery.Get("recency_minutes")) // 3 天 = 3*1440 分钟
	require.Equal(t, "US", gotQuery.Get("location"))
	require.Equal(t, "en", gotQuery.Get("language"))
	require.Empty(t, gotQuery.Get("max_results"))
	require.NotContains(t, gotQuery.Get("query"), "site:")

	require.Equal(t, types.WebProviderTinyFish, result.Metadata["provider"])
	require.Equal(t, 2, result.Metadata["count"])
	require.Equal(t, true, result.Metadata["truncated"])
	require.Contains(t, result.Content, "https://investor.nvidia.com/q3")
	// 缺 scheme 的 url 补全为 https://，否则下游 WebFetch 会被 validateHTTPURL 拒掉
	require.Contains(t, result.Content, "https://reuters.com/tech/nvidia")
	require.NotContains(t, result.Content, "bloomberg.com")
}

// TestWebSearchToolTavilySendsExpectedRequest 校验 tavily 的请求形状：POST +
// Bearer 认证（不是 tinyfish 的 X-API-Key），以及 body 里的固定字段；摘要取
// results[].content 而不是 snippet。
func TestWebSearchToolTavilySendsExpectedRequest(t *testing.T) {
	var (
		gotMethod string
		gotAuth   string
		gotBody   map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"query":"nvidia earnings",
			"results":[
				{"title":"Q3 FY2026","url":"https://investor.nvidia.com/q3","content":"record revenue","score":0.9},
				{"title":"Nvidia beats","url":"reuters.com/tech/nvidia","content":"demand","score":0.8}
			],
			"answer":"",
			"response_time":1
		}`)
	}))
	defer srv.Close()

	tool := NewWebSearchTool(srv.Client(), WebSearchConfig{
		Provider: types.WebProviderTavily,
		Endpoint: srv.URL,
		APIKey:   "tvly-secret",
	})
	result, err := tool.Execute(context.Background(), map[string]any{
		"query":       "nvidia earnings",
		"domains":     []any{"investor.nvidia.com", "reuters.com"},
		"max_results": 5,
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "Bearer tvly-secret", gotAuth)
	require.Equal(t, "nvidia earnings", gotBody["query"])
	require.EqualValues(t, 5, gotBody["max_results"])
	require.Equal(t, []any{"investor.nvidia.com", "reuters.com"}, gotBody["include_domains"])
	require.Equal(t, "general", gotBody["topic"])
	require.Equal(t, "basic", gotBody["search_depth"])
	require.Equal(t, false, gotBody["include_raw_content"])
	require.Equal(t, false, gotBody["include_answer"])
	// 未传 recency_days 时不应出现 time_range
	require.NotContains(t, gotBody, "time_range")

	require.Equal(t, types.WebProviderTavily, result.Metadata["provider"])
	require.Equal(t, 2, result.Metadata["count"])
	require.Contains(t, result.Content, "https://investor.nvidia.com/q3")
	require.Contains(t, result.Content, "record revenue")
	// 缺 scheme 的 url 补全为 https://，否则下游 WebFetch 会被 validateHTTPURL 拒掉
	require.Contains(t, result.Content, "https://reuters.com/tech/nvidia")
}

// TestWebSearchToolTavilyRecencyMapsToTimeRange：tavily 只接受 day/week/month/year
// 四档，recency_days 是有损映射，这里锁定四个边界。
func TestWebSearchToolTavilyRecencyMapsToTimeRange(t *testing.T) {
	cases := map[int]string{
		1:   "day",
		7:   "week",
		31:  "month",
		400: "year",
	}
	for days, want := range cases {
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"results":[{"title":"t","url":"https://example.com","content":"c"}]}`)
		}))

		tool := NewWebSearchTool(srv.Client(), WebSearchConfig{
			Provider: types.WebProviderTavily,
			Endpoint: srv.URL,
			APIKey:   "tvly-secret",
		})
		result, err := tool.Execute(context.Background(), map[string]any{
			"query":        "golang",
			"recency_days": days,
		})
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
		require.Equal(t, want, gotBody["time_range"], "recency_days=%d", days)
		srv.Close()
	}
}

// TestWebSearchToolTavilyRequiresAPIKey：tavily 无 key 时 fail-closed，不发请求。
func TestWebSearchToolTavilyRequiresAPIKey(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tool := NewWebSearchTool(srv.Client(), WebSearchConfig{
		Provider: types.WebProviderTavily,
		Endpoint: srv.URL,
	})
	result, err := tool.Execute(context.Background(), map[string]any{"query": "golang"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "requires an API key")
	require.False(t, called)
}

// TestWebSearchToolRequiresAPIKeyIsFailClosed 校验需要 key 的 provider 在没有
// key 时直接失败，且不静默降级到 duckduckgo、也不发任何 HTTP 请求。
func TestWebSearchToolRequiresAPIKeyIsFailClosed(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, provider := range []string{types.WebProviderTinyFish, types.WebProviderBrave, types.WebProviderTavily} {
		called = false
		tool := NewWebSearchTool(srv.Client(), WebSearchConfig{
			Provider: provider,
			Endpoint: srv.URL,
		})
		result, err := tool.Execute(context.Background(), map[string]any{"query": "golang"})
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Contains(t, result.Error, "requires an API key")
		require.Contains(t, result.Error, "web.api_key")
		require.False(t, called, "provider %s 无 key 时不应发起请求", provider)
	}
}

func TestWebSearchToolUnknownProviderFails(t *testing.T) {
	tool := NewWebSearchTool(nil, WebSearchConfig{Provider: "bing", APIKey: "k"})
	result, err := tool.Execute(context.Background(), map[string]any{"query": "golang"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "unknown web search provider")
}

func TestWebFetchToolTinyFishPostsExpectedBody(t *testing.T) {
	var (
		gotMethod string
		gotAPIKey string
		gotBody   map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAPIKey = r.Header.Get("X-API-Key")
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"url":"https://example.com/page","text":"# fetched content","status":200}]}`)
	}))
	defer srv.Close()

	tool := NewWebFetchTool(srv.Client(), WebFetchConfig{
		Provider: types.WebProviderTinyFish,
		Endpoint: srv.URL,
		APIKey:   "tf-secret",
	})
	result, err := tool.Execute(context.Background(), map[string]any{"url": "https://example.com/page"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "tf-secret", gotAPIKey)
	urls, ok := gotBody["urls"].([]any)
	require.True(t, ok)
	require.Equal(t, "https://example.com/page", urls[0])
	require.Equal(t, "markdown", gotBody["format"])
	require.EqualValues(t, 45000, gotBody["per_url_timeout_ms"])

	require.Equal(t, "# fetched content", result.Content)
	require.Equal(t, types.WebProviderTinyFish, result.Metadata["provider"])
	require.Equal(t, "https://example.com/page", result.Metadata["url"])
	require.False(t, result.Metadata["truncated"].(bool))
}

// TestWebFetchToolTinyFishErrorsFailClosed：该 API 是 200 携带 per-URL 错误
// （timeout / DNS / 反爬），errors 非空必须当失败返回，不能返回空内容。
func TestWebFetchToolTinyFishErrorsFailClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[],"errors":[{"url":"https://example.com/page","error":"Navigation timeout","error_code":408}]}`)
	}))
	defer srv.Close()

	tool := NewWebFetchTool(srv.Client(), WebFetchConfig{
		Provider: types.WebProviderTinyFish,
		Endpoint: srv.URL,
		APIKey:   "tf-secret",
	})
	result, err := tool.Execute(context.Background(), map[string]any{"url": "https://example.com/page"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "Navigation timeout")
	require.Empty(t, result.Content)
}

func TestWebFetchToolTinyFishRequiresAPIKey(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tool := NewWebFetchTool(srv.Client(), WebFetchConfig{
		Provider: types.WebProviderTinyFish,
		Endpoint: srv.URL,
	})
	result, err := tool.Execute(context.Background(), map[string]any{"url": "https://example.com/page"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "requires an API key")
	require.False(t, called)
}

// TestWebFetchToolTinyFishDegradesToDirectForPrivateHost：私网 / localhost
// 目标不能发给第三方 provider，自动降级为本地直连。
func TestWebFetchToolTinyFishDegradesToDirectForPrivateHost(t *testing.T) {
	externalCalled := false
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer external.Close()

	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "local content")
	}))
	defer local.Close()

	tool := NewWebFetchTool(&http.Client{}, WebFetchConfig{
		Provider:          types.WebProviderTinyFish,
		Endpoint:          external.URL,
		APIKey:            "tf-secret",
		AllowPrivateHosts: true,
	})
	result, err := tool.Execute(context.Background(), map[string]any{"url": local.URL})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, "local content", result.Content)
	require.False(t, externalCalled, "私网 URL 不应发给第三方 endpoint")
}

// TestWebFetchToolTavilyPostsExpectedBody 校验 tavily extract 的请求形状与正文
// 取值：POST + Bearer，body 是 urls / format / extract_depth，正文取
// results[0].raw_content。
func TestWebFetchToolTavilyPostsExpectedBody(t *testing.T) {
	var (
		gotMethod string
		gotAuth   string
		gotBody   map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"url":"https://example.com/page","raw_content":"# fetched content"}],"failed_results":[]}`)
	}))
	defer srv.Close()

	tool := NewWebFetchTool(srv.Client(), WebFetchConfig{
		Provider: types.WebProviderTavily,
		Endpoint: srv.URL,
		APIKey:   "tvly-secret",
	})
	result, err := tool.Execute(context.Background(), map[string]any{"url": "https://example.com/page"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "Bearer tvly-secret", gotAuth)
	urls, ok := gotBody["urls"].([]any)
	require.True(t, ok)
	require.Equal(t, "https://example.com/page", urls[0])
	require.Equal(t, "markdown", gotBody["format"])
	require.Equal(t, "basic", gotBody["extract_depth"])

	require.Equal(t, "# fetched content", result.Content)
	require.Equal(t, types.WebProviderTavily, result.Metadata["provider"])
}

// TestWebFetchToolTavilyFailedResultsFailClosed：tavily 是 200 携带 per-URL 失败
// （failed_results），非空必须判失败，不能返回空正文。
func TestWebFetchToolTavilyFailedResultsFailClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[],"failed_results":[{"url":"https://example.com/page","error":"Unable to fetch"}]}`)
	}))
	defer srv.Close()

	tool := NewWebFetchTool(srv.Client(), WebFetchConfig{
		Provider: types.WebProviderTavily,
		Endpoint: srv.URL,
		APIKey:   "tvly-secret",
	})
	result, err := tool.Execute(context.Background(), map[string]any{"url": "https://example.com/page"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "Unable to fetch")
	require.Empty(t, result.Content)
}

// TestWebFetchToolTavilyRejectsUnsupportedFormat：tavily extract 只支持
// markdown / text，html / json 必须明确报错，不静默改成 markdown。
func TestWebFetchToolTavilyRejectsUnsupportedFormat(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, format := range []string{"html", "json"} {
		called = false
		tool := NewWebFetchTool(srv.Client(), WebFetchConfig{
			Provider: types.WebProviderTavily,
			Endpoint: srv.URL,
			APIKey:   "tvly-secret",
			Format:   format,
		})
		result, err := tool.Execute(context.Background(), map[string]any{"url": "https://example.com/page"})
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Contains(t, result.Error, "tavily")
		require.Contains(t, result.Error, format)
		require.False(t, called, "format %s 被拒时不应发起请求", format)
	}
}

func TestWebFetchToolRejectsInvalidFormat(t *testing.T) {
	tool := NewWebFetchTool(nil, WebFetchConfig{
		Provider: types.WebProviderTinyFish,
		Format:   "yaml",
		APIKey:   "tf-secret",
	})
	result, err := tool.Execute(context.Background(), map[string]any{"url": "https://example.com/page"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "format")
	require.Contains(t, result.Error, "yaml")
}

func TestWebFetchToolUnknownProviderFails(t *testing.T) {
	tool := NewWebFetchTool(nil, WebFetchConfig{
		Provider: "jina",
		APIKey:   "k",
	})
	result, err := tool.Execute(context.Background(), map[string]any{"url": "https://example.com/page"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "unknown web fetch provider")
}

func TestNormalizeResultURL(t *testing.T) {
	require.Equal(t, "https://investor.nvidia.com/q3", normalizeResultURL("investor.nvidia.com/q3"))
	require.Equal(t, "https://example.com/a", normalizeResultURL("//example.com/a"))
	require.Equal(t, "http://example.com/a", normalizeResultURL("http://example.com/a"))
	require.Equal(t, "https://example.com/a", normalizeResultURL("https://example.com/a"))
	require.Equal(t, "", normalizeResultURL(""))
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
