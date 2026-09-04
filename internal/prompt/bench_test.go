package prompt

import (
	"encoding/json"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// BenchmarkRenderPrompt measures full prompt assembly (system + user) for one
// Agent turn. This is on every model call hot path: a render that allocates
// heavily or joins many strings raises per-turn latency and GC pressure across
// the entire engine. It exercises persona, tools, structured output, context
// blocks and shared-record formatting in a single call.
func BenchmarkRenderPrompt(b *testing.B) {
	renderer := NewPromptRenderer(BuiltinTemplates)

	agent := types.AgentConfig{
		Name: "assistant",
		Persona: types.PersonaConfig{
			Role:      "Software Engineer",
			Goal:      "Write clean Go code",
			Backstory: "Ten years of distributed systems experience",
		},
		Tools: types.ToolConfig{Builtin: []string{"Read", "Write", "Search"}},
		Body:  "Always prefer the standard library.",
		Structured: &types.StructuredOutput{
			Type:   "object",
			Schema: map[string]any{"answer": "string"},
		},
	}

	req := types.AgentRequest{
		FlowSessionID: "fs-1",
		AgentID:       "assistant",
		ContextBlocks: []types.ContextBlock{
			{Kind: "input", Text: "Explain the JSONL session format.", Source: "user", Placement: "user"},
		},
	}

	rctx := RenderContext{
		Variables: map[string]string{"language": "Go"},
		ContextBlocks: []types.ContextBlock{
			{Kind: "rules", Text: "Do not expose secrets.", Placement: "system", Source: "rule"},
			{Kind: "knowledge", Text: "The repository uses append-only JSONL sessions.", Placement: "system", Source: "knowledge"},
			{Kind: "team_state", Text: "We already inspected the loader.", Placement: "user", Source: "team_state"},
			{Kind: "records", Text: recordJSON(), Placement: "user", Source: "shared_records"},
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		messages, err := renderer.Render(agent, req, rctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(messages) != 2 {
			b.Fatalf("expected 2 messages, got %d", len(messages))
		}
	}
}

func recordJSON() string {
	records := []types.SharedRecord{
		{
			RecordID: "diagnosis-1",
			Name:     "DiagnosisReport",
			Kind:     "diagnosis",
			Summary:  "The parser is missing a null check.",
			Data:     map[string]any{"confidence": 0.9, "root_cause": "duplicate retry"},
		},
	}
	data, _ := json.Marshal(records)
	return string(data)
}
