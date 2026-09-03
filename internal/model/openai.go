package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// OpenAIProvider speaks the OpenAI Chat Completions wire format. It is also
// used for OpenAI-compatible gateways, including the local Tencent endpoint.
// ModelProfile is kept here so model defaults are applied centrally instead
// of being repeated in every Agent file.
type OpenAIProvider struct {
	client    *http.Client
	apiKey    string
	baseURL   string
	modelName string
	profile   types.ModelProfile
	media     types.MediaResolver
}

func NewOpenAIProvider(apiKey, baseURL, modelName string) *OpenAIProvider {
	return NewOpenAIProviderWithProfile(apiKey, baseURL, modelName, types.ModelProfile{
		ID:      modelName,
		Name:    modelName,
		BaseURL: baseURL,
		APIKey:  apiKey,
	})
}

func NewOpenAIProviderWithProfile(apiKey, baseURL, modelName string, profile types.ModelProfile) *OpenAIProvider {
	if profile.Name == "" {
		profile.Name = modelName
	}
	if profile.ID == "" {
		profile.ID = profile.Name
	}
	if profile.BaseURL == "" {
		profile.BaseURL = baseURL
	}
	if profile.APIKey == "" {
		profile.APIKey = apiKey
	}
	return &OpenAIProvider{
		client:    http.DefaultClient,
		apiKey:    apiKey,
		baseURL:   strings.TrimRight(baseURL, "/"),
		modelName: modelName,
		profile:   profile,
	}
}

func (p *OpenAIProvider) SetMediaResolver(resolver types.MediaResolver) {
	p.media = resolver
}

type openAIRequest struct {
	Model               string          `json:"model"`
	Messages            []openAIMessage `json:"messages"`
	Tools               []openAITool    `json:"tools,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	TopK                *int            `json:"top_k,omitempty"`
	RepetitionPenalty   *float64        `json:"repetition_penalty,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	ResponseFormat      map[string]any  `json:"response_format,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIContentPart struct {
	Type     string             `json:"type"`
	Text     string             `json:"text,omitempty"`
	ImageURL *openAIImageURL    `json:"image_url,omitempty"`
	File     *openAIFileContent `json:"file,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

type openAIFileContent struct {
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data,omitempty"`
}

type openAITool struct {
	Type     string            `json:"type"`
	Function openAIFunctionDef `json:"function"`
}

type openAIFunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content          string           `json:"content"`
			ReasoningContent string           `json:"reasoning_content"`
			ToolCalls        []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens             int `json:"prompt_tokens"`
		CompletionTokens         int `json:"completion_tokens"`
		TotalTokens              int `json:"total_tokens"`
		PromptCacheHitTokens     int `json:"prompt_cache_hit_tokens"`
		PromptCacheMissTokens    int `json:"prompt_cache_miss_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		PromptTokensDetails      *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// openAIStreamToolCall mirrors the wire delta.tool_calls entries during
// streaming. Unlike the non-streaming openAIToolCall, streaming fragments are
// identified by an `index` (stable across chunks) rather than an id, which only
// appears on the first fragment.
type openAIStreamToolCall struct {
	Index    int                `json:"index"`
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string                 `json:"content"`
			ReasoningContent string                 `json:"reasoning_content"`
			ToolCalls        []openAIStreamToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func (p *OpenAIProvider) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	modelName := p.modelName
	if config.Model != "" {
		modelName = config.Model
	}
	if id := requestModelName(p.profile); id != "" {
		modelName = id
	}
	effective := MergeProfileDefaults(p.profile, config)
	req, err := p.buildRequest(ctx, modelName, messages, tools, config, effective, p.profile.Stream)
	if err != nil {
		return nil, err
	}

	if p.profile.Stream {
		wire, err := p.postStream(ctx, effective, req)
		if err != nil {
			return nil, fmt.Errorf("openai chat: %w", err)
		}
		return withModel(convertOpenAIResponse(*wire), modelName), nil
	}

	var wire openAIResponse
	if err := p.post(ctx, effective, req, &wire); err != nil {
		// response_format is an optional optimization. The structured-output
		// validator remains authoritative when a compatible gateway omits it.
		if len(req.ResponseFormat) > 0 {
			req.ResponseFormat = nil
			if retryErr := p.post(ctx, effective, req, &wire); retryErr == nil {
				return withModel(convertOpenAIResponse(wire), modelName), nil
			} else {
				err = retryErr
			}
		}
		if isUnsupportedParameterError(err) && hasOptionalGenerationParameters(req) {
			req = withoutOptionalGenerationParameters(req)
			if retryErr := p.post(ctx, effective, req, &wire); retryErr == nil {
				return withModel(convertOpenAIResponse(wire), modelName), nil
			} else {
				err = retryErr
			}
		}
		return nil, fmt.Errorf("openai chat: %w", err)
	}
	return withModel(convertOpenAIResponse(wire), modelName), nil
}

// ChatStream keeps the ModelProvider contract for callers that want a stream.
// The V1 Agent loop uses Chat because tool calls and structured output need the
// complete response. Returning one completed chunk here keeps the provider
// compatible with both execution modes without duplicating protocol logic.
func (p *OpenAIProvider) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	resp, err := p.Chat(ctx, messages, tools, config)
	if err != nil {
		return nil, err
	}
	ch := make(chan types.ChatChunk, 1)
	ch <- types.ChatChunk{Text: resp.Text, Reasoning: resp.Reasoning, Finished: true}
	close(ch)
	return ch, nil
}

func (p *OpenAIProvider) buildRequest(
	ctx context.Context,
	modelName string,
	messages []types.Message,
	tools []types.JSONSchema,
	override types.ModelConfig,
	effective types.ModelConfig,
	stream bool,
) (openAIRequest, error) {
	converted, err := p.convertMessages(ctx, messages)
	if err != nil {
		return openAIRequest{}, err
	}
	req := openAIRequest{
		Model:    modelName,
		Messages: converted,
		Stream:   stream,
	}

	if profileAllowsToolCalls(p.profile, len(tools) > 0) {
		req.Tools = convertOpenAITools(tools)
	}
	if schema := responseFormat(effective.ResponseFormat); schema != nil && profileAllowsStructuredOutput(p.profile) {
		req.ResponseFormat = schema
	}
	if limit := effective.OutputTokenLimit(); limit != nil && *limit > 0 {
		req.MaxCompletionTokens = limit
	}
	if generationAllowed(override.Temperature != nil, effective.Temperature, p.profile.SupportsTemperature) {
		req.Temperature = effective.Temperature
	}
	if generationAllowed(override.TopP != nil, effective.TopP, p.profile.SupportsTopP) {
		req.TopP = effective.TopP
	}
	if generationAllowed(override.TopK != nil, effective.TopK, p.profile.SupportsTopK) {
		req.TopK = effective.TopK
	}
	if generationAllowed(override.RepetitionPenalty != nil, effective.RepetitionPenalty, p.profile.SupportsRepetitionPenalty) {
		req.RepetitionPenalty = effective.RepetitionPenalty
	}
	if effective.Reasoning != nil && profileAllowsReasoning(p.profile, override.Reasoning != nil) {
		req.ReasoningEffort = effective.Reasoning.Effort
		if req.ReasoningEffort == "" {
			switch strings.ToLower(strings.TrimSpace(effective.Reasoning.Type)) {
			case "none", "disabled", "off":
				// OpenAI-compatible gateways commonly inherit the model
				// registry's reasoning default when this field is omitted.
				// Send an explicit "none" for small structured routing calls.
				req.ReasoningEffort = "none"
			}
		}
	}
	return req, nil
}

func (p *OpenAIProvider) post(ctx context.Context, effective types.ModelConfig, req openAIRequest, output *openAIResponse) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	baseURL := p.baseURL
	if effective.BaseURL != "" {
		baseURL = strings.TrimRight(effective.BaseURL, "/")
	}
	url := baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	apiKey := p.apiKey
	if effective.APIKey != "" {
		apiKey = effective.APIKey
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if isTimeoutError(err) {
			return types.NewProviderTimeoutError(err)
		}
		return types.NewProviderNetworkError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if isTimeoutError(err) {
			return types.NewProviderTimeoutError(err)
		}
		return types.NewProviderNetworkError(err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var apiErr openAIErrorResponse
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
			pe := types.ClassifyProviderError(resp.StatusCode, apiErr.Error.Type, body)
			pe.Message = apiErr.Error.Type + ": " + apiErr.Error.Message
			return pe
		}
		return types.ClassifyProviderError(resp.StatusCode, "", body)
	}
	if err := json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// postStream issues a streaming chat completions request and aggregates the
// SSE chunks into a single openAIResponse. It mirrors post's URL/header/auth
// construction but reads the body as an event stream and reconstructs the
// message from per-chunk deltas (content, reasoning, and tool-call fragments).
func (p *OpenAIProvider) postStream(ctx context.Context, effective types.ModelConfig, req openAIRequest) (*openAIResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	baseURL := p.baseURL
	if effective.BaseURL != "" {
		baseURL = strings.TrimRight(effective.BaseURL, "/")
	}
	url := baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	apiKey := p.apiKey
	if effective.APIKey != "" {
		apiKey = effective.APIKey
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if isTimeoutError(err) {
			return nil, types.NewProviderTimeoutError(err)
		}
		return nil, types.NewProviderNetworkError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, types.NewProviderNetworkError(readErr)
		}
		var apiErr openAIErrorResponse
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
			pe := types.ClassifyProviderError(resp.StatusCode, apiErr.Error.Type, body)
			pe.Message = apiErr.Error.Type + ": " + apiErr.Error.Message
			return nil, pe
		}
		return nil, types.ClassifyProviderError(resp.StatusCode, "", body)
	}

	result := &openAIResponse{}
	var content, reasoning strings.Builder
	toolCalls := make(map[int]*openAIToolCall)
	var finishReason string

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			content.WriteString(delta.Content)
		}
		if delta.ReasoningContent != "" {
			reasoning.WriteString(delta.ReasoningContent)
		}
		for _, tc := range delta.ToolCalls {
			// Streaming tool-call fragments are keyed by their stable index;
			// the id only appears on the first fragment, and later fragments
			// carry just the incremental arguments (and often an empty name).
			call := toolCalls[tc.Index]
			if call == nil {
				call = &openAIToolCall{}
				toolCalls[tc.Index] = call
			}
			if call.ID == "" && tc.ID != "" {
				call.ID = tc.ID
			}
			if call.Type == "" && tc.Type != "" {
				call.Type = tc.Type
			}
			if call.Function.Name == "" && tc.Function.Name != "" {
				call.Function.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				call.Function.Arguments += tc.Function.Arguments
			}
		}
		if chunk.Choices[0].FinishReason != "" {
			finishReason = chunk.Choices[0].FinishReason
		}
		if chunk.Usage != nil {
			result.Usage.PromptTokens = chunk.Usage.PromptTokens
			result.Usage.CompletionTokens = chunk.Usage.CompletionTokens
			result.Usage.TotalTokens = chunk.Usage.TotalTokens
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, types.NewProviderNetworkError(err)
	}

	// Reconstruct tool calls in index order.
	var orderedToolCalls []openAIToolCall
	for i := 0; i < len(toolCalls); i++ {
		if call, ok := toolCalls[i]; ok {
			orderedToolCalls = append(orderedToolCalls, *call)
		}
	}

	result.Choices = []struct {
		Message struct {
			Content          string           `json:"content"`
			ReasoningContent string           `json:"reasoning_content"`
			ToolCalls        []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}{
		{
			Message: struct {
				Content          string           `json:"content"`
				ReasoningContent string           `json:"reasoning_content"`
				ToolCalls        []openAIToolCall `json:"tool_calls"`
			}{
				Content:          content.String(),
				ReasoningContent: reasoning.String(),
				ToolCalls:        orderedToolCalls,
			},
			FinishReason: finishReason,
		},
	}
	return result, nil
}

func (p *OpenAIProvider) convertMessages(ctx context.Context, messages []types.Message) ([]openAIMessage, error) {
	result := make([]openAIMessage, len(messages))
	for i, msg := range messages {
		content, err := p.convertContent(ctx, msg)
		if err != nil {
			return nil, err
		}
		result[i] = openAIMessage{
			Role:       msg.Role,
			Content:    content,
			ToolCallID: msg.ToolCallID,
		}
		if len(msg.ToolCalls) > 0 {
			result[i].ToolCalls = convertOpenAIToolCalls(msg.ToolCalls)
		}
	}
	return result, nil
}

func (p *OpenAIProvider) convertContent(ctx context.Context, msg types.Message) (any, error) {
	if len(msg.Parts) == 0 {
		return msg.Content, nil
	}
	parts := make([]openAIContentPart, 0, len(msg.Parts)+1)
	if msg.Content != "" {
		parts = append(parts, openAIContentPart{Type: "text", Text: msg.Content})
	}
	for _, part := range msg.Parts {
		if part.Media == nil {
			if part.Text != "" {
				parts = append(parts, openAIContentPart{Type: "text", Text: part.Text})
			}
			continue
		}
		kind := mediaKind(part)
		switch kind {
		case "image":
			if !capabilityEnabled(p.profile.SupportsImages, true) {
				return nil, unsupportedMedia(kind)
			}
			payload, err := resolveMediaPayload(ctx, p.media, *part.Media)
			if err != nil {
				return nil, err
			}
			imageURL := payload.URL
			if imageURL == "" {
				imageURL = dataURL(part.Media.MIMEType, payload.Data)
			}
			parts = append(parts, openAIContentPart{
				Type: "image_url", ImageURL: &openAIImageURL{URL: imageURL},
			})
		case "document", "file":
			if !capabilityEnabled(p.profile.SupportsDocuments, false) {
				return nil, unsupportedMedia(kind)
			}
			payload, err := resolveMediaPayload(ctx, p.media, *part.Media)
			if err != nil {
				return nil, err
			}
			if payload.URL != "" {
				return nil, fmt.Errorf("OpenAI file content requires inline bytes, not a URL")
			}
			parts = append(parts, openAIContentPart{
				Type: "file",
				File: &openAIFileContent{
					Filename: part.Media.Name,
					FileData: dataURL(part.Media.MIMEType, payload.Data),
				},
			})
		default:
			return nil, unsupportedMedia(kind)
		}
	}
	if len(parts) == 1 && parts[0].Type == "text" {
		return parts[0].Text, nil
	}
	return parts, nil
}

func convertOpenAIToolCalls(toolCalls []types.ToolCall) []openAIToolCall {
	result := make([]openAIToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		args, _ := json.Marshal(tc.Arguments)
		result[i] = openAIToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: openAIFunctionCall{
				Name:      tc.Name,
				Arguments: string(args),
			},
		}
	}
	return result
}

func convertOpenAITools(tools []types.JSONSchema) []openAITool {
	result := make([]openAITool, len(tools))
	for i, tool := range tools {
		properties := make(map[string]any, len(tool.Properties))
		for name, property := range tool.Properties {
			item := map[string]any{}
			// An empty or "any" type means the parameter accepts any JSON
			// value; providers have no such native type, so the constraint is
			// simply omitted.
			if property.Type != "" && property.Type != "any" {
				item["type"] = property.Type
			}
			if property.Description != "" {
				item["description"] = property.Description
			}
			if len(property.Enum) > 0 {
				item["enum"] = property.Enum
			}
			properties[name] = item
		}
		parameters := map[string]any{
			"type":       tool.Type,
			"properties": properties,
		}
		if len(tool.Required) > 0 {
			parameters["required"] = tool.Required
		}
		result[i] = openAITool{
			Type: "function",
			Function: openAIFunctionDef{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		}
	}
	return result
}

func convertOpenAIResponse(resp openAIResponse) *types.ChatResponse {
	result := &types.ChatResponse{
		Usage: types.TokenUsage{
			PromptTokens:             resp.Usage.PromptTokens,
			CompletionTokens:         resp.Usage.CompletionTokens,
			TotalTokens:              resp.Usage.TotalTokens,
			PromptCacheHitTokens:     resp.Usage.PromptCacheHitTokens,
			PromptCacheMissTokens:    resp.Usage.PromptCacheMissTokens,
			CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
			CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
		},
	}
	if resp.Usage.PromptTokensDetails != nil &&
		resp.Usage.PromptTokensDetails.CachedTokens > 0 &&
		result.Usage.CacheReadInputTokens == 0 {
		result.Usage.CacheReadInputTokens = resp.Usage.PromptTokensDetails.CachedTokens
	}
	if resp.Usage.CompletionTokensDetails != nil {
		result.Usage.ReasoningTokens = resp.Usage.CompletionTokensDetails.ReasoningTokens
	}
	if len(resp.Choices) == 0 {
		return result
	}
	choice := resp.Choices[0]
	result.Text = choice.Message.Content
	result.Reasoning = choice.Message.ReasoningContent
	result.FinishReason = choice.FinishReason
	for _, call := range choice.Message.ToolCalls {
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			args = map[string]any{"_raw": call.Function.Arguments}
		}
		result.ToolCalls = append(result.ToolCalls, types.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: args,
		})
	}
	return result
}

func responseFormat(schema *types.StructuredOutput) map[string]any {
	if schema == nil || len(schema.Schema) == 0 {
		return nil
	}
	properties := make(map[string]any, len(schema.Schema))
	required := make([]string, 0)
	for name, value := range schema.Schema {
		if property, ok := value.(map[string]any); ok {
			clean := make(map[string]any, len(property))
			if requiredFlag, ok := property["required"].(bool); ok && requiredFlag {
				required = append(required, name)
			}
			for key, item := range property {
				if key != "required" {
					clean[key] = item
				}
			}
			properties[name] = clean
			continue
		}
		properties[name] = value
	}
	raw := map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "heron_structured_output",
			"schema": map[string]any{
				"type":                 "object",
				"properties":           properties,
				"required":             required,
				"additionalProperties": true,
			},
			"strict": false,
		},
	}
	return raw
}

func generationAllowed(explicit bool, value any, capability *bool) bool {
	if capability != nil && !*capability {
		return false
	}
	if explicit {
		return value != nil
	}
	return value != nil
}

func profileAllowsToolCalls(profile types.ModelProfile, requested bool) bool {
	if !requested {
		return false
	}
	return profile.SupportsToolCall == nil || *profile.SupportsToolCall
}

func profileAllowsStructuredOutput(profile types.ModelProfile) bool {
	// Unknown remains enabled so compatible gateways can use it and the
	// request-level fallback can remove it. An explicit false is authoritative.
	return profile.SupportsStructuredOutput == nil || *profile.SupportsStructuredOutput
}

func profileAllowsReasoning(profile types.ModelProfile, explicit bool) bool {
	if explicit && profile.SupportsReasoning == nil {
		return true
	}
	if profile.SupportsReasoning != nil {
		return *profile.SupportsReasoning
	}
	return explicit || profile.Reasoning != nil
}

func hasOptionalGenerationParameters(req openAIRequest) bool {
	return req.Temperature != nil ||
		req.TopP != nil ||
		req.TopK != nil ||
		req.RepetitionPenalty != nil ||
		req.ReasoningEffort != ""
}

func withoutOptionalGenerationParameters(req openAIRequest) openAIRequest {
	req.Temperature = nil
	req.TopP = nil
	req.TopK = nil
	req.RepetitionPenalty = nil
	req.ReasoningEffort = ""
	return req
}

func isUnsupportedParameterError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, phrase := range []string{
		"unsupported",
		"not supported",
		"unknown parameter",
		"unrecognized parameter",
		"invalid parameter",
		"does not support",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}
