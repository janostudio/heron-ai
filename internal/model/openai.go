package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/sashabaranov/go-openai"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type OpenAIProvider struct {
	client    *openai.Client
	modelName string
}

func NewOpenAIProvider(apiKey, baseURL, modelName string) *OpenAIProvider {
	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	return &OpenAIProvider{
		client:    openai.NewClientWithConfig(config),
		modelName: modelName,
	}
}

func (p *OpenAIProvider) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	modelName := p.modelName
	if config.Model != "" {
		modelName = config.Model
	}

	oaiMessages := convertMessages(messages)
	oaiTools := convertTools(tools)

	req := openai.ChatCompletionRequest{
		Model:    modelName,
		Messages: oaiMessages,
		Tools:    oaiTools,
	}

	if config.Temperature > 0 {
		req.Temperature = float32(config.Temperature)
	}
	if config.MaxTokens > 0 {
		req.MaxTokens = config.MaxTokens
	}

	resp, err := p.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("openai chat: %w", err)
	}

	return convertResponse(resp), nil
}

func (p *OpenAIProvider) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	modelName := p.modelName
	if config.Model != "" {
		modelName = config.Model
	}

	oaiMessages := convertMessages(messages)
	oaiTools := convertTools(tools)

	req := openai.ChatCompletionRequest{
		Model:    modelName,
		Messages: oaiMessages,
		Tools:    oaiTools,
		Stream:   true,
	}

	if config.Temperature > 0 {
		req.Temperature = float32(config.Temperature)
	}
	if config.MaxTokens > 0 {
		req.MaxTokens = config.MaxTokens
	}

	stream, err := p.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("openai chat stream: %w", err)
	}

	ch := make(chan types.ChatChunk, 10)
	go func() {
		defer close(ch)
		defer stream.Close()
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				ch <- types.ChatChunk{Finished: true}
				return
			}
			if err != nil {
				ch <- types.ChatChunk{Finished: true}
				return
			}
			if len(resp.Choices) > 0 {
				delta := resp.Choices[0].Delta
				ch <- types.ChatChunk{
					Text:      delta.Content,
					Reasoning: delta.ReasoningContent,
				}
			}
		}
	}()

	return ch, nil
}

func convertMessages(messages []types.Message) []openai.ChatCompletionMessage {
	result := make([]openai.ChatCompletionMessage, len(messages))
	for i, msg := range messages {
		oaiMsg := openai.ChatCompletionMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
		if len(msg.ToolCalls) > 0 {
			oaiMsg.ToolCalls = convertToolCalls(msg.ToolCalls)
		}
		if msg.ToolCallID != "" {
			oaiMsg.ToolCallID = msg.ToolCallID
		}
		result[i] = oaiMsg
	}
	return result
}

func convertToolCalls(toolCalls []types.ToolCall) []openai.ToolCall {
	result := make([]openai.ToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		args, _ := json.Marshal(tc.Arguments)
		result[i] = openai.ToolCall{
			ID:   tc.ID,
			Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{
				Name:      tc.Name,
				Arguments: string(args),
			},
		}
	}
	return result
}

func convertTools(tools []types.JSONSchema) []openai.Tool {
	result := make([]openai.Tool, len(tools))
	for i, t := range tools {
		params := make(map[string]interface{})
		if t.Properties != nil {
			props := make(map[string]interface{})
			for name, prop := range t.Properties {
				propMap := map[string]interface{}{
					"type":        prop.Type,
					"description": prop.Description,
				}
				if len(prop.Enum) > 0 {
					propMap["enum"] = prop.Enum
				}
				props[name] = propMap
			}
			params["type"] = t.Type
			params["properties"] = props
			if len(t.Required) > 0 {
				params["required"] = t.Required
			}
		}
		result[i] = openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "",
				Description: "",
				Parameters:  params,
			},
		}
	}
	return result
}

func convertResponse(resp openai.ChatCompletionResponse) *types.ChatResponse {
	result := &types.ChatResponse{
		Usage: types.TokenUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		result.Text = choice.Message.Content

		for _, tc := range choice.Message.ToolCalls {
			var args map[string]any
			json.Unmarshal([]byte(tc.Function.Arguments), &args)
			result.ToolCalls = append(result.ToolCalls, types.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: args,
			})
		}
	}

	return result
}
