package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

const (
	defaultAnthropicVersion         = "2023-06-01"
	defaultAnthropicMaxOutputTokens = 32768
)

// AnthropicProvider speaks the native Anthropic Messages API. It converts the
// engine's provider-neutral messages and tool calls into Anthropic content
// blocks, and maps the model profile defaults to Anthropic's field names.
type AnthropicProvider struct {
	client    *http.Client
	apiKey    string
	baseURL   string
	modelName string
	profile   types.ModelProfile
	version   string
}

func NewAnthropicProvider(apiKey, baseURL, modelName string) *AnthropicProvider {
	return NewAnthropicProviderWithProfile(apiKey, baseURL, modelName, types.ModelProfile{
		ID:       modelName,
		Name:     modelName,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Protocol: "anthropic_messages",
	})
}

func NewAnthropicProviderWithProfile(apiKey, baseURL, modelName string, profile types.ModelProfile) *AnthropicProvider {
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
	return &AnthropicProvider{
		client:    http.DefaultClient,
		apiKey:    apiKey,
		baseURL:   strings.TrimRight(baseURL, "/"),
		modelName: modelName,
		profile:   profile,
		version:   defaultAnthropicVersion,
	}
}

type anthropicRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       string                 `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	Temperature  *float64               `json:"temperature,omitempty"`
	TopP         *float64               `json:"top_p,omitempty"`
	TopK         *int                   `json:"top_k,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	Stream       bool                   `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
	Signature string         `json:"signature,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type anthropicOutputConfig struct {
	Effort *string                `json:"effort,omitempty"`
	Format *anthropicOutputFormat `json:"format,omitempty"`
}

type anthropicOutputFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema"`
}

type anthropicResponse struct {
	Content []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicErrorResponse struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *AnthropicProvider) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	modelName := p.modelName
	if config.Model != "" {
		modelName = config.Model
	}
	effective := MergeProfileDefaults(p.profile, config)
	req := p.buildRequest(modelName, messages, tools, config, effective, false)

	var wire anthropicResponse
	if err := p.post(ctx, effective, req, &wire); err != nil {
		if isUnsupportedParameterError(err) && hasAnthropicOptionalParameters(req) {
			req = withoutAnthropicOptionalParameters(req)
			if retryErr := p.post(ctx, effective, req, &wire); retryErr == nil {
				return convertAnthropicResponse(wire), nil
			} else {
				err = retryErr
			}
		}
		return nil, fmt.Errorf("anthropic messages: %w", err)
	}
	return convertAnthropicResponse(wire), nil
}

func (p *AnthropicProvider) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	resp, err := p.Chat(ctx, messages, tools, config)
	if err != nil {
		return nil, err
	}
	ch := make(chan types.ChatChunk, 1)
	ch <- types.ChatChunk{Text: resp.Text, Reasoning: resp.Reasoning, Finished: true}
	close(ch)
	return ch, nil
}

func (p *AnthropicProvider) buildRequest(
	modelName string,
	messages []types.Message,
	tools []types.JSONSchema,
	override types.ModelConfig,
	effective types.ModelConfig,
	stream bool,
) anthropicRequest {
	req := anthropicRequest{
		Model:  modelName,
		Stream: stream,
	}
	req.System, req.Messages = convertAnthropicMessages(messages)
	if profileAllowsToolCalls(p.profile, len(tools) > 0) {
		req.Tools = convertAnthropicTools(tools)
	}
	if limit := effective.OutputTokenLimit(); limit != nil && *limit > 0 {
		req.MaxTokens = *limit
	} else {
		req.MaxTokens = defaultAnthropicMaxOutputTokens
	}
	if effective.Temperature != nil && anthropicOptionAllowed(
		override.Temperature != nil,
		p.profile.SupportsTemperature,
	) {
		req.Temperature = effective.Temperature
	}
	if effective.TopP != nil && anthropicOptionAllowed(
		override.TopP != nil,
		p.profile.SupportsTopP,
	) {
		req.TopP = effective.TopP
	}
	if effective.TopK != nil && anthropicOptionAllowed(
		override.TopK != nil,
		p.profile.SupportsTopK,
	) {
		req.TopK = effective.TopK
	}
	if effective.Reasoning != nil && profileAllowsReasoning(p.profile, override.Reasoning != nil) {
		req.Thinking, req.OutputConfig = anthropicReasoning(effective.Reasoning)
	}
	if effective.ResponseFormat != nil && profileAllowsStructuredOutput(p.profile) {
		if req.OutputConfig == nil {
			req.OutputConfig = &anthropicOutputConfig{}
		}
		req.OutputConfig.Format = anthropicStructuredFormat(effective.ResponseFormat)
	}
	// Anthropic's thinking mode is mutually exclusive with sampling controls.
	// Prefer the explicit reasoning profile and let the provider fallback
	// remove it if the endpoint does not implement that mode.
	if req.Thinking != nil {
		req.Temperature = nil
		req.TopP = nil
		req.TopK = nil
	}
	return req
}

func (p *AnthropicProvider) post(ctx context.Context, effective types.ModelConfig, req anthropicRequest, output *anthropicResponse) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	baseURL := p.baseURL
	if effective.BaseURL != "" {
		baseURL = strings.TrimRight(effective.BaseURL, "/")
	}
	url := baseURL
	if !strings.HasSuffix(url, "/messages") {
		url += "/messages"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", p.version)
	apiKey := p.apiKey
	if effective.APIKey != "" {
		apiKey = effective.APIKey
	}
	if apiKey != "" {
		httpReq.Header.Set("x-api-key", apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var apiErr anthropicErrorResponse
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
			return fmt.Errorf("%s: %s", apiErr.Error.Type, apiErr.Error.Message)
		}
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func convertAnthropicMessages(messages []types.Message) (string, []anthropicMessage) {
	result := make([]anthropicMessage, 0, len(messages))
	var system []string
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			if strings.TrimSpace(msg.Content) != "" {
				system = append(system, msg.Content)
			}
		case "tool":
			result = append(result, anthropicMessage{
				Role: "user",
				Content: []anthropicBlock{{
					Type:      "tool_result",
					ToolUseID: msg.ToolCallID,
					Content:   msg.Content,
				}},
			})
		case "assistant":
			blocks := make([]anthropicBlock, 0, 1+len(msg.ToolCalls))
			if msg.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: msg.Content})
			}
			for _, call := range msg.ToolCalls {
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    call.ID,
					Name:  call.Name,
					Input: call.Arguments,
				})
			}
			if len(blocks) > 0 {
				result = append(result, anthropicMessage{Role: "assistant", Content: blocks})
			}
		default:
			result = append(result, anthropicMessage{Role: "user", Content: msg.Content})
		}
	}
	return strings.Join(system, "\n\n"), mergeAnthropicMessages(result)
}

func mergeAnthropicMessages(messages []anthropicMessage) []anthropicMessage {
	if len(messages) < 2 {
		return messages
	}
	result := make([]anthropicMessage, 0, len(messages))
	for _, message := range messages {
		if len(result) > 0 && result[len(result)-1].Role == message.Role {
			result[len(result)-1].Content = appendAnthropicContent(
				result[len(result)-1].Content,
				message.Content,
			)
			continue
		}
		result = append(result, message)
	}
	return result
}

func appendAnthropicContent(left, right any) any {
	leftBlocks, leftOK := left.([]anthropicBlock)
	rightBlocks, rightOK := right.([]anthropicBlock)
	if leftOK && rightOK {
		return append(leftBlocks, rightBlocks...)
	}
	leftText, leftOK := left.(string)
	rightText, rightOK := right.(string)
	if leftOK && rightOK {
		return leftText + "\n\n" + rightText
	}
	blocks := make([]anthropicBlock, 0, 2)
	if leftOK {
		blocks = append(blocks, anthropicBlock{Type: "text", Text: leftText})
	} else if leftBlocks, ok := left.([]anthropicBlock); ok {
		blocks = append(blocks, leftBlocks...)
	}
	if rightOK {
		blocks = append(blocks, anthropicBlock{Type: "text", Text: rightText})
	} else if rightBlocks, ok := right.([]anthropicBlock); ok {
		blocks = append(blocks, rightBlocks...)
	}
	if len(blocks) > 0 {
		return blocks
	}
	return []any{left, right}
}

func convertAnthropicTools(tools []types.JSONSchema) []anthropicTool {
	result := make([]anthropicTool, len(tools))
	for i, tool := range tools {
		properties := make(map[string]any, len(tool.Properties))
		for name, property := range tool.Properties {
			item := map[string]any{"type": property.Type}
			if property.Description != "" {
				item["description"] = property.Description
			}
			if len(property.Enum) > 0 {
				item["enum"] = property.Enum
			}
			properties[name] = item
		}
		schema := map[string]any{
			"type":       tool.Type,
			"properties": properties,
		}
		if len(tool.Required) > 0 {
			schema["required"] = tool.Required
		}
		result[i] = anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schema,
		}
	}
	return result
}

func anthropicReasoning(reasoning *types.ReasoningConfig) (*anthropicThinking, *anthropicOutputConfig) {
	if reasoning == nil {
		return nil, nil
	}
	thinkingType := strings.ToLower(strings.TrimSpace(reasoning.Type))
	if thinkingType == "" || thinkingType == "auto" {
		if reasoning.BudgetTokens != nil && *reasoning.BudgetTokens > 0 {
			thinkingType = "enabled"
		} else if reasoning.Effort != "" {
			thinkingType = "adaptive"
		}
	}
	if thinkingType == "" || thinkingType == "none" || thinkingType == "disabled" {
		return nil, nil
	}
	thinking := &anthropicThinking{Type: thinkingType}
	if reasoning.BudgetTokens != nil && *reasoning.BudgetTokens > 0 {
		thinking.BudgetTokens = *reasoning.BudgetTokens
	}
	var output *anthropicOutputConfig
	if reasoning.Effort != "" {
		effort := reasoning.Effort
		output = &anthropicOutputConfig{Effort: &effort}
	}
	return thinking, output
}

func anthropicStructuredFormat(schema *types.StructuredOutput) *anthropicOutputFormat {
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
	return &anthropicOutputFormat{
		Type: "json_schema",
		Schema: map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             required,
			"additionalProperties": true,
		},
	}
}

func convertAnthropicResponse(resp anthropicResponse) *types.ChatResponse {
	result := &types.ChatResponse{
		Usage: types.TokenUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			result.Text += block.Text
		case "thinking":
			result.Reasoning += block.Thinking
		case "tool_use":
			var args map[string]any
			if len(block.Input) > 0 {
				if err := json.Unmarshal(block.Input, &args); err != nil {
					args = map[string]any{"_raw": string(block.Input)}
				}
			}
			result.ToolCalls = append(result.ToolCalls, types.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
		}
	}
	return result
}

func hasAnthropicOptionalParameters(req anthropicRequest) bool {
	return req.Temperature != nil ||
		req.TopP != nil ||
		req.TopK != nil ||
		req.Thinking != nil ||
		req.OutputConfig != nil
}

func withoutAnthropicOptionalParameters(req anthropicRequest) anthropicRequest {
	req.Temperature = nil
	req.TopP = nil
	req.TopK = nil
	req.Thinking = nil
	req.OutputConfig = nil
	return req
}

func anthropicOptionAllowed(explicit bool, capability *bool) bool {
	if capability != nil && !*capability {
		return false
	}
	if explicit {
		return true
	}
	// A nil capability means the model profile did not publish a separate
	// capability flag. Presence of a model default is enough to use it; the
	// endpoint-level fallback handles stale metadata.
	return capability == nil
}
