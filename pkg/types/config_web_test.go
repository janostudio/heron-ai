package types

import "testing"

func TestWebConfigWithDefaults_FillsDuckDuckGo(t *testing.T) {
	got := WebConfig{APIKey: "k"}.WithDefaults()

	if got.Provider != WebProviderDuckDuckGo {
		t.Fatalf("provider = %q, want %q", got.Provider, WebProviderDuckDuckGo)
	}
	if got.Search.URL != DefaultWebSearchURL(WebProviderDuckDuckGo) {
		t.Fatalf("search url = %q, want %q", got.Search.URL, DefaultWebSearchURL(WebProviderDuckDuckGo))
	}
	if got.Fetch.URL != DefaultWebFetchURL(WebProviderDuckDuckGo) {
		t.Fatalf("fetch url = %q, want %q", got.Fetch.URL, DefaultWebFetchURL(WebProviderDuckDuckGo))
	}
	if got.Search.MaxResults != 10 {
		t.Fatalf("max results = %d, want 10", got.Search.MaxResults)
	}
	if got.Search.TimeoutMs != 30000 {
		t.Fatalf("search timeout = %d, want 30000", got.Search.TimeoutMs)
	}
	if got.Fetch.TimeoutMs != 45000 {
		t.Fatalf("fetch timeout = %d, want 45000", got.Fetch.TimeoutMs)
	}
	if got.Fetch.Format != "markdown" {
		t.Fatalf("format = %q, want markdown", got.Fetch.Format)
	}
	if got.APIKey != "k" {
		t.Fatalf("api key = %q, want k", got.APIKey)
	}
}

func TestWebConfigWithDefaults_KeepsExplicitValues(t *testing.T) {
	in := WebConfig{
		Provider: WebProviderBrave,
		Search:   WebSearchConfig{URL: "https://custom/search", MaxResults: 3, TimeoutMs: 100},
		Fetch:    WebFetchConfig{URL: "https://custom/fetch", Format: "html", TimeoutMs: 200},
	}

	got := in.WithDefaults()

	if got.Provider != WebProviderBrave {
		t.Fatalf("provider = %q, want brave", got.Provider)
	}
	if got.Search.URL != "https://custom/search" {
		t.Fatalf("search url = %q, want explicit override", got.Search.URL)
	}
	if got.Fetch.URL != "https://custom/fetch" {
		t.Fatalf("fetch url = %q, want explicit override", got.Fetch.URL)
	}
	if got.Search.MaxResults != 3 || got.Search.TimeoutMs != 100 {
		t.Fatalf("search = %+v, want explicit values kept", got.Search)
	}
	if got.Fetch.Format != "html" || got.Fetch.TimeoutMs != 200 {
		t.Fatalf("fetch = %+v, want explicit values kept", got.Fetch)
	}
}

// WithDefaults 不得修改入参（值接收者 + 返回新值）。
func TestWebConfigWithDefaults_DoesNotMutateInput(t *testing.T) {
	in := WebConfig{Provider: WebProviderDuckDuckGo}

	in.WithDefaults()

	if in.Search.URL != "" || in.Fetch.URL != "" || in.Fetch.Format != "" {
		t.Fatalf("input mutated: %+v", in)
	}
}

// Format 非法值保持原样，交给 tool 层报错，不在配置层静默改写。
func TestWebConfigWithDefaults_KeepsInvalidFormat(t *testing.T) {
	got := WebConfig{Fetch: WebFetchConfig{Format: "yaml"}}.WithDefaults()

	if got.Fetch.Format != "yaml" {
		t.Fatalf("format = %q, want yaml kept for tool-layer validation", got.Fetch.Format)
	}
}

// fetch.provider 留空表示「跟随 web.provider」：不回填默认值，endpoint 按
// web.provider 取。
func TestWebFetchProvider_FollowsWebProviderWhenEmpty(t *testing.T) {
	in := WebConfig{Provider: WebProviderBrave}

	if got := in.FetchProvider(); got != WebProviderBrave {
		t.Fatalf("fetch provider = %q, want %q (follow web.provider)", got, WebProviderBrave)
	}
	got := in.WithDefaults()
	if got.Fetch.Provider != "" {
		t.Fatalf("fetch provider = %q, want empty (跟随语义不得被默认值抹掉)", got.Fetch.Provider)
	}
	// brave 没有 fetch API，跟随模式下 endpoint 为空，由 tool 层按 direct 处理。
	if got.Fetch.URL != DefaultWebFetchURL(WebProviderBrave) {
		t.Fatalf("fetch url = %q, want %q", got.Fetch.URL, DefaultWebFetchURL(WebProviderBrave))
	}
}

// fetch.provider 显式写 direct 时不再跟随：search 仍走默认 duckduckgo，
// fetch 本地直连。
func TestWebFetchProvider_ExplicitOverridesWebProvider(t *testing.T) {
	in := WebConfig{Fetch: WebFetchConfig{Provider: WebProviderDirect}}

	if got := in.FetchProvider(); got != WebProviderDirect {
		t.Fatalf("fetch provider = %q, want %q", got, WebProviderDirect)
	}
	got := in.WithDefaults()
	if got.Provider != WebProviderDuckDuckGo {
		t.Fatalf("web provider = %q, want %q", got.Provider, WebProviderDuckDuckGo)
	}
	if got.Search.URL != DefaultWebSearchURL(WebProviderDuckDuckGo) {
		t.Fatalf("search url = %q, want duckduckgo endpoint", got.Search.URL)
	}
	if got.Fetch.URL != "" {
		t.Fatalf("fetch url = %q, want empty (direct 不经外部 API)", got.Fetch.URL)
	}
}

func TestDefaultWebURLs(t *testing.T) {
	cases := map[string]struct{ search, fetch string }{
		WebProviderTinyFish:   {"https://api.search.tinyfish.ai", "https://api.fetch.tinyfish.ai"},
		WebProviderBrave:      {"https://api.search.brave.com/res/v1/web/search", ""},
		WebProviderDuckDuckGo: {"https://api.duckduckgo.com/", ""},
		WebProviderTavily:     {"https://api.tavily.com/search", "https://api.tavily.com/extract"},
		WebProviderDirect:     {"", ""},
		"unknown":             {"", ""},
	}
	for provider, want := range cases {
		if got := DefaultWebSearchURL(provider); got != want.search {
			t.Fatalf("search url(%s) = %q, want %q", provider, got, want.search)
		}
		if got := DefaultWebFetchURL(provider); got != want.fetch {
			t.Fatalf("fetch url(%s) = %q, want %q", provider, got, want.fetch)
		}
	}
}
