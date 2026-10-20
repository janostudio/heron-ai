package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

const (
	defaultWebMaxBytes          = 256 * 1024
	defaultWebSearchMaxResults  = 10
	defaultWebSearchTimeoutMs   = 30000
	defaultWebFetchTimeoutMs    = 45000
	defaultWebFetchMaxRedirects = 5
	// tinyFishEnvelopeSlack 是 tinyfish 响应里 JSON 信封（url / status 等字段）
	// 相对正文的额外开销。读 body 时把它一起计入上限，避免正文刚好等于上限时
	// 信封被截断、JSON 解析失败。
	tinyFishEnvelopeSlack = 32 * 1024
	// tavilyEnvelopeSlack 同 tinyFishEnvelopeSlack，用于 tavily 的 extract 响应
	// （results / failed_results 信封包着 raw_content）。
	tavilyEnvelopeSlack = 32 * 1024
	// webFetchFormatMarkdown 是默认抓取格式，与 types.WebFetchConfig 的约定一致。
	webFetchFormatMarkdown = "markdown"
)

// WebSearchConfig 是 WebSearch 工具的数据源配置。
type WebSearchConfig struct {
	// Provider 为 tinyfish | brave | duckduckgo | tavily；空 = duckduckgo。
	Provider string
	// Endpoint 为完整 endpoint；空 = 取 provider 内置默认。
	Endpoint string
	// APIKey 为 tinyfish / brave / tavily 必填，缺失时 fail-closed（不降级、不发请求）。
	APIKey string
	// Location 为国家码，tinyfish 专有。
	Location string
	// Language 为语言码，tinyfish 专有。
	Language string
	// MaxResults 为默认返回条数上限，可被 max_results 参数覆盖。
	MaxResults int
	// MaxResponseBytes 为响应体上限；空 = 256KB。
	MaxResponseBytes int
	// TimeoutMs 为单次请求超时；空 = 30000。
	TimeoutMs int
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

// withWebTimeout 为单次 web 请求套上配置里的超时；timeoutMs <= 0 表示不限。
func withWebTimeout(ctx context.Context, timeoutMs int) (context.Context, context.CancelFunc) {
	if timeoutMs <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
}

type WebSearchTool struct {
	client *http.Client
	config WebSearchConfig
}

func NewWebSearchTool(client *http.Client, config WebSearchConfig) *WebSearchTool {
	if client == nil {
		client = http.DefaultClient
	}
	config.Provider = strings.ToLower(strings.TrimSpace(config.Provider))
	if config.Provider == "" {
		// 默认 duckduckgo：免 key、零配置就能搜，引擎「开箱即用」。代价是它是
		// Instant Answer API 而不是网页搜索——只回答百科 / 实体类查询，普通搜索
		// 词往往返回空结果。要真搜索就得配 tinyfish / brave / tavily 的 key。
		config.Provider = types.WebProviderDuckDuckGo
	}
	if strings.TrimSpace(config.Endpoint) == "" {
		config.Endpoint = types.DefaultWebSearchURL(config.Provider)
	}
	if config.MaxResults <= 0 {
		config.MaxResults = defaultWebSearchMaxResults
	}
	if config.MaxResponseBytes <= 0 {
		config.MaxResponseBytes = defaultWebMaxBytes
	}
	if config.TimeoutMs <= 0 {
		config.TimeoutMs = defaultWebSearchTimeoutMs
	}
	return &WebSearchTool{client: client, config: config}
}

func (t *WebSearchTool) Name() string        { return "WebSearch" }
func (t *WebSearchTool) Description() string { return "Search the public web for information" }
func (t *WebSearchTool) NeedsApproval() bool { return false }
func (t *WebSearchTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolReadOnly}
}
func (t *WebSearchTool) Parameters() map[string]any {
	return map[string]any{
		"query":        map[string]any{"type": "string", "description": "Search query", "required": true},
		"domains":      map[string]any{"type": "array", "description": "Optional domains to restrict the search to"},
		"max_results":  map[string]any{"type": "integer", "description": "Maximum number of results"},
		"recency_days": map[string]any{"type": "integer", "description": "Optional recency window in days"},
	}
}

// WebSearchResult 是归一化后的一条搜索结果，与具体 provider 的响应字段无关。
type WebSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

func (t *WebSearchTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query := strings.TrimSpace(stringParam(params, "query"))
	if query == "" {
		return &types.ToolResult{Success: false, Error: "query parameter is required"}, nil
	}
	provider, err := newSearchProvider(t.config.Provider)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	// 需要 key 的 provider 在无 key 时直接失败：绝不静默降级到 duckduckgo，
	// 否则用户以为在用付费检索，实际拿到的是免费即时答案。
	if provider.RequiresAPIKey() && strings.TrimSpace(t.config.APIKey) == "" {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("%s search requires an API key: set web.api_key in settings.json", provider.Name()),
		}, nil
	}
	request := searchRequest{
		Query:       query,
		Domains:     cleanDomains(stringSliceParam(params, "domains")),
		MaxResults:  t.config.MaxResults,
		RecencyDays: intParam(params, "recency_days"),
	}
	if maxResults := intParam(params, "max_results"); maxResults > 0 {
		request.MaxResults = maxResults
	}
	results, err := provider.Search(ctx, t.client, t.config, request)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	// 条数一律在客户端截断：不是每个 provider 都有 max_results 参数，
	// 有参数的那个也未必按我们的上限返回。
	truncated := false
	if len(results) > request.MaxResults {
		results = results[:request.MaxResults]
		truncated = true
	}
	lines := make([]string, 0, len(results))
	for i, result := range results {
		lines = append(lines, fmt.Sprintf("%d. %s\n%s\n%s", i+1, result.Title, result.URL, result.Snippet))
	}
	return &types.ToolResult{
		Success: true,
		Content: strings.Join(lines, "\n\n"),
		Metadata: map[string]any{
			"provider":  provider.Name(),
			"query":     query,
			"count":     len(results),
			"truncated": truncated,
		},
	}, nil
}

// searchRequest 是一次搜索的归一化入参，与 provider 的 wire format 解耦。
type searchRequest struct {
	Query       string
	Domains     []string
	MaxResults  int
	RecencyDays int
}

// SearchProvider 抽象一个搜索数据源：自己拼请求、自己解析响应。
//
// 早期实现靠响应形状猜 provider（parseSearchResponse），一旦某个 provider 的
// 字段缺省就会猜错，现在改由各 provider 自己负责。
type SearchProvider interface {
	Name() string
	// RequiresAPIKey 为 true 时，无 APIKey 直接失败，不发请求、不降级。
	RequiresAPIKey() bool
	// Search 返回未截断的结果列表，条数上限由调用方统一处理。
	Search(ctx context.Context, client *http.Client, config WebSearchConfig, request searchRequest) ([]WebSearchResult, error)
}

func newSearchProvider(name string) (SearchProvider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case types.WebProviderTinyFish:
		return tinyFishSearchProvider{}, nil
	case types.WebProviderBrave:
		return braveSearchProvider{}, nil
	case types.WebProviderDuckDuckGo:
		return duckDuckGoSearchProvider{}, nil
	case types.WebProviderTavily:
		return tavilySearchProvider{}, nil
	default:
		return nil, fmt.Errorf("unknown web search provider %q", name)
	}
}

// doSearchRequest 发起一次 GET 搜索请求，返回截断到 maxBytes 的响应体。
func doSearchRequest(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, maxBytes, timeoutMs int) ([]byte, error) {
	ctx, cancel := withWebTimeout(ctx, timeoutMs)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Heron-AI/1.0")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("web search returned status %d", resp.StatusCode)
	}
	body, _, err := readLimitedBody(resp.Body, maxBytes)
	return body, err
}

// doPostJSONRequest 发起一次 POST JSON 请求（tavily 这类以 body 传参的 provider
// 用），返回截断到 maxBytes 的响应体。
func doPostJSONRequest(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, payload any, maxBytes, timeoutMs int) ([]byte, error) {
	ctx, cancel := withWebTimeout(ctx, timeoutMs)
	defer cancel()
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Heron-AI/1.0")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("web request returned status %d", resp.StatusCode)
	}
	body, _, err := readLimitedBody(resp.Body, maxBytes)
	return body, err
}

// cleanDomains 去掉空白与空项。
func cleanDomains(domains []string) []string {
	cleaned := make([]string, 0, len(domains))
	for _, domain := range domains {
		if domain = strings.TrimSpace(domain); domain != "" {
			cleaned = append(cleaned, domain)
		}
	}
	return cleaned
}

// withSites 把 domains 拼成搜索引擎通用的 "site:" 语法（brave / duckduckgo 用）。
func withSites(query string, domains []string) string {
	if len(domains) == 0 {
		return query
	}
	sites := make([]string, 0, len(domains))
	for _, domain := range domains {
		sites = append(sites, "site:"+domain)
	}
	return strings.TrimSpace(query + " " + strings.Join(sites, " "))
}

// normalizeResultURL 补全搜索结果里缺 scheme 的 URL。
//
// 搜索引擎偶尔返回 "investor.nvidia.com/q3" 或 "//example.com/a" 这类相对形式，
// 直接交给 WebFetch 会被 validateHTTPURL 以"非 http/https"拒掉，所以在这里统一
// 补成 https://。返回空串表示该条目没有可用链接。
func normalizeResultURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return raw
	case strings.HasPrefix(raw, "//"):
		return "https:" + raw
	default:
		return "https://" + raw
	}
}

// tinyFishSearchProvider 走 TinyFish 的语义检索 API。
type tinyFishSearchProvider struct{}

func (tinyFishSearchProvider) Name() string         { return types.WebProviderTinyFish }
func (tinyFishSearchProvider) RequiresAPIKey() bool { return true }

func (p tinyFishSearchProvider) Search(ctx context.Context, client *http.Client, config WebSearchConfig, request searchRequest) ([]WebSearchResult, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid web search endpoint: %w", err)
	}
	values := endpoint.Query()
	values.Set("query", request.Query)
	if len(request.Domains) > 0 {
		// 站点过滤走专用参数：该 API 的 query 是语义检索，拼 site: 会污染语义。
		values.Set("include_domains", strings.Join(request.Domains, ","))
	}
	if request.RecencyDays > 0 {
		// 入参是天，该 API 只收分钟。
		values.Set("recency_minutes", strconv.Itoa(request.RecencyDays*24*60))
	}
	if config.Location != "" {
		values.Set("location", config.Location)
	}
	if config.Language != "" {
		values.Set("language", config.Language)
	}
	// 注意：该 API 没有 max_results 参数，条数在客户端截断。
	endpoint.RawQuery = values.Encode()

	body, err := doSearchRequest(ctx, client, endpoint.String(),
		map[string]string{"X-API-Key": config.APIKey}, config.MaxResponseBytes, config.TimeoutMs)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode %s search response: %w", p.Name(), err)
	}
	results := make([]WebSearchResult, 0, len(payload.Results))
	for _, item := range payload.Results {
		normalized := normalizeResultURL(item.URL)
		if normalized == "" {
			continue
		}
		results = append(results, WebSearchResult{Title: item.Title, URL: normalized, Snippet: item.Snippet})
	}
	return results, nil
}

// braveSearchProvider 走 Brave Search API。
type braveSearchProvider struct{}

func (braveSearchProvider) Name() string         { return types.WebProviderBrave }
func (braveSearchProvider) RequiresAPIKey() bool { return true }

func (p braveSearchProvider) Search(ctx context.Context, client *http.Client, config WebSearchConfig, request searchRequest) ([]WebSearchResult, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid web search endpoint: %w", err)
	}
	values := endpoint.Query()
	values.Set("q", withSites(request.Query, request.Domains))
	values.Set("count", strconv.Itoa(clamp(request.MaxResults, 1, 20)))
	if request.RecencyDays > 0 {
		values.Set("freshness", fmt.Sprintf("%dd", request.RecencyDays))
	}
	// 只发 brave 自己的参数。format / no_html / no_redirect / skip_disambig 是
	// duckduckgo 那套，照抄过来纯属噪音（还有时候会干扰结果）。
	endpoint.RawQuery = values.Encode()

	body, err := doSearchRequest(ctx, client, endpoint.String(),
		map[string]string{"X-Subscription-Token": config.APIKey}, config.MaxResponseBytes, config.TimeoutMs)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode %s search response: %w", p.Name(), err)
	}
	results := make([]WebSearchResult, 0, len(payload.Web.Results))
	for _, item := range payload.Web.Results {
		normalized := normalizeResultURL(item.URL)
		if normalized == "" {
			continue
		}
		results = append(results, WebSearchResult{Title: item.Title, URL: normalized, Snippet: item.Description})
	}
	return results, nil
}

// duckDuckGoTopic 是 Instant Answer API 的一个相关词条，Topics 为二级分组。
type duckDuckGoTopic struct {
	Text     string `json:"Text"`
	FirstURL string `json:"FirstURL"`
	Topics   []struct {
		Text     string `json:"Text"`
		FirstURL string `json:"FirstURL"`
	} `json:"Topics"`
}

// duckDuckGoSearchProvider 走 DuckDuckGo Instant Answer API，免 key。
type duckDuckGoSearchProvider struct{}

func (duckDuckGoSearchProvider) Name() string         { return types.WebProviderDuckDuckGo }
func (duckDuckGoSearchProvider) RequiresAPIKey() bool { return false }

func (p duckDuckGoSearchProvider) Search(ctx context.Context, client *http.Client, config WebSearchConfig, request searchRequest) ([]WebSearchResult, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid web search endpoint: %w", err)
	}
	values := endpoint.Query()
	values.Set("q", withSites(request.Query, request.Domains))
	values.Set("format", "json")
	values.Set("no_html", "1")
	values.Set("no_redirect", "1")
	values.Set("skip_disambig", "1")
	endpoint.RawQuery = values.Encode()

	body, err := doSearchRequest(ctx, client, endpoint.String(), nil, config.MaxResponseBytes, config.TimeoutMs)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Heading       string            `json:"Heading"`
		AbstractText  string            `json:"AbstractText"`
		AbstractURL   string            `json:"AbstractURL"`
		RelatedTopics []duckDuckGoTopic `json:"RelatedTopics"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode %s search response: %w", p.Name(), err)
	}
	results := make([]WebSearchResult, 0, len(payload.RelatedTopics)+1)
	if normalized := normalizeResultURL(payload.AbstractURL); normalized != "" {
		results = append(results, WebSearchResult{Title: payload.Heading, URL: normalized, Snippet: payload.AbstractText})
	}
	for _, topic := range payload.RelatedTopics {
		results = appendTopic(results, topic)
	}
	return results, nil
}

// appendTopic 展开一个词条，含它的二级分组。
func appendTopic(results []WebSearchResult, topic duckDuckGoTopic) []WebSearchResult {
	if normalized := normalizeResultURL(topic.FirstURL); normalized != "" {
		results = append(results, WebSearchResult{Title: topic.Text, URL: normalized})
	}
	for _, nested := range topic.Topics {
		if normalized := normalizeResultURL(nested.FirstURL); normalized != "" {
			results = append(results, WebSearchResult{Title: nested.Text, URL: normalized})
		}
	}
	return results
}

// tavilySearchProvider 走 Tavily Search API：POST + Bearer 认证。
type tavilySearchProvider struct{}

func (tavilySearchProvider) Name() string         { return types.WebProviderTavily }
func (tavilySearchProvider) RequiresAPIKey() bool { return true }

func (p tavilySearchProvider) Search(ctx context.Context, client *http.Client, config WebSearchConfig, request searchRequest) ([]WebSearchResult, error) {
	payload := map[string]any{
		"query": request.Query,
		// topic / search_depth / include_* 固定下发：官方默认会带 raw_content
		// 和 answer，体积大且我们不用，显式关掉。
		"topic":               "general",
		"search_depth":        "basic",
		"include_raw_content": false,
		"include_answer":      false,
	}
	// 该 API 原生支持 max_results，>0 时直接下发；条数仍在客户端兜底截断。
	if request.MaxResults > 0 {
		payload["max_results"] = request.MaxResults
	}
	if len(request.Domains) > 0 {
		payload["include_domains"] = request.Domains
	}
	if request.RecencyDays > 0 {
		payload["time_range"] = tavilyTimeRange(request.RecencyDays)
	}
	body, err := doPostJSONRequest(ctx, client, config.Endpoint,
		// 注意：tavily 用 Authorization: Bearer，不是 tinyfish 的 X-API-Key、
		// 也不是 brave 的 X-Subscription-Token。
		map[string]string{"Authorization": "Bearer " + config.APIKey},
		payload, config.MaxResponseBytes, config.TimeoutMs)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			// 摘要字段是 content，不是 snippet。
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode %s search response: %w", p.Name(), err)
	}
	results := make([]WebSearchResult, 0, len(decoded.Results))
	for _, item := range decoded.Results {
		normalized := normalizeResultURL(item.URL)
		if normalized == "" {
			continue
		}
		results = append(results, WebSearchResult{Title: item.Title, URL: normalized, Snippet: item.Content})
	}
	return results, nil
}

// tavilyTimeRange 把 recency_days 映射到 tavily 的 time_range。
//
// 这是有损映射：tavily 只支持 day / week / month / year 四档，不支持精确天数，
// 所以 31 天以内的请求都会映射到更宽的窗口（结果只会更多，不会漏）。
func tavilyTimeRange(days int) string {
	switch {
	case days <= 1:
		return "day"
	case days <= 7:
		return "week"
	case days <= 31:
		return "month"
	default:
		return "year"
	}
}

// WebFetchConfig 是 WebFetch 工具的数据源配置。
type WebFetchConfig struct {
	// Provider 为 tinyfish | tavily | direct；空 = duckduckgo。brave /
	// duckduckgo 不提供 fetch API，等价于 direct。
	Provider string
	// Endpoint 为完整 endpoint；空 = 取 provider 内置默认。
	Endpoint string
	// APIKey 为 tinyfish / tavily 必填，缺失时 fail-closed。
	APIKey string
	// Format 为 markdown | html | json；空（未走构造函数）= 不校验。
	Format string
	// AllowPrivateHosts 为 true 时放开私网 / localhost 目标。
	AllowPrivateHosts bool
	// MaxResponseBytes 为响应体上限；空 = 256KB。
	MaxResponseBytes int
	// MaxRedirects 为本地直连时允许的最大跳转次数。
	MaxRedirects int
	// TimeoutMs 为单次请求超时；空 = 45000。
	TimeoutMs int
}

type WebFetchTool struct {
	client *http.Client
	config WebFetchConfig
}

func NewWebFetchTool(client *http.Client, config WebFetchConfig) *WebFetchTool {
	if client == nil {
		client = http.DefaultClient
	}
	config.Provider = strings.ToLower(strings.TrimSpace(config.Provider))
	if config.Provider == "" {
		// 默认跟随 search：duckduckgo 免 key。它没有 fetch API，
		// DefaultWebFetchURL 返回空、newFetchProvider 落成 direct（本地直连），
		// 所以默认 fetch 依然可用，不会报 unknown provider。
		config.Provider = types.WebProviderDuckDuckGo
	}
	if strings.TrimSpace(config.Endpoint) == "" {
		config.Endpoint = types.DefaultWebFetchURL(config.Provider)
	}
	if strings.TrimSpace(config.Format) == "" {
		config.Format = webFetchFormatMarkdown
	}
	if config.MaxResponseBytes <= 0 {
		config.MaxResponseBytes = defaultWebMaxBytes
	}
	if config.MaxRedirects <= 0 {
		config.MaxRedirects = defaultWebFetchMaxRedirects
	}
	if config.TimeoutMs <= 0 {
		config.TimeoutMs = defaultWebFetchTimeoutMs
	}
	return &WebFetchTool{client: client, config: config}
}

func (t *WebFetchTool) Name() string { return "WebFetch" }
func (t *WebFetchTool) Description() string {
	return "Fetch and extract readable content from a web URL"
}
func (t *WebFetchTool) NeedsApproval() bool { return false }
func (t *WebFetchTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolReadOnly}
}
func (t *WebFetchTool) Parameters() map[string]any {
	return map[string]any{
		"url":       map[string]any{"type": "string", "description": "HTTP or HTTPS URL", "required": true},
		"max_bytes": map[string]any{"type": "integer", "description": "Maximum response bytes to return"},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rawURL := strings.TrimSpace(stringParam(params, "url"))
	if rawURL == "" {
		return &types.ToolResult{Success: false, Error: "url parameter is required"}, nil
	}
	provider, err := newFetchProvider(t.config.Provider)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	// 私网 / localhost 目标不外发给第三方 provider：既抓不到（对方出网访问不到
	// 我们的内网），也会把内网地址泄漏给第三方，统一降级为本地直连。
	if isPrivateURL(ctx, rawURL) {
		provider = directFetchProvider{}
	}
	// 降级只决定用哪个 provider，私网是否可抓仍由 AllowPrivateHosts 说了算。
	parsed, err := validateHTTPURL(ctx, rawURL, t.config.AllowPrivateHosts)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	if provider.RequiresAPIKey() && strings.TrimSpace(t.config.APIKey) == "" {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("%s fetch requires an API key: set web.api_key in settings.json", provider.Name()),
		}, nil
	}
	if err := validateFetchFormat(t.config.Format); err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	maxBytes := intParam(params, "max_bytes")
	if maxBytes <= 0 {
		maxBytes = t.config.MaxResponseBytes
	}
	outcome, err := provider.Fetch(ctx, t.client, t.config, fetchRequest{
		Target:       parsed.String(),
		MaxBytes:     maxBytes,
		AllowPrivate: t.config.AllowPrivateHosts,
	})
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	metadata := map[string]any{
		"provider":  provider.Name(),
		"url":       outcome.URL,
		"truncated": outcome.Truncated,
	}
	if outcome.StatusCode > 0 {
		metadata["status_code"] = outcome.StatusCode
	}
	if outcome.ContentType != "" {
		metadata["content_type"] = outcome.ContentType
	}
	return &types.ToolResult{Success: true, Content: outcome.Content, Metadata: metadata}, nil
}

// fetchRequest 是一次抓取的归一化入参。
type fetchRequest struct {
	// Target 是已通过 validateHTTPURL 的绝对 URL。
	Target string
	// MaxBytes 是返回正文的字节上限。
	MaxBytes int
	// AllowPrivate 表示跳转目标也允许是私网地址。
	AllowPrivate bool
}

// fetchOutcome 是抓取后的归一化结果，屏蔽掉"远程 API"与"本地直连"的差异。
type fetchOutcome struct {
	Content string
	// URL 是实际抓到的地址，跟随跳转后可能与请求不同。
	URL string
	// StatusCode / ContentType 只有本地直连能给全，远程 provider 可留空。
	StatusCode  int
	ContentType string
	Truncated   bool
}

// FetchProvider 抽象一个抓取数据源。
type FetchProvider interface {
	Name() string
	// RequiresAPIKey 为 true 时，无 APIKey 直接失败，不发请求。
	RequiresAPIKey() bool
	Fetch(ctx context.Context, client *http.Client, config WebFetchConfig, request fetchRequest) (fetchOutcome, error)
}

func newFetchProvider(name string) (FetchProvider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case types.WebProviderTinyFish:
		return tinyFishFetchProvider{}, nil
	case types.WebProviderTavily:
		return tavilyFetchProvider{}, nil
	case types.WebProviderDirect, types.WebProviderBrave, types.WebProviderDuckDuckGo:
		// brave / duckduckgo 没有 fetch API（DefaultWebFetchURL 为空），
		// 本地直连是它们唯一可用的抓取方式。
		return directFetchProvider{}, nil
	default:
		return nil, fmt.Errorf("unknown web fetch provider %q", name)
	}
}

// validateFetchFormat 校验抓取格式。空串表示未配置（不走构造函数时的零值），
// 交给 provider 自己按默认处理，不在这里拦。
func validateFetchFormat(format string) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "markdown", "html", "json":
		return nil
	default:
		return fmt.Errorf("unsupported web fetch format %q: use markdown, html or json", format)
	}
}

// directFetchProvider 由引擎自己发起 HTTP GET，不经任何外部 API。
type directFetchProvider struct{}

func (directFetchProvider) Name() string         { return types.WebProviderDirect }
func (directFetchProvider) RequiresAPIKey() bool { return false }

func (p directFetchProvider) Fetch(ctx context.Context, client *http.Client, config WebFetchConfig, request fetchRequest) (fetchOutcome, error) {
	ctx, cancel := withWebTimeout(ctx, config.TimeoutMs)
	defer cancel()
	// 每次抓取独立设置跳转策略，避免污染调用方传入的 client。
	httpClient := *client
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > config.MaxRedirects {
			return errors.New("too many redirects")
		}
		// 跳转目标重新校验，防止公网 URL 跳到内网地址（SSRF）。
		_, err := validateHTTPURL(req.Context(), req.URL.String(), request.AllowPrivate)
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, request.Target, nil)
	if err != nil {
		return fetchOutcome{}, err
	}
	req.Header.Set("Accept", "text/html, text/plain, application/json")
	req.Header.Set("User-Agent", "Heron-AI/1.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fetchOutcome{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fetchOutcome{}, fmt.Errorf("web fetch returned status %d", resp.StatusCode)
	}
	body, truncated, err := readLimitedBody(resp.Body, request.MaxBytes)
	if err != nil {
		return fetchOutcome{}, err
	}
	contentType := resp.Header.Get("Content-Type")
	content := string(body)
	if strings.Contains(strings.ToLower(contentType), "html") {
		content = extractHTMLText(content)
	}
	return fetchOutcome{
		Content:     content,
		URL:         resp.Request.URL.String(),
		StatusCode:  resp.StatusCode,
		ContentType: contentType,
		Truncated:   truncated,
	}, nil
}

// tinyFishFetchProvider 走 TinyFish 的批量抓取 API：POST 一组 URL，取回正文。
type tinyFishFetchProvider struct{}

func (tinyFishFetchProvider) Name() string         { return types.WebProviderTinyFish }
func (tinyFishFetchProvider) RequiresAPIKey() bool { return true }

func (p tinyFishFetchProvider) Fetch(ctx context.Context, client *http.Client, config WebFetchConfig, request fetchRequest) (fetchOutcome, error) {
	ctx, cancel := withWebTimeout(ctx, config.TimeoutMs)
	defer cancel()
	payload := map[string]any{
		"urls":               []string{request.Target},
		"format":             config.Format,
		"per_url_timeout_ms": config.TimeoutMs,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fetchOutcome{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.Endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fetchOutcome{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Heron-AI/1.0")
	req.Header.Set("X-API-Key", config.APIKey)
	resp, err := client.Do(req)
	if err != nil {
		return fetchOutcome{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fetchOutcome{}, fmt.Errorf("web fetch returned status %d", resp.StatusCode)
	}
	body, _, err := readLimitedBody(resp.Body, request.MaxBytes+tinyFishEnvelopeSlack)
	if err != nil {
		return fetchOutcome{}, err
	}
	var decoded struct {
		Results []struct {
			URL    string `json:"url"`
			Text   string `json:"text"`
			Status int    `json:"status"`
		} `json:"results"`
		Errors []struct {
			URL   string `json:"url"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fetchOutcome{}, fmt.Errorf("decode %s fetch response: %w", p.Name(), err)
	}
	// 该 API 是 200 携带 per-URL 错误（超时 / DNS / 反爬）：errors 非空必须判失败，
	// 否则会静默返回空正文，让 agent 以为页面真的是空的。
	if len(decoded.Errors) > 0 {
		messages := make([]string, 0, len(decoded.Errors))
		for _, item := range decoded.Errors {
			text := strings.TrimSpace(item.Error)
			if text == "" {
				text = "unknown error"
			}
			if item.URL != "" {
				messages = append(messages, fmt.Sprintf("%s: %s", item.URL, text))
			} else {
				messages = append(messages, text)
			}
		}
		return fetchOutcome{}, fmt.Errorf("web fetch failed: %s", strings.Join(messages, "; "))
	}
	if len(decoded.Results) == 0 {
		return fetchOutcome{}, fmt.Errorf("web fetch returned no content for %s", request.Target)
	}
	content := decoded.Results[0].Text
	truncated := false
	if request.MaxBytes > 0 && len(content) > request.MaxBytes {
		content = content[:request.MaxBytes]
		truncated = true
	}
	statusCode := decoded.Results[0].Status
	if statusCode == 0 {
		statusCode = resp.StatusCode
	}
	return fetchOutcome{
		Content:    content,
		URL:        request.Target,
		StatusCode: statusCode,
		Truncated:  truncated,
	}, nil
}

// tavilyFetchProvider 走 Tavily Extract API：POST 一组 URL，取回正文。
type tavilyFetchProvider struct{}

func (tavilyFetchProvider) Name() string         { return types.WebProviderTavily }
func (tavilyFetchProvider) RequiresAPIKey() bool { return true }

func (p tavilyFetchProvider) Fetch(ctx context.Context, client *http.Client, config WebFetchConfig, request fetchRequest) (fetchOutcome, error) {
	// tavily extract 只输出 markdown / text。html / json 明确报错，不静默改成
	// markdown——否则用户以为拿到了 HTML 源码，实际是转换后的正文。
	var format string
	switch strings.ToLower(strings.TrimSpace(config.Format)) {
	case "", webFetchFormatMarkdown:
		format = webFetchFormatMarkdown
	case "text":
		format = "text"
	default:
		return fetchOutcome{}, fmt.Errorf("tavily fetch does not support format %q: use markdown or text", config.Format)
	}

	payload := map[string]any{
		"urls":          []string{request.Target},
		"format":        format,
		"extract_depth": "basic",
	}
	body, err := doPostJSONRequest(ctx, client, config.Endpoint,
		map[string]string{"Authorization": "Bearer " + config.APIKey},
		payload, request.MaxBytes+tavilyEnvelopeSlack, config.TimeoutMs)
	if err != nil {
		return fetchOutcome{}, err
	}
	var decoded struct {
		Results []struct {
			URL        string `json:"url"`
			RawContent string `json:"raw_content"`
		} `json:"results"`
		FailedResults []struct {
			URL   string `json:"url"`
			Error string `json:"error"`
		} `json:"failed_results"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fetchOutcome{}, fmt.Errorf("decode %s fetch response: %w", p.Name(), err)
	}
	// 该 API 是 200 携带 per-URL 失败：failed_results 非空必须判失败，否则会
	// 静默返回空正文，让 agent 以为页面真的是空的。
	if len(decoded.FailedResults) > 0 {
		messages := make([]string, 0, len(decoded.FailedResults))
		for _, item := range decoded.FailedResults {
			text := strings.TrimSpace(item.Error)
			if text == "" {
				text = "unknown error"
			}
			if item.URL != "" {
				messages = append(messages, fmt.Sprintf("%s: %s", item.URL, text))
			} else {
				messages = append(messages, text)
			}
		}
		return fetchOutcome{}, fmt.Errorf("web fetch failed: %s", strings.Join(messages, "; "))
	}
	if len(decoded.Results) == 0 {
		return fetchOutcome{}, fmt.Errorf("web fetch returned no content for %s", request.Target)
	}
	content := decoded.Results[0].RawContent
	truncated := false
	if request.MaxBytes > 0 && len(content) > request.MaxBytes {
		content = content[:request.MaxBytes]
		truncated = true
	}
	return fetchOutcome{
		Content:   content,
		URL:       request.Target,
		Truncated: truncated,
	}, nil
}

func readLimitedBody(reader io.Reader, maxBytes int) ([]byte, bool, error) {
	if maxBytes <= 0 {
		maxBytes = defaultWebMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(reader, int64(maxBytes)+1))
	if err != nil {
		return nil, false, err
	}
	truncated := len(body) > maxBytes
	if truncated {
		body = body[:maxBytes]
	}
	return body, truncated, nil
}

func validateHTTPURL(ctx context.Context, rawURL string, allowPrivate bool) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("only http and https URLs are supported")
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("URL must contain a host and no user credentials")
	}
	if allowPrivate {
		return parsed, nil
	}
	if isBlockedHost(ctx, parsed.Hostname()) {
		return nil, errors.New("private, local, or metadata hosts are not allowed")
	}
	return parsed, nil
}

// isPrivateURL 判断目标是否是私网 / 本机地址。与 AllowPrivateHosts 无关：
// 即便放开私网抓取，这类目标也不该发给第三方 provider。
func isPrivateURL(ctx context.Context, rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	return isBlockedHost(ctx, host)
}

func isBlockedHost(ctx context.Context, host string) bool {
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
		return true
	}
	if ip := net.ParseIP(lower); ip != nil {
		return isPrivateIP(ip)
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", lower)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return true
		}
	}
	return false
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

var htmlTagPattern = regexp.MustCompile(`(?s)<[^>]*>`)
var htmlNoisePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`),
	regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`),
	regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript>`),
	regexp.MustCompile(`(?is)<template[^>]*>.*?</template>`),
}
var whitespacePattern = regexp.MustCompile(`[ \t\r\n]+`)

func extractHTMLText(source string) string {
	for _, pattern := range htmlNoisePatterns {
		source = pattern.ReplaceAllString(source, " ")
	}
	source = strings.ReplaceAll(source, "</p>", "\n")
	source = strings.ReplaceAll(source, "</div>", "\n")
	source = strings.ReplaceAll(source, "<br>", "\n")
	source = strings.ReplaceAll(source, "<br/>", "\n")
	source = htmlTagPattern.ReplaceAllString(source, " ")
	source = html.UnescapeString(source)
	return strings.TrimSpace(whitespacePattern.ReplaceAllString(source, " "))
}
