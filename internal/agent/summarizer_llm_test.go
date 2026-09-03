package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// capturingSummarizerModel captures the Chat arguments so tests can assert on
// the request the llmSummarizer produced.
type capturingSummarizerModel struct {
	lastMessages []types.Message
	lastTools    []types.JSONSchema
	lastConfig   types.ModelConfig
	text         string
	err          error
}

func (m *capturingSummarizerModel) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	m.lastMessages = messages
	m.lastTools = tools
	m.lastConfig = config
	if m.err != nil {
		return nil, m.err
	}
	if m.text == "" {
		m.text = "structured LLM summary"
	}
	return &types.ChatResponse{Text: m.text}, nil
}

func (m *capturingSummarizerModel) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	return nil, nil
}

func TestLLMSummarizerBuildsRequest(t *testing.T) {
	model := &capturingSummarizerModel{text: "  summarized state  "}
	base := types.ModelConfig{Model: "test-model", APIKey: "key"}
	s := NewLLMSummarizer(model, base)

	groups := [][]types.Message{
		{{Role: "user", Content: "build the thing"}},
	}
	got, err := s.Summarize(context.Background(), groups)
	if err != nil {
		t.Fatalf("Summarize returned error: %v", err)
	}
	if got != "summarized state" {
		t.Fatalf("expected trimmed text, got %q", got)
	}

	// Assert request shape.
	if model.lastTools != nil {
		t.Fatalf("expected tools to be nil, got %v", model.lastTools)
	}
	if model.lastConfig.MaxOutputTokens == nil || *model.lastConfig.MaxOutputTokens != 1024 {
		t.Fatalf("expected MaxOutputTokens=1024, got %v", model.lastConfig.MaxOutputTokens)
	}
	if model.lastConfig.Reasoning != nil {
		t.Fatalf("expected Reasoning to be nil, got %v", model.lastConfig.Reasoning)
	}
	if model.lastConfig.ResponseFormat != nil {
		t.Fatalf("expected ResponseFormat to be nil, got %v", model.lastConfig.ResponseFormat)
	}
	if model.lastConfig.Temperature == nil || *model.lastConfig.Temperature != 0.0 {
		t.Fatalf("expected Temperature=0.0, got %v", model.lastConfig.Temperature)
	}
	if model.lastConfig.Model != "test-model" || model.lastConfig.APIKey != "key" {
		t.Fatalf("expected base fields preserved, got %+v", model.lastConfig)
	}

	if len(model.lastMessages) != 2 {
		t.Fatalf("expected 2 messages (system+user), got %d", len(model.lastMessages))
	}
	if model.lastMessages[0].Role != "system" {
		t.Fatalf("expected first message role system, got %q", model.lastMessages[0].Role)
	}
	if !strings.Contains(model.lastMessages[0].Content, "summarizing a conversation") {
		t.Fatalf("expected system prompt marker, got %q", model.lastMessages[0].Content)
	}
	if model.lastMessages[1].Role != "user" {
		t.Fatalf("expected second message role user, got %q", model.lastMessages[1].Role)
	}
	if !strings.Contains(model.lastMessages[1].Content, "build the thing") {
		t.Fatalf("expected user template to embed material, got %q", model.lastMessages[1].Content)
	}
}

func TestLLMSummarizerNilResponse(t *testing.T) {
	model := &capturingSummarizerModel{}
	s := NewLLMSummarizer(model, types.ModelConfig{})

	// Force nil response via a model that returns nil, nil.
	nilModel := &nilResponseModel{}
	s2 := NewLLMSummarizer(nilModel, types.ModelConfig{})
	_, err := s2.Summarize(context.Background(), [][]types.Message{{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatalf("expected error on nil response")
	}

	_ = s
}

type nilResponseModel struct{}

func (nilResponseModel) Chat(context.Context, []types.Message, []types.JSONSchema, types.ModelConfig) (*types.ChatResponse, error) {
	return nil, nil
}

func (nilResponseModel) ChatStream(context.Context, []types.Message, []types.JSONSchema, types.ModelConfig) (<-chan types.ChatChunk, error) {
	return nil, nil
}
