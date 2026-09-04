package knowledge

import (
	"context"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// capturingSummarizerModel captures Chat arguments so tests can assert on the
// request the KnowledgeSummarizer produced.
type capturingSummarizerModel struct {
	calls        int
	lastMessages []types.Message
	lastTools    []types.JSONSchema
	lastConfig   types.ModelConfig
	text         string
	err          error
}

func (m *capturingSummarizerModel) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	m.calls++
	m.lastMessages = messages
	m.lastTools = tools
	m.lastConfig = config
	if m.err != nil {
		return nil, m.err
	}
	if m.text == "" {
		m.text = "distilled knowledge"
	}
	return &types.ChatResponse{Text: m.text}, nil
}

func (m *capturingSummarizerModel) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	return nil, nil
}

func TestSummarizerSummarizeReturnsTrimmedText(t *testing.T) {
	model := &capturingSummarizerModel{text: "  ---\nkind: fact\n---\n\nbody  "}
	s := NewKnowledgeSummarizer(model, "")

	got, err := s.Summarize(context.Background(), []string{"a candidate source"})
	if err != nil {
		t.Fatalf("Summarize returned error: %v", err)
	}
	if !strings.HasPrefix(got, "---") || !strings.HasSuffix(got, "body") {
		t.Fatalf("expected trimmed text, got %q", got)
	}
	if strings.HasPrefix(got, " ") || strings.HasSuffix(got, " ") {
		t.Fatalf("expected TrimSpace applied, got %q", got)
	}
}

func TestSummarizerBuildsRequestShape(t *testing.T) {
	// summaryModel = "gpt-x" -> config.Model == "gpt-x".
	model := &capturingSummarizerModel{text: "entry"}
	s := NewKnowledgeSummarizer(model, "gpt-x")

	_, err := s.Summarize(context.Background(), []string{"source one", "source two"})
	if err != nil {
		t.Fatalf("Summarize returned error: %v", err)
	}

	if model.lastTools != nil {
		t.Fatalf("expected tools nil, got %v", model.lastTools)
	}
	if model.lastConfig.Model != "gpt-x" {
		t.Fatalf("expected config.Model == %q, got %q", "gpt-x", model.lastConfig.Model)
	}
	if model.lastConfig.MaxOutputTokens == nil || *model.lastConfig.MaxOutputTokens != 2048 {
		t.Fatalf("expected MaxOutputTokens=2048, got %v", model.lastConfig.MaxOutputTokens)
	}
	if model.lastConfig.Temperature == nil || *model.lastConfig.Temperature != 0.0 {
		t.Fatalf("expected Temperature=0.0, got %v", model.lastConfig.Temperature)
	}
	if model.lastConfig.Reasoning != nil {
		t.Fatalf("expected Reasoning nil, got %v", model.lastConfig.Reasoning)
	}

	if len(model.lastMessages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(model.lastMessages))
	}
	if model.lastMessages[0].Role != "system" {
		t.Fatalf("expected system role, got %q", model.lastMessages[0].Role)
	}
	if !strings.Contains(model.lastMessages[0].Content, "knowledge summarizer") {
		t.Fatalf("expected system prompt marker, got %q", model.lastMessages[0].Content)
	}
	if model.lastMessages[1].Role != "user" {
		t.Fatalf("expected user role, got %q", model.lastMessages[1].Role)
	}
	if !strings.Contains(model.lastMessages[1].Content, "<candidate_sources>") {
		t.Fatalf("expected user template marker, got %q", model.lastMessages[1].Content)
	}
	if !strings.Contains(model.lastMessages[1].Content, "source one") ||
		!strings.Contains(model.lastMessages[1].Content, "source two") {
		t.Fatalf("expected sources embedded, got %q", model.lastMessages[1].Content)
	}
}

func TestSummarizerDefaultModelLeavesModelEmpty(t *testing.T) {
	model := &capturingSummarizerModel{text: "entry"}
	s := NewKnowledgeSummarizer(model, "")

	_, err := s.Summarize(context.Background(), []string{"source"})
	if err != nil {
		t.Fatalf("Summarize returned error: %v", err)
	}
	if model.lastConfig.Model != "" {
		t.Fatalf("expected config.Model == \"\", got %q", model.lastConfig.Model)
	}
}

func TestSummarizerEmptySourcesErrorsWithoutCallingModel(t *testing.T) {
	model := &capturingSummarizerModel{text: "entry"}
	s := NewKnowledgeSummarizer(model, "gpt-x")

	_, err := s.Summarize(context.Background(), nil)
	if err == nil {
		t.Fatalf("expected error on empty sources")
	}
	if model.calls != 0 {
		t.Fatalf("expected model not called, got %d calls", model.calls)
	}
}

func TestSummarizerNilResponseErrors(t *testing.T) {
	model := &nilSummarizerModel{}
	s := NewKnowledgeSummarizer(model, "")

	_, err := s.Summarize(context.Background(), []string{"source"})
	if err == nil {
		t.Fatalf("expected error on nil response")
	}
}

type nilSummarizerModel struct{}

func (nilSummarizerModel) Chat(context.Context, []types.Message, []types.JSONSchema, types.ModelConfig) (*types.ChatResponse, error) {
	return nil, nil
}

func (nilSummarizerModel) ChatStream(context.Context, []types.Message, []types.JSONSchema, types.ModelConfig) (<-chan types.ChatChunk, error) {
	return nil, nil
}
