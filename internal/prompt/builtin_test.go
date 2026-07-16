package prompt

import (
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestBuiltinTemplates(t *testing.T) {
	expectedNames := []string{
		"task-management",
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
		"task-management",
		"tool-usage",
		"memory-management",
		"knowledge-query",
		"perspective-isolation",
		"output-format",
	} {
		tmpl := GetTemplate(name)
		if tmpl == "" {
			t.Errorf("GetTemplate(%q) returned empty", name)
		}
	}
}

func TestGetTemplateNonExistent(t *testing.T) {
	tmpl := GetTemplate("nonexistent-template")
	if tmpl != "" {
		t.Errorf("expected empty string for non-existent template, got %q", tmpl)
	}
}

func TestListTemplates(t *testing.T) {
	names := ListTemplates()
	if len(names) != 6 {
		t.Errorf("expected 6 template names, got %d", len(names))
	}

	seen := make(map[string]bool)
	for _, name := range names {
		seen[name] = true
	}

	expected := []string{
		"task-management",
		"tool-usage",
		"memory-management",
		"knowledge-query",
		"perspective-isolation",
		"output-format",
	}
	for _, name := range expected {
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

	prompt := r.BuildSystemPrompt(agent, nil, nil, "", "")
	if !strings.Contains(prompt, "You are Software Engineer.") {
		t.Error("system prompt missing role")
	}
	if !strings.Contains(prompt, "Your goal: Write clean code") {
		t.Error("system prompt missing goal")
	}
	if !strings.Contains(prompt, "Background: Experienced in Go") {
		t.Error("system prompt missing backstory")
	}
}

func TestBuildSystemPromptEmptyPersona(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{}

	prompt := r.BuildSystemPrompt(agent, nil, nil, "", "")

	if strings.Contains(prompt, "You are") {
		t.Error("system prompt should not contain persona for empty config")
	}
	if strings.Contains(prompt, "Your goal") {
		t.Error("system prompt should not contain goal for empty config")
	}
}

func TestBuildSystemPromptWithTools(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{
		Persona: types.PersonaConfig{
			Role: "Assistant",
		},
		Tools: types.ToolConfig{
			Builtin: []string{"read_file", "write_file"},
		},
	}

	prompt := r.BuildSystemPrompt(agent, nil, nil, "", "")
	if !strings.Contains(prompt, "Tool Usage") {
		t.Errorf("system prompt missing tool usage section:\n%s", prompt)
	}
}

func TestBuildSystemPromptWithoutTools(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{
		Persona: types.PersonaConfig{
			Role: "Assistant",
		},
	}

	prompt := r.BuildSystemPrompt(agent, nil, nil, "", "")
	if strings.Contains(prompt, "Tool Usage") {
		t.Error("system prompt should not contain tool usage when no tools configured")
	}
}

func TestBuildSystemPromptWithAgentState(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{
		Persona: types.PersonaConfig{
			Role: "Assistant",
		},
	}

	prompt := r.BuildSystemPrompt(agent, nil, nil, "Processing task #3", "")
	if !strings.Contains(prompt, "Current State") {
		t.Errorf("system prompt missing agent state:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Processing task #3") {
		t.Error("system prompt missing agent state content")
	}
}

func TestBuildSystemPromptWithTeamState(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{
		Persona: types.PersonaConfig{
			Role: "Assistant",
		},
	}

	prompt := r.BuildSystemPrompt(agent, nil, nil, "", "Team Alpha is working")
	if !strings.Contains(prompt, "Team Context") {
		t.Errorf("system prompt missing team state:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Team Alpha is working") {
		t.Error("system prompt missing team state content")
	}
}

func TestBuildSystemPromptWithStructuredOutput(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{
		Persona: types.PersonaConfig{
			Role: "Assistant",
		},
		Structured: &types.StructuredOutput{
			Type: "json",
		},
	}

	prompt := r.BuildSystemPrompt(agent, nil, nil, "", "")
	if !strings.Contains(prompt, "Output Format") {
		t.Errorf("system prompt missing output format section:\n%s", prompt)
	}
}

func TestBuildSystemPromptWithHandoffs(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{
		Persona: types.PersonaConfig{
			Role: "Assistant",
		},
		Handoffs: []string{"agent-b", "agent-c"},
	}

	prompt := r.BuildSystemPrompt(agent, nil, nil, "", "")
	if !strings.Contains(prompt, "Perspective Isolation") {
		t.Errorf("system prompt missing perspective isolation section:\n%s", prompt)
	}
}

func TestBuildUserPromptWithTask(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{}
	task := types.TaskConfig{
		Description: "Analyze the data",
	}

	prompt := r.BuildUserPrompt(agent, task, "", RenderContext{})
	if !strings.Contains(prompt, "Analyze the data") {
		t.Errorf("user prompt missing task description:\n%s", prompt)
	}
}

func TestBuildUserPromptWithInput(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{}
	task := types.TaskConfig{}

	prompt := r.BuildUserPrompt(agent, task, "Hello, world", RenderContext{})
	if !strings.Contains(prompt, "Hello, world") {
		t.Errorf("user prompt missing input:\n%s", prompt)
	}
}

func TestBuildUserPromptWithKnowledge(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{}
	task := types.TaskConfig{}

	rctx := RenderContext{
		KnowledgeText: "The sky is blue",
	}
	prompt := r.BuildUserPrompt(agent, task, "", rctx)
	if !strings.Contains(prompt, "The sky is blue") {
		t.Errorf("user prompt missing knowledge:\n%s", prompt)
	}
}

func TestBuildUserPromptWithMemories(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{}
	task := types.TaskConfig{}

	rctx := RenderContext{
		RecentMemories: "User prefers Python",
	}
	prompt := r.BuildUserPrompt(agent, task, "", rctx)
	if !strings.Contains(prompt, "Recent Memories") {
		t.Errorf("user prompt missing recent memories header:\n%s", prompt)
	}
	if !strings.Contains(prompt, "User prefers Python") {
		t.Error("user prompt missing memory content")
	}
}

func TestBuildUserPromptWithWorkerResults(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{}
	task := types.TaskConfig{}

	rctx := RenderContext{
		WorkerResults: []types.AgentResult{
			{Raw: "Result from worker A"},
			{Raw: "Result from worker B"},
		},
	}
	prompt := r.BuildUserPrompt(agent, task, "", rctx)
	if !strings.Contains(prompt, "Previous Results") {
		t.Errorf("user prompt missing previous results header:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Result from worker A") {
		t.Error("user prompt missing worker A result")
	}
	if !strings.Contains(prompt, "Result from worker B") {
		t.Error("user prompt missing worker B result")
	}
}

func TestRender(t *testing.T) {
	r := NewPromptRenderer(nil)
	agent := types.AgentConfig{
		Persona: types.PersonaConfig{
			Role: "Assistant",
			Goal: "Help the user",
		},
	}
	task := types.TaskConfig{
		Description: "Answer the question",
	}
	input := "What is Go?"

	messages, err := r.Render(agent, task, input, RenderContext{})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	if messages[0].Role != "system" {
		t.Errorf("first message role should be system, got %q", messages[0].Role)
	}

	if messages[1].Role != "user" {
		t.Errorf("second message role should be user, got %q", messages[1].Role)
	}

	if messages[0].Content == "" {
		t.Error("system message content is empty")
	}

	if messages[1].Content == "" {
		t.Error("user message content is empty")
	}
}

func TestNewPromptRendererNil(t *testing.T) {
	r := NewPromptRenderer(nil)
	if r == nil {
		t.Fatal("NewPromptRenderer returned nil")
	}
	if r.templates == nil {
		t.Error("templates map should not be nil after NewPromptRenderer(nil)")
	}
}

func TestNewPromptRendererWithTemplates(t *testing.T) {
	custom := map[string]string{"custom": "content"}
	r := NewPromptRenderer(custom)
	if r.templates["custom"] != "content" {
		t.Error("custom template not preserved")
	}
}
