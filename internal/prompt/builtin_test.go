package prompt

import (
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestBuiltinTemplates(t *testing.T) {
	expected := []string{"execution-management", "tool-usage", "memory-management", "knowledge-query", "perspective-isolation", "output-format"}
	if len(BuiltinTemplates) != len(expected) {
		t.Fatalf("templates=%d want=%d", len(BuiltinTemplates), len(expected))
	}
	for _, name := range expected {
		if GetTemplate(name) == "" {
			t.Fatalf("template %q is empty", name)
		}
	}
}

func TestGetTemplateNonExistent(t *testing.T) {
	if got := GetTemplate("missing"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildSystemPromptWithPersonaAndSystemContext(t *testing.T) {
	r := NewPromptRenderer(nil)
	prompt := r.BuildSystemPrompt(types.AgentConfig{
		Persona: types.PersonaConfig{Role: "Software Engineer", Goal: "Write clean code", Backstory: "Experienced in Go"},
		Tools:   types.ToolConfig{Builtin: []string{"Read"}},
	}, RenderContext{ContextBlocks: []types.ContextBlock{{
		Kind: "rules", Text: "Do not expose secrets", Placement: "system", Source: "rule",
	}}})
	for _, expected := range []string{"You are Software Engineer.", "Your goal: Write clean code", "Background: Experienced in Go", "Tool Usage", "Do not expose secrets"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("missing %q:\n%s", expected, prompt)
		}
	}
}

func TestBuildUserPromptUsesContextBlocksOnly(t *testing.T) {
	r := NewPromptRenderer(nil)
	prompt := r.BuildUserPrompt(types.AgentConfig{}, types.AgentRequest{}, RenderContext{ContextBlocks: []types.ContextBlock{
		{Kind: "responsibility", Text: "Analyze the data", Source: "call", Priority: 100},
		{Kind: "input", Text: "Hello, world", Source: "user", Priority: 80},
		{Kind: "team_memory", Text: "Already inspected the loader", Source: "team_memory", Priority: 70},
	}})
	for _, expected := range []string{"## Responsibility", "Analyze the data", "## Input", "Hello, world", "## Team Memory", "Already inspected the loader"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("missing %q:\n%s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "## Task") {
		t.Error("removed Task concept appeared")
	}
}

func TestBuildUserPromptRendersRecordContextBlock(t *testing.T) {
	r := NewPromptRenderer(nil)
	records := []types.SharedRecord{{Name: "DiagnosisReport", Kind: "diagnosis", Summary: "The parser is missing a null check.", Data: map[string]any{"confidence": 0.9}}}
	data := formatRecords(records)
	prompt := r.BuildUserPrompt(types.AgentConfig{}, types.AgentRequest{}, RenderContext{ContextBlocks: []types.ContextBlock{
		{Kind: "knowledge", Text: "The repository uses JSONL sessions.", Source: "knowledge"},
		{Kind: "agent_memory", Text: "Keep changes small.", Source: "agent_memory"},
		{Kind: "records", Text: data, Source: "shared_records"},
	}})
	for _, expected := range []string{"The repository uses JSONL sessions.", "Keep changes small.", "## Shared Records", "DiagnosisReport", "confidence: 0.9"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("missing %q:\n%s", expected, prompt)
		}
	}
}

func TestBuildUserPromptRendersSpawnContextBlocks(t *testing.T) {
	r := NewPromptRenderer(nil)
	prompt := r.BuildUserPrompt(types.AgentConfig{}, types.AgentRequest{}, RenderContext{ContextBlocks: []types.ContextBlock{
		{Kind: "fanout_item", Text: `{"file":"a.go"}`, Source: "spawn", Priority: 85},
		{Kind: "entity_memory", Text: "Goal: fix the assigned file", Source: "entity_memory", Priority: 60},
	}})
	for _, expected := range []string{"## Your Item", `{"file":"a.go"}`, "## Entity Memory", "Goal: fix the assigned file"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("missing %q:\n%s", expected, prompt)
		}
	}
}

func TestRenderKeepsSystemAndUserContextSeparated(t *testing.T) {
	r := NewPromptRenderer(nil)
	messages, err := r.Render(types.AgentConfig{Persona: types.PersonaConfig{Role: "Assistant"}}, types.AgentRequest{}, RenderContext{ContextBlocks: []types.ContextBlock{
		{Kind: "rules", Text: "stable rule", Placement: "system", Source: "rule"},
		{Kind: "input", Text: "Read project/src/config.js", Placement: "user", Source: "user"},
		{Kind: "team_memory", Text: "known revision", Placement: "user", Source: "team_memory"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" {
		t.Fatalf("messages=%#v", messages)
	}
	if !strings.Contains(messages[0].Content, "stable rule") {
		t.Fatal("system block missing")
	}
	if strings.Contains(messages[0].Content, "known revision") {
		t.Fatal("dynamic block leaked into system")
	}
	if strings.Count(messages[1].Content, "## Input") != 1 {
		t.Fatalf("input count wrong: %s", messages[1].Content)
	}
}

func TestRenderAlwaysReturnsTwoMessages(t *testing.T) {
	messages, err := NewPromptRenderer(nil).Render(types.AgentConfig{}, types.AgentRequest{}, RenderContext{ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "What is Go?"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Content == "" || messages[1].Content == "" {
		t.Fatalf("messages=%#v", messages)
	}
}
