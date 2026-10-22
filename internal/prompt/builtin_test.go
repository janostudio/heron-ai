package prompt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestBuiltinTemplates(t *testing.T) {
	expected := []string{"execution-management", "tool-usage", "state-management", "knowledge-query", "perspective-isolation", "output-format"}
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
		{Kind: "team_state", Text: "Already inspected the loader", Source: "team_state", Priority: 70},
	}})
	for _, expected := range []string{"## Responsibility", "Analyze the data", "## Input", "Hello, world"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("missing %q:\n%s", expected, prompt)
		}
	}
	// Memory is the Agent's own and travels as its own assistant message.
	if strings.Contains(prompt, "Already inspected the loader") {
		t.Errorf("team state leaked into the user turn:\n%s", prompt)
	}
	if strings.Contains(prompt, "## Task") {
		t.Error("removed Task concept appeared")
	}
}

func TestBuildUserPromptExcludesProvenanceBlocks(t *testing.T) {
	r := NewPromptRenderer(nil)
	data, err := json.Marshal([]types.SharedRecord{{Name: "DiagnosisReport", Summary: "The parser is missing a null check."}})
	require.NoError(t, err)
	prompt := r.BuildUserPrompt(types.AgentConfig{}, types.AgentRequest{}, RenderContext{ContextBlocks: []types.ContextBlock{
		{Kind: "knowledge", Text: "The repository uses JSONL sessions.", Source: "knowledge"},
		{Kind: "agent_state", Text: "Keep changes small.", Source: "agent_state"},
		{Kind: "records", Text: string(data), Source: "owner"},
		{Kind: "input", Text: "Read project/src/config.js", Source: "user"},
	}})
	require.Contains(t, prompt, "Read project/src/config.js")
	for _, unexpected := range []string{"JSONL sessions", "Keep changes small", "DiagnosisReport", "null check"} {
		require.NotContains(t, prompt, unexpected,
			"provenance-bearing blocks must become their own messages, not part of the user turn")
	}
}

func TestBuildUserPromptKeepsUnattributedContext(t *testing.T) {
	r := NewPromptRenderer(nil)
	prompt := r.BuildUserPrompt(types.AgentConfig{}, types.AgentRequest{}, RenderContext{ContextBlocks: []types.ContextBlock{
		{Kind: "fanout_item", Text: `{"file":"a.go"}`, Source: "spawn", Priority: 85},
		{Kind: "input", Text: "fix the assigned file", Source: "user"},
	}})
	require.Contains(t, prompt, "## Your Item")
	require.Contains(t, prompt, "fix the assigned file")
}

// The whole point of the split: a collaborator's output is a message from that
// Team (assistant + name), the Agent's own memory is an unnamed assistant
// message, knowledge is harness context, and the task is the last user turn.
func TestRenderCarriesProvenanceInProtocol(t *testing.T) {
	data, err := json.Marshal([]types.SharedRecord{{
		Name:    "OwnerReply",
		Summary: "仓库 heron-connect 的主分支是 main",
		Data:    map[string]any{"repo": "heron-connect", "nested": map[string]any{"branch": "main"}},
	}})
	require.NoError(t, err)

	messages, err := NewPromptRenderer(nil).Render(
		types.AgentConfig{Persona: types.PersonaConfig{Role: "Coordinator"}},
		types.AgentRequest{TeamID: "default"},
		RenderContext{ContextBlocks: []types.ContextBlock{
			{Kind: "knowledge", Text: "repo layout", Placement: "user", Source: "knowledge"},
			{Kind: "team_state", Text: "Goal: answer the user", Placement: "user", Source: "team_state"},
			{Kind: "records", Text: string(data), Placement: "user", Source: "owner"},
			{Kind: "input", Text: "查看仓库heron-connect，目前主分支是什么", Placement: "user", Source: "user"},
		}})
	require.NoError(t, err)
	require.Len(t, messages, 5)

	require.Equal(t, "system", messages[0].Role)
	require.Contains(t, messages[0].Content, "Your team is default.")

	require.Equal(t, "user", messages[1].Role)
	require.Contains(t, messages[1].Content, systemReminderOpen)
	require.Contains(t, messages[1].Content, "repo layout")

	require.Equal(t, "assistant", messages[2].Role)
	require.Empty(t, messages[2].TeamID, "memory is the Agent's own, so it carries no name")
	require.Contains(t, messages[2].Content, "Goal: answer the user")

	require.Equal(t, "assistant", messages[3].Role)
	require.Equal(t, "owner", messages[3].TeamID)
	require.Contains(t, messages[3].Content, "主分支是 main")
	require.Contains(t, messages[3].Content, "repo=heron-connect")
	require.Contains(t, messages[3].Content, "nested.branch=main", "Data is flattened, not JSON")
	require.NotContains(t, messages[3].Content, "{")

	require.Equal(t, "user", messages[4].Role)
	require.Contains(t, messages[4].Content, "查看仓库heron-connect")

	for _, message := range messages[1:] {
		require.True(t, message.Injected, "injected context must not be folded by compaction")
	}
}

// A result delivered through the mailbox and the record it produced are the
// same content. The record is structured and wins; the mailbox copy is only a
// fallback, otherwise the user sees the answer twice.
func TestRenderPrefersRecordOverMailboxResult(t *testing.T) {
	data, err := json.Marshal([]types.SharedRecord{{Name: "OwnerReply", Summary: "主分支是 main"}})
	require.NoError(t, err)
	messages, err := NewPromptRenderer(nil).Render(types.AgentConfig{}, types.AgentRequest{TeamID: "default"},
		RenderContext{ContextBlocks: []types.ContextBlock{
			{Kind: "records", Text: string(data), Source: "owner"},
			{Kind: "mailbox_result", Text: "主分支是 main", Source: "owner"},
			{Kind: "input", Text: "查看主分支", Source: "user"},
		}})
	require.NoError(t, err)

	ownerMessages := 0
	for _, message := range messages {
		if message.TeamID == "owner" {
			ownerMessages++
			require.Equal(t, 1, strings.Count(message.Content, "主分支是 main"))
		}
	}
	require.Equal(t, 1, ownerMessages)
}

// A record with no producer cannot be attributed, and an unnamed assistant
// message would read as the model's own words. It stays in the user turn.
func TestRenderKeepsUnattributedRecordInUserTurn(t *testing.T) {
	data, err := json.Marshal([]types.SharedRecord{{Name: "Mystery", Summary: "谁都不知道这条从哪来"}})
	require.NoError(t, err)
	messages, err := NewPromptRenderer(nil).Render(types.AgentConfig{}, types.AgentRequest{TeamID: "default"},
		RenderContext{ContextBlocks: []types.ContextBlock{
			{Kind: "records", Text: string(data)},
			{Kind: "input", Text: "任务", Source: "user"},
		}})
	require.NoError(t, err)
	for _, message := range messages {
		if message.Role == "assistant" {
			require.NotContains(t, message.Content, "谁都不知道这条从哪来")
		}
	}
	last := messages[len(messages)-1]
	require.Equal(t, "user", last.Role)
	require.Contains(t, last.Content, "谁都不知道这条从哪来")
	require.Contains(t, last.Content, "任务")
}

// A Team that receives no user input still needs a user turn to answer.
func TestRenderAlwaysEndsWithUserTurn(t *testing.T) {
	messages, err := NewPromptRenderer(nil).Render(types.AgentConfig{}, types.AgentRequest{TeamID: "owner"},
		RenderContext{ContextBlocks: []types.ContextBlock{
			{Kind: "team_state", Text: "Goal: g", Placement: "user", Source: "team_state"},
		}})
	require.NoError(t, err)
	require.Equal(t, "user", messages[len(messages)-1].Role)
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

// The structured-output contract must tell the model that newlines inside
// string fields are allowed. Without it, every rule is a prohibition and the
// model collapses multi-point replies onto one line
// (ISSUE-structured-output-forces-single-line).
func TestStructuredOutputContractAllowsNewlines(t *testing.T) {
	got := structuredOutputContract(&types.StructuredOutput{Type: "json"})
	require.Contains(t, got, `\n`, "the contract must mention \\n so the model uses it")
	require.Contains(t, got, "readable", "and explain why")
}

func TestStructuredOutputContractNilIsEmpty(t *testing.T) {
	require.Empty(t, structuredOutputContract(nil))
}

// A Team publishes an intermediate reply ("dispatching a lookup") before its
// final answer arrives through the mailbox. Dropping the delivered result just
// because the source already has a record hid the answer from the caller, which
// then re-dispatched the same task in a loop.
func TestRenderKeepsDeliveredResultWhenRecordIsIntermediate(t *testing.T) {
	data, err := json.Marshal([]types.SharedRecord{{
		Name:    "OwnerReply",
		Summary: "我来查 genie-agent 仓库的主分支，已派事实查询。",
	}})
	require.NoError(t, err)

	const finalAnswer = "genie-agent 仓库的主分支是 main。"
	messages, err := NewPromptRenderer(nil).Render(types.AgentConfig{}, types.AgentRequest{TeamID: "default"},
		RenderContext{ContextBlocks: []types.ContextBlock{
			{Kind: "records", Text: string(data), Source: "owner"},
			{Kind: "mailbox_result", Text: finalAnswer, Source: "owner"},
			{Kind: "input", Text: "查看仓库genie-agent，目前主分支是什么", Source: "user"},
		}})
	require.NoError(t, err)

	var ownerText string
	for _, message := range messages {
		if message.TeamID == "owner" {
			ownerText += message.Content
		}
	}
	require.Contains(t, ownerText, "已派事实查询", "the intermediate record is kept")
	require.Contains(t, ownerText, finalAnswer, "the delivered answer must survive")
}
