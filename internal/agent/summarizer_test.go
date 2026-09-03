package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestMechanicalSummarizerMatchesBuildContextSummary(t *testing.T) {
	groups := [][]types.Message{
		{
			{Role: "user", Content: "please implement the parser"},
			{Role: "assistant", Content: "I will add the parser module."},
		},
		{
			{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{{ID: "c1", Name: "Read", Arguments: map[string]any{"path": "a.go"}}}},
		},
	}

	got, err := (mechanicalSummarizer{}).Summarize(context.Background(), groups)
	if err != nil {
		t.Fatalf("Summarize returned error: %v", err)
	}
	want := buildContextSummary(groups, 0)
	if got != want {
		t.Fatalf("mechanical summary mismatch:\n got: %q\nwant: %q", got, want)
	}
}

type errorSummarizer struct{}

func (errorSummarizer) Summarize(context.Context, [][]types.Message) (string, error) {
	return "", errors.New("summarizer boom")
}

func TestCompactFallsBackToMechanicalOnSummarizerError(t *testing.T) {
	config := types.ContextConfig{MaxInputTokens: 2000, RecentMessageGroups: 1}
	mgr := NewContextManagerWithSummarizer(config, nil, errorSummarizer{})

	// Add enough messages to be dropped on forced compaction.
	for i := 0; i < 20; i++ {
		if err := mgr.AddMessage(types.Message{Role: "user", Content: strings.Repeat("x", 100)}); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	if err := mgr.CompactForce(context.Background()); err != nil {
		t.Fatalf("CompactForce returned error: %v", err)
	}

	messages := mgr.Messages()
	if !hasCompactionSummary(messages) {
		t.Fatalf("expected a compaction summary to be present after fallback")
	}
	// The mechanical fallback should contain the role: content lines, not the
	// error text.
	var found bool
	for _, m := range messages {
		if strings.Contains(m.Content, "user: ") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected mechanical fallback content in summary")
	}
}

type fixedSummarizer struct{ text string }

func (f fixedSummarizer) Summarize(context.Context, [][]types.Message) (string, error) {
	return f.text, nil
}

func TestCompactUsesInjectedSummarizer(t *testing.T) {
	config := types.ContextConfig{MaxInputTokens: 2000, RecentMessageGroups: 1}
	mgr := NewContextManagerWithSummarizer(config, nil, fixedSummarizer{text: "LLM-produced summary of the dropped context"})

	for i := 0; i < 20; i++ {
		if err := mgr.AddMessage(types.Message{Role: "user", Content: strings.Repeat("x", 100)}); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	if err := mgr.CompactForce(context.Background()); err != nil {
		t.Fatalf("CompactForce returned error: %v", err)
	}

	var found bool
	for _, m := range mgr.Messages() {
		if strings.Contains(m.Content, "LLM-produced summary of the dropped context") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected injected summarizer text in compaction summary")
	}
}

func TestNewContextManagerWithSummarizerNilDefaults(t *testing.T) {
	mgr := NewContextManagerWithSummarizer(types.ContextConfig{}, nil, nil)
	if mgr.summarizer == nil {
		t.Fatalf("expected default mechanical summarizer when nil")
	}
	if mgr.estimator == nil {
		t.Fatalf("expected default estimator when nil")
	}
	if _, ok := mgr.summarizer.(mechanicalSummarizer); !ok {
		t.Fatalf("expected mechanicalSummarizer, got %T", mgr.summarizer)
	}
}
