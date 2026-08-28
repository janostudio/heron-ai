package tool

import (
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
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

const (
	defaultWebSearchEndpoint   = "https://api.duckduckgo.com/"
	defaultBraveSearchEndpoint = "https://api.search.brave.com/res/v1/web/search"
	defaultWebMaxBytes         = 256 * 1024
)

type WebSearchConfig struct {
	Endpoint         string
	APIKey           string
	MaxResponseBytes int
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

type WebSearchTool struct {
	client *http.Client
	config WebSearchConfig
}

func NewWebSearchTool(client *http.Client, config WebSearchConfig) *WebSearchTool {
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(config.Endpoint) == "" {
		if strings.TrimSpace(config.APIKey) != "" {
			config.Endpoint = defaultBraveSearchEndpoint
		} else {
			config.Endpoint = defaultWebSearchEndpoint
		}
	}
	if config.MaxResponseBytes <= 0 {
		config.MaxResponseBytes = defaultWebMaxBytes
	}
	return &WebSearchTool{client: client, config: config}
}

// NewWebSearchToolWithEndpoint is a convenience constructor for tests and
// callers that only need to override the endpoint.
func NewWebSearchToolWithEndpoint(client *http.Client, endpoint string) *WebSearchTool {
	return NewWebSearchTool(client, WebSearchConfig{Endpoint: endpoint})
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
	domains := stringSliceParam(params, "domains")
	if len(domains) > 0 && strings.Contains(t.config.Endpoint, "duckduckgo.com") {
		sites := make([]string, 0, len(domains))
		for _, domain := range domains {
			domain = strings.TrimSpace(domain)
			if domain != "" {
				sites = append(sites, "site:"+domain)
			}
		}
		if len(sites) > 0 {
			query += " " + strings.Join(sites, " ")
		}
	}

	requestURL, err := url.Parse(t.config.Endpoint)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	values := requestURL.Query()
	values.Set("q", query)
	values.Set("format", "json")
	values.Set("no_html", "1")
	values.Set("no_redirect", "1")
	values.Set("skip_disambig", "1")
	if max := intParam(params, "max_results"); max > 0 {
		values.Set("count", fmt.Sprintf("%d", clamp(max, 1, 20)))
	}
	if recency := intParam(params, "recency_days"); recency > 0 {
		values.Set("freshness", fmt.Sprintf("%dd", recency))
	}
	requestURL.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Heron-AI/1.0")
	if t.config.APIKey != "" {
		req.Header.Set("X-Subscription-Token", t.config.APIKey)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("web search returned status %d", resp.StatusCode)}, nil
	}
	body, truncated, err := readLimitedBody(resp.Body, t.config.MaxResponseBytes)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	results, provider, err := parseSearchResponse(body)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	maxResults := intParam(params, "max_results")
	if maxResults <= 0 {
		maxResults = 10
	}
	if len(results) > maxResults {
		results = results[:maxResults]
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
			"provider":  provider,
			"query":     query,
			"count":     len(results),
			"truncated": truncated,
		},
	}, nil
}

type WebFetchConfig struct {
	AllowPrivateHosts bool
	MaxResponseBytes  int
	MaxRedirects      int
}

type WebFetchTool struct {
	client *http.Client
	config WebFetchConfig
}

func NewWebFetchTool(client *http.Client, config WebFetchConfig) *WebFetchTool {
	if client == nil {
		client = http.DefaultClient
	}
	if config.MaxResponseBytes <= 0 {
		config.MaxResponseBytes = defaultWebMaxBytes
	}
	if config.MaxRedirects <= 0 {
		config.MaxRedirects = 5
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
	parsed, err := validateHTTPURL(ctx, rawURL, t.config.AllowPrivateHosts)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	maxBytes := intParam(params, "max_bytes")
	if maxBytes <= 0 {
		maxBytes = t.config.MaxResponseBytes
	}
	client := *t.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > t.config.MaxRedirects {
			return errors.New("too many redirects")
		}
		_, err := validateHTTPURL(req.Context(), req.URL.String(), t.config.AllowPrivateHosts)
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	req.Header.Set("Accept", "text/html, text/plain, application/json")
	req.Header.Set("User-Agent", "Heron-AI/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("web fetch returned status %d", resp.StatusCode)}, nil
	}
	body, truncated, err := readLimitedBody(resp.Body, maxBytes)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	contentType := resp.Header.Get("Content-Type")
	content := string(body)
	if strings.Contains(strings.ToLower(contentType), "html") {
		content = extractHTMLText(content)
	}
	return &types.ToolResult{
		Success: true,
		Content: content,
		Metadata: map[string]any{
			"url":          resp.Request.URL.String(),
			"status_code":  resp.StatusCode,
			"content_type": contentType,
			"truncated":    truncated,
		},
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

func parseSearchResponse(body []byte) ([]WebSearchResult, string, error) {
	var response struct {
		Heading       string `json:"Heading"`
		AbstractText  string `json:"AbstractText"`
		AbstractURL   string `json:"AbstractURL"`
		RelatedTopics []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
			Topics   []struct {
				Text     string `json:"Text"`
				FirstURL string `json:"FirstURL"`
			} `json:"Topics"`
		} `json:"RelatedTopics"`
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		var generic struct {
			Results []WebSearchResult `json:"results"`
		}
		if genericErr := json.Unmarshal(body, &generic); genericErr != nil {
			return nil, "", fmt.Errorf("decode web search response: %w", err)
		}
		return generic.Results, "generic", nil
	}
	results := make([]WebSearchResult, 0)
	provider := "duckduckgo"
	if response.Web.Results != nil {
		provider = "brave"
		for _, result := range response.Web.Results {
			results = append(results, WebSearchResult{Title: result.Title, URL: result.URL, Snippet: result.Description})
		}
	}
	if response.AbstractURL != "" {
		results = append(results, WebSearchResult{Title: response.Heading, URL: response.AbstractURL, Snippet: response.AbstractText})
	}
	var flatten func([]struct {
		Text     string `json:"Text"`
		FirstURL string `json:"FirstURL"`
		Topics   []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
		} `json:"Topics"`
	})
	flatten = func(items []struct {
		Text     string `json:"Text"`
		FirstURL string `json:"FirstURL"`
		Topics   []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
		} `json:"Topics"`
	}) {
		for _, item := range items {
			if item.FirstURL != "" {
				results = append(results, WebSearchResult{Title: item.Text, URL: item.FirstURL})
			}
			if len(item.Topics) > 0 {
				for _, nested := range item.Topics {
					if nested.FirstURL != "" {
						results = append(results, WebSearchResult{Title: nested.Text, URL: nested.FirstURL})
					}
				}
			}
		}
	}
	flatten(response.RelatedTopics)
	return results, provider, nil
}
