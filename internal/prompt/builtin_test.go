package prompt

import (
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestBuiltinTemplates(t *testing.T) {
	expectedNames := []string{
		"execution-management",
		"tool-usage",
		"memory-management",
		"knowledge-query",
		"perspective-isolation",
		"output-format",
	}

	if len(BuiltinTemplates) != len(expectedNames) {
		t.Errorf("expected %d templates, got %d", len(expectedNames), len(BuiltinTemplates))
	}
	for _, name := range expectedNames {
		if tmpl, ok := BuiltinTemplates[name]; !ok {
			t.Errorf("missing template: %s", name)
		} else if tmpl == "" {
			t.Errorf("template %s is empty", name)
		}
	}
}

func TestGetTemplate(t *testing.T) {
	for _, name := range []string{
		"execution-management",
		"tool-usage",
		"memory-management",
		"knowledge-query",
		"perspective-isolation",
		"output-format",
	} {
		if tmpl := GetTemplate(name); tmpl == "" {
			t.Errorf("GetTemplate(%q) returned empty", name)
		}
	}
}

func TestGetTemplateNonExistent(t *testing.T) {
	if tmpl := GetTemplate("nonexistent-template"); tmpl != "" {
		t.Errorf("expected empty string for non-existent template, got %q", tmpl)
	}
}

func TestListTemplates(t *testing.T) {
	names := ListTemplates()
	if len(names) != 6 {
		t.Fatalf("expected 6 template names, got %d", len(names))
	}
	seen := make(map[string]bool)
	for _, name := range names {
		seen[name] = true
	}
	for _, name := range []string{
		"execution-management",
		"tool-usage",
		"memory-management",
		"knowledge-query",
		"perspective-isolation",
		"output-format",
	} {
		if !seen[name] {
			t.Errorf("expected template %q in list, but not found", name)
		}
	}
}

func TestBuildSystemPromptWithPersona(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{
		Persona: types.PersonaConfig{
			Role:      "Software Engineer",
			Goal:      "Write clean code",
			Backstory: "Experienced in Go",
		},
	}

	prompt := r.BuildSystemPrompt(agent, RenderContext{})
	for _, expected := range []string{
		"You are Software Engineer.",
		"Your goal: Write clean code",
		"Background: Experienced in Go",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("system prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestBuildSystemPromptWithRuntimeContext(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{
		Persona: types.PersonaConfig{Role: "Assistant"},
		Tools:   types.ToolConfig{Builtin: []string{"Read"}},
		Structured: &types.StructuredOutput{
			Type: "json",
		},
	}

	prompt := r.BuildSystemPrompt(agent, RenderContext{
		TeamMemory:     "Team decided to run tests before editing.",
		SubagentMemory: "The failing package is internal/api.",
	})
	for _, expected := range []string{
		"Tool Usage",
		"Output Format",
		"Team Memory",
		"Team decided to run tests before editing.",
		"Subagent Memory",
		"The failing package is internal/api.",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("system prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestBuildSystemPromptWithEmptyDefinition(t *testing.T) {
	r := NewPromptRenderer(nil)
	prompt := r.BuildSystemPrompt(types.AgentConfig{}, RenderContext{})
	if strings.Contains(prompt, "You are Software Engineer.") || strings.Contains(prompt, "Your goal: Write clean code") {
		t.Errorf("empty Agent Definition should not contain persona:\n%s", prompt)
	}
}

func TestBuildUserPromptWithResponsibilityAndInput(t *testing.T) {
	r := NewPromptRenderer(nil)
	prompt := r.BuildUserPrompt(types.AgentConfig{}, types.SubagentRequest{
		Responsibility: "Analyze the data",
		Input:          "Hello, world",
	}, RenderContext{})
	for _, expected := range []string{"## Responsibility", "Analyze the data", "## Input", "Hello, world"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("user prompt missing %q:\n%s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "## Task") {
		t.Error("user prompt must not use the removed Task concept")
	}
}

func TestBuildUserPromptWithRecordsKnowledgeAndMemory(t *testing.T) {
	r := NewPromptRenderer(nil)
	prompt := r.BuildUserPrompt(types.AgentConfig{}, types.SubagentRequest{}, RenderContext{
		KnowledgeText:  "The repository uses JSONL sessions.",
		TeamMemory:     "The team already inspected the loader.",
		SubagentMemory: "Keep changes small.",
		Records: []types.SharedRecord{{
			Name:    "DiagnosisReport",
			Kind:    "diagnosis",
			Summary: "The parser is missing a null check.",
			Data:    map[string]any{"confidence": 0.9},
		}},
	})
	for _, expected := range []string{
		"The repository uses JSONL sessions.",
		"## Team Memory",
		"## Subagent Memory",
		"## Shared Records",
		"DiagnosisReport",
		"The parser is missing a null check.",
		"confidence: 0.9",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("user prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestRender(t *testing.T) {
	r := NewPromptRenderer(nil)
	messages, err := r.Render(
		types.AgentConfig{Persona: types.PersonaConfig{Role: "Assistant"}},
		types.SubagentRequest{
			Responsibility: "Answer the question",
			Input:          "What is Go?",
		},
		RenderContext{},
	)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "system" || messages[1].Role != "user" {
		t.Fatalf("expected system then user messages, got %#v", messages)
	}
	if messages[0].Content == "" || messages[1].Content == "" {
		t.Error("rendered messages must not be empty")
	}
}

func TestNewPromptRenderer(t *testing.T) {
	if renderer := NewPromptRenderer(nil); renderer == nil || renderer.templates == nil {
		t.Error("NewPromptRenderer(nil) must initialize a renderer and template map")
	}
	custom := map[string]string{"custom": "content"}
	renderer := NewPromptRenderer(custom)
	if renderer.templates["custom"] != "content" {
		t.Error("custom template not preserved")
	}
}
