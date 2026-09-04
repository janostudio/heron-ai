package team

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestSortedCallResultNames(t *testing.T) {
	tests := []struct {
		name     string
		results  map[string]types.CallResult
		expected []string
	}{
		{
			name:     "empty",
			results:  map[string]types.CallResult{},
			expected: []string{},
		},
		{
			name: "unsorted keys",
			results: map[string]types.CallResult{
				"zebra": {}, "alpha": {}, "mike": {},
			},
			expected: []string{"alpha", "mike", "zebra"},
		},
		{
			name: "single",
			results: map[string]types.CallResult{
				"only": {},
			},
			expected: []string{"only"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, sortedCallResultNames(tt.results))
		})
	}
}

func TestAddUsage(t *testing.T) {
	tests := []struct {
		name     string
		usage    types.TokenUsage
		expected types.TokenUsage
	}{
		{
			name:     "all zero",
			usage:    types.TokenUsage{},
			expected: types.TokenUsage{},
		},
		{
			name: "accumulates every field",
			usage: types.TokenUsage{
				PromptTokens:             10,
				CompletionTokens:         20,
				ReasoningTokens:          30,
				TotalTokens:              60,
				PromptCacheHitTokens:     1,
				PromptCacheMissTokens:    2,
				CacheReadInputTokens:     3,
				CacheCreationInputTokens: 4,
			},
			expected: types.TokenUsage{
				PromptTokens:             10,
				CompletionTokens:         20,
				ReasoningTokens:          30,
				TotalTokens:              60,
				PromptCacheHitTokens:     1,
				PromptCacheMissTokens:    2,
				CacheReadInputTokens:     3,
				CacheCreationInputTokens: 4,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := &types.TokenUsage{}
			addUsage(total, tt.usage)
			assert.Equal(t, tt.expected, *total)
		})
	}
}

func TestAddUsageAddsToExisting(t *testing.T) {
	total := &types.TokenUsage{
		PromptTokens:     5,
		CompletionTokens: 7,
		TotalTokens:      12,
	}
	addUsage(total, types.TokenUsage{
		PromptTokens:     3,
		CompletionTokens: 4,
		TotalTokens:      7,
	})
	assert.Equal(t, 8, total.PromptTokens)
	assert.Equal(t, 11, total.CompletionTokens)
	assert.Equal(t, 19, total.TotalTokens)
}

func TestAddUsageNilReceiverIsNoop(t *testing.T) {
	// Must not panic when total is nil.
	assert.NotPanics(t, func() {
		addUsage(nil, types.TokenUsage{PromptTokens: 1})
	})
}

func TestRenderState(t *testing.T) {
	tests := []struct {
		name     string
		snapshot types.StateSnapshot
		expected string
	}{
		{
			name:     "empty snapshot",
			snapshot: types.StateSnapshot{},
			expected: "",
		},
		{
			name: "goal only",
			snapshot: types.StateSnapshot{
				Goal: "fix the bug",
			},
			expected: "Goal: fix the bug",
		},
		{
			name: "goal plus lists",
			snapshot: types.StateSnapshot{
				Goal:          "g",
				Confirmed:     []string{"a", "b"},
				OpenQuestions: []string{"q"},
				Decisions:     []string{"d1", "d2"},
				NextSteps:     []string{"n"},
			},
			expected: "Goal: g\n\n" +
				"Confirmed:\n- a\n- b\n\n" +
				"Open Questions:\n- q\n\n" +
				"Decisions:\n- d1\n- d2\n\n" +
				"Next Steps:\n- n",
		},
		{
			name: "lists without goal",
			snapshot: types.StateSnapshot{
				NextSteps: []string{"n1"},
			},
			expected: "Next Steps:\n- n1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, renderState(tt.snapshot))
		})
	}
}

func TestRenderRules(t *testing.T) {
	definitions := map[string]types.RuleItem{
		"r-all": {
			ID: "r-all", Type: "hard", Content: "always applies",
			Scope: types.Scope{Type: "all"},
		},
		"r-team": {
			ID: "r-team", Content: "team scoped",
			Scope: types.Scope{Type: "team", Teams: []string{"team-a"}},
		},
		"r-agents": {
			ID: "r-agents", Type: "soft", Content: "agent scoped",
			Scope: types.Scope{Type: "agents", Agents: []string{"agent-x"}},
		},
		"r-empty": {
			ID: "r-empty", Content: "   ",
			Scope: types.Scope{Type: "all"},
		},
	}

	tests := []struct {
		name     string
		names    []string
		agentID  string
		teamID   string
		expected string
	}{
		{
			name:    "no names returns empty",
			names:   nil,
			agentID: "agent-x",
			teamID:  "team-a",
		},
		{
			name:     "all scope renders",
			names:    []string{"r-all"},
			agentID:  "any",
			teamID:   "any",
			expected: "### r-all (hard)\nalways applies",
		},
		{
			name:     "team scope matches",
			names:    []string{"r-team"},
			agentID:  "any",
			teamID:   "team-a",
			expected: "### r-team\nteam scoped",
		},
		{
			name:    "team scope mismatch",
			names:   []string{"r-team"},
			agentID: "any",
			teamID:  "team-b",
		},
		{
			name:     "agents scope matches",
			names:    []string{"r-agents"},
			agentID:  "agent-x",
			teamID:   "any",
			expected: "### r-agents (soft)\nagent scoped",
		},
		{
			name:    "agents scope mismatch",
			names:   []string{"r-agents"},
			agentID: "agent-y",
			teamID:  "any",
		},
		{
			name:    "blank content skipped",
			names:   []string{"r-empty"},
			agentID: "any",
			teamID:  "any",
		},
		{
			name:     "unknown rule name skipped",
			names:    []string{"r-all", "r-missing"},
			agentID:  "any",
			teamID:   "any",
			expected: "### r-all (hard)\nalways applies",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, renderRules(definitions, tt.names, tt.agentID, tt.teamID))
		})
	}
}

func TestRenderRulesEmptyDefinitions(t *testing.T) {
	assert.Equal(t, "", renderRules(nil, []string{"r"}, "a", "t"))
	assert.Equal(t, "", renderRules(map[string]types.RuleItem{}, []string{"r"}, "a", "t"))
}

func TestRenderRulesWithLoader(t *testing.T) {
	ctx := context.Background()

	definitions := map[string]types.RuleItem{
		"r-loaded": {
			ID: "r-loaded", Type: "hard", Path: "rules/loaded.md",
			Scope: types.Scope{Type: "all"},
		},
		"r-memory": {
			ID: "r-memory", Content: "in memory", Path: "rules/memory.md",
			Scope: types.Scope{Type: "all"},
		},
		"r-gated": {
			ID: "r-gated", Path: "rules/gated.md",
			Paths: []string{"src/*.go"},
			Scope: types.Scope{Type: "all"},
		},
		"r-nopath": {
			ID: "r-nopath", Path: "rules/nopath.md",
			Scope: types.Scope{Type: "all"},
		},
		"r-err": {
			ID: "r-err", Path: "rules/err.md",
			Scope: types.Scope{Type: "all"},
		},
	}

	loader := func(_ context.Context, p string) (string, error) {
		switch p {
		case "rules/loaded.md":
			return "loaded body", nil
		case "rules/memory.md":
			return "should not be used", nil
		case "rules/gated.md":
			return "gated body", nil
		case "rules/nopath.md":
			return "", nil
		case "rules/err.md":
			return "", errors.New("boom")
		default:
			return "", errors.New("unexpected path " + p)
		}
	}

	t.Run("loader reads body for empty content", func(t *testing.T) {
		r := &Runtime{ruleLoader: loader}
		out := r.renderRulesWithLoader(ctx, definitions, []string{"r-loaded"}, "any", "any", "")
		assert.Equal(t, "### r-loaded (hard)\nloaded body", out)
	})

	t.Run("in-memory content wins over loader", func(t *testing.T) {
		r := &Runtime{ruleLoader: loader}
		out := r.renderRulesWithLoader(ctx, definitions, []string{"r-memory"}, "any", "any", "")
		assert.Equal(t, "### r-memory\nin memory", out)
	})

	t.Run("paths gate matches", func(t *testing.T) {
		r := &Runtime{ruleLoader: loader}
		out := r.renderRulesWithLoader(ctx, definitions, []string{"r-gated"}, "any", "any", "fix src/main.go now")
		assert.Equal(t, "### r-gated\ngated body", out)
	})

	t.Run("paths gate misses", func(t *testing.T) {
		r := &Runtime{ruleLoader: loader}
		out := r.renderRulesWithLoader(ctx, definitions, []string{"r-gated"}, "any", "any", "fix README.md now")
		assert.Equal(t, "", out)
	})

	t.Run("loader empty body skipped", func(t *testing.T) {
		r := &Runtime{ruleLoader: loader}
		out := r.renderRulesWithLoader(ctx, definitions, []string{"r-nopath"}, "any", "any", "")
		assert.Equal(t, "", out)
	})

	t.Run("loader error skipped", func(t *testing.T) {
		r := &Runtime{ruleLoader: loader}
		out := r.renderRulesWithLoader(ctx, definitions, []string{"r-err"}, "any", "any", "")
		assert.Equal(t, "", out)
	})

	t.Run("nil loader skips empty-content rules", func(t *testing.T) {
		r := &Runtime{}
		out := r.renderRulesWithLoader(ctx, definitions, []string{"r-loaded"}, "any", "any", "")
		assert.Equal(t, "", out)
	})
}

func TestRulePathsMatch(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		ctx      string
		expected bool
	}{
		{"empty patterns always match", nil, "anything", true},
		{"glob hit", []string{"src/*.go"}, "edit src/main.go", true},
		{"glob miss", []string{"src/*.go"}, "edit README.md", false},
		{"multiple patterns one hit", []string{"docs/*.md", "src/*.go"}, "edit src/a.go", true},
		{"token split on quotes", []string{"pkg/*.go"}, `open "pkg/x.go"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, rulePathsMatch(tt.patterns, tt.ctx))
		})
	}
}

func TestRuleVisible(t *testing.T) {
	tests := []struct {
		name    string
		rule    types.RuleItem
		agentID string
		teamID  string
		visible bool
	}{
		{
			name:    "empty type defaults to all",
			rule:    types.RuleItem{Scope: types.Scope{Type: ""}},
			visible: true,
		},
		{
			name:    "explicit all",
			rule:    types.RuleItem{Scope: types.Scope{Type: "all"}},
			visible: true,
		},
		{
			name:    "team match",
			rule:    types.RuleItem{Scope: types.Scope{Type: "team", Teams: []string{"t1", "t2"}}},
			teamID:  "t2",
			visible: true,
		},
		{
			name:    "team no match",
			rule:    types.RuleItem{Scope: types.Scope{Type: "team", Teams: []string{"t1"}}},
			teamID:  "t9",
			visible: false,
		},
		{
			name:    "agents match",
			rule:    types.RuleItem{Scope: types.Scope{Type: "agents", Agents: []string{"a1"}}},
			agentID: "a1",
			visible: true,
		},
		{
			name:    "agents no match",
			rule:    types.RuleItem{Scope: types.Scope{Type: "agents", Agents: []string{"a1"}}},
			agentID: "a2",
			visible: false,
		},
		{
			name:    "unknown scope type hidden",
			rule:    types.RuleItem{Scope: types.Scope{Type: "weird"}},
			visible: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.visible, ruleVisible(tt.rule, tt.agentID, tt.teamID))
		})
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		target   string
		expected bool
	}{
		{"empty slice", nil, "x", false},
		{"found", []string{"a", "b", "c"}, "b", true},
		{"not found", []string{"a", "b"}, "z", false},
		{"empty target found", []string{"", "a"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, contains(tt.values, tt.target))
		})
	}
}

func TestAppendUnique(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		extra    []string
		expected []string
	}{
		{
			name:     "adds new values",
			values:   []string{"a"},
			extra:    []string{"b", "c"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "skips duplicates",
			values:   []string{"a", "b"},
			extra:    []string{"b", "c"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "skips empty strings",
			values:   []string{"a"},
			extra:    []string{"", "b", ""},
			expected: []string{"a", "b"},
		},
		{
			name:     "empty base and empty extra",
			values:   nil,
			extra:    nil,
			expected: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, appendUnique(tt.values, tt.extra...))
		})
	}
}

func TestLimitCalls(t *testing.T) {
	calls := map[string]types.Call{
		"a": {ID: "a"},
		"b": {ID: "b"},
		"c": {ID: "c"},
	}

	t.Run("max zero returns unchanged", func(t *testing.T) {
		limited := limitCalls(calls, 0)
		assert.Len(t, limited, 3)
	})

	t.Run("max negative returns unchanged", func(t *testing.T) {
		limited := limitCalls(calls, -1)
		assert.Len(t, limited, 3)
	})

	t.Run("max greater than len returns unchanged", func(t *testing.T) {
		limited := limitCalls(calls, 10)
		assert.Len(t, limited, 3)
	})

	t.Run("truncates to max", func(t *testing.T) {
		limited := limitCalls(calls, 2)
		assert.Len(t, limited, 2)
	})
}

func TestShouldReceiveInput(t *testing.T) {
	tests := []struct {
		name     string
		inputs   types.InputSpec
		expected bool
	}{
		{"user message true", types.InputSpec{UserMessage: true}, true},
		{"team user message true", types.InputSpec{TeamUserMessage: true}, true},
		{"both true", types.InputSpec{UserMessage: true, TeamUserMessage: true}, true},
		{"neither", types.InputSpec{}, false},
		{"only records", types.InputSpec{Records: []types.InputBinding{{Record: "R"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, shouldReceiveInput(tt.inputs))
		})
	}
}

func TestBuildCallInput(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		configured types.Call
		expected   string
	}{
		{
			name:  "empty input returns empty",
			input: "",
			configured: types.Call{Inputs: types.InputSpec{
				UserMessage: true,
			}},
			expected: "",
		},
		{
			name:  "whitespace only returns empty",
			input: "   ",
			configured: types.Call{Inputs: types.InputSpec{
				UserMessage: true,
			}},
			expected: "",
		},
		{
			name:  "receives input",
			input: "hello",
			configured: types.Call{Inputs: types.InputSpec{
				UserMessage: true,
			}},
			expected: "hello",
		},
		{
			name:  "team user message receives input",
			input: "hello",
			configured: types.Call{Inputs: types.InputSpec{
				TeamUserMessage: true,
			}},
			expected: "hello",
		},
		{
			name:  "does not receive input",
			input: "hello",
			configured: types.Call{Inputs: types.InputSpec{
				Records: []types.InputBinding{{Record: "R"}},
			}},
			expected: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, buildCallInput(tt.input, nil, nil, tt.configured))
		})
	}
}

func TestContextBlockText(t *testing.T) {
	blocks := []types.ContextBlock{
		{Kind: "input", Text: "the input"},
		{Kind: "records", Text: "the records"},
	}
	assert.Equal(t, "the input", contextBlockText(blocks, "input"))
	assert.Equal(t, "the records", contextBlockText(blocks, "records"))
	assert.Equal(t, "", contextBlockText(blocks, "missing"))
	assert.Equal(t, "", contextBlockText(nil, "input"))
}

func TestHasContextBlock(t *testing.T) {
	blocks := []types.ContextBlock{
		{Kind: "input", Text: "x"},
	}
	assert.True(t, hasContextBlock(blocks, "input"))
	assert.False(t, hasContextBlock(blocks, "records"))
	assert.False(t, hasContextBlock(nil, "input"))
}

func TestBuildContextBlocks(t *testing.T) {
	records := []types.SharedRecord{
		{Name: "Report", RecordID: "r1", Summary: "s"},
	}

	t.Run("empty request builds responsibility only", func(t *testing.T) {
		req := types.CallRequest{
			Call: types.Call{Responsibility: "do work"},
		}
		blocks := buildContextBlocks(req)
		require.Len(t, blocks, 1)
		assert.Equal(t, "responsibility", blocks[0].Kind)
		assert.Equal(t, "do work", blocks[0].Text)
		assert.Equal(t, "call", blocks[0].Source)
		assert.Equal(t, "semi_stable", blocks[0].Stability)
		assert.Equal(t, 100, blocks[0].Priority)
		assert.True(t, blocks[0].Compressible)
	})

	t.Run("adds input when no input block present", func(t *testing.T) {
		req := types.CallRequest{
			Call:  types.Call{Responsibility: "do work"},
			Input: "user text",
		}
		blocks := buildContextBlocks(req)
		kinds := blockKinds(blocks)
		assert.Equal(t, []string{"responsibility", "input"}, kinds)
	})

	t.Run("preserves existing input block and skips adding", func(t *testing.T) {
		req := types.CallRequest{
			Call:  types.Call{Responsibility: "do work"},
			Input: "should not duplicate",
			ContextBlocks: []types.ContextBlock{
				{Kind: "input", Text: "original input"},
			},
		}
		blocks := buildContextBlocks(req)
		var inputs []string
		for _, b := range blocks {
			if b.Kind == "input" {
				inputs = append(inputs, b.Text)
			}
		}
		require.Len(t, inputs, 1)
		assert.Equal(t, "original input", inputs[0])
	})

	t.Run("adds records block", func(t *testing.T) {
		req := types.CallRequest{
			Call:    types.Call{Responsibility: "do work"},
			Records: records,
		}
		blocks := buildContextBlocks(req)
		kinds := blockKinds(blocks)
		assert.Equal(t, []string{"responsibility", "records"}, kinds)
	})

	t.Run("keeps non input/records/responsibility blocks", func(t *testing.T) {
		req := types.CallRequest{
			Call: types.Call{Responsibility: "do work"},
			ContextBlocks: []types.ContextBlock{
				{Kind: "knowledge", Text: "k"},
				{Kind: "rules", Text: "r"},
			},
		}
		blocks := buildContextBlocks(req)
		kinds := blockKinds(blocks)
		assert.Equal(t, []string{"knowledge", "rules", "responsibility"}, kinds)
	})

	t.Run("empty responsibility omitted", func(t *testing.T) {
		req := types.CallRequest{
			Call: types.Call{},
		}
		blocks := buildContextBlocks(req)
		assert.Empty(t, blocks)
	})
}

func blockKinds(blocks []types.ContextBlock) []string {
	kinds := make([]string, 0, len(blocks))
	for _, b := range blocks {
		kinds = append(kinds, b.Kind)
	}
	return kinds
}

func TestDeduplicateRecords(t *testing.T) {
	tests := []struct {
		name     string
		records  []types.SharedRecord
		expected []string // RecordIDs in order
	}{
		{
			name:     "empty",
			records:  nil,
			expected: []string{},
		},
		{
			name: "no duplicates",
			records: []types.SharedRecord{
				{RecordID: "r1"},
				{RecordID: "r2"},
			},
			expected: []string{"r1", "r2"},
		},
		{
			name: "duplicate record ids removed",
			records: []types.SharedRecord{
				{RecordID: "r1", Name: "a"},
				{RecordID: "r1", Name: "b"},
				{RecordID: "r2", Name: "c"},
			},
			expected: []string{"r1", "r2"},
		},
		{
			name: "fallback key on name and summary when no record id",
			records: []types.SharedRecord{
				{Name: "n", Summary: "s"},
				{Name: "n", Summary: "s"},
				{Name: "n", Summary: "other"},
			},
			expected: []string{"", ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deduplicateRecords(tt.records)
			ids := recordIDs(got)
			assert.Equal(t, tt.expected, ids)
		})
	}
}

func TestRecordIDs(t *testing.T) {
	records := []types.SharedRecord{
		{RecordID: "a"},
		{RecordID: ""},
		{RecordID: "c"},
	}
	assert.Equal(t, []string{"a", "", "c"}, recordIDs(records))
	assert.Equal(t, []string{}, recordIDs(nil))
}

func TestSessionStatusForCall(t *testing.T) {
	tests := []struct {
		name     string
		status   types.TurnStatus
		expected types.SessionStatus
	}{
		{"waiting input", types.TurnWaitingInput, types.SessionWaitingInput},
		{"waiting tool", types.TurnWaitingTool, types.SessionWaitingTool},
		{"waiting approval", types.TurnWaitingApproval, types.SessionWaitingApproval},
		{"completed", types.TurnCompleted, types.SessionCompleted},
		{"cancelled", types.TurnCancelled, types.SessionCancelled},
		{"failed", types.TurnFailed, types.SessionFailed},
		{"default running", types.TurnStatus("unknown"), types.SessionRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, sessionStatusForCall(tt.status))
		})
	}
}

func TestSpawnEventPayload(t *testing.T) {
	t.Run("nil spawn returns payload unchanged", func(t *testing.T) {
		payload := map[string]any{"key": "value"}
		got := spawnEventPayload(payload, nil)
		assert.Equal(t, map[string]any{"key": "value"}, got)
	})

	t.Run("adds spawn block", func(t *testing.T) {
		payload := map[string]any{"key": "value"}
		got := spawnEventPayload(payload, &agentstore.SpawnedCallSpec{
			AgentID:      "child-agent",
			Key:          "k1",
			ParentCallID: "parent",
		})
		spawnBlock, ok := got["spawn"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "child-agent", spawnBlock["agent"])
		assert.Equal(t, "k1", spawnBlock["key"])
		assert.Equal(t, "parent", spawnBlock["parent_call_id"])
	})
}

func TestPendingToolTaskForCall(t *testing.T) {
	t.Run("uses result fields when present", func(t *testing.T) {
		req := types.TeamTurnRequest{
			TeamTurn: types.TeamTurn{ID: "tt-1"},
		}
		result := types.CallResult{
			CallTurnID:   "ct-1",
			AgentID:      "agent-a",
			TaskID:       "task-1",
			CheckpointID: "cp-1",
			Status:       types.TurnWaitingTool,
		}
		task := pendingToolTaskForCall(req, "call-1", result)
		assert.Equal(t, "call-1", task.CallID)
		assert.Equal(t, "ct-1", task.CallTurnID)
		assert.Equal(t, "ct-1", task.AgentTurnID)
		assert.Equal(t, "agent-a", task.AgentID)
		assert.Equal(t, "task-1", task.TaskID)
		assert.Equal(t, "cp-1", task.CheckpointID)
		assert.Equal(t, types.TurnWaitingTool, task.Status)
	})

	t.Run("fallback to team turn id when call turn empty", func(t *testing.T) {
		req := types.TeamTurnRequest{
			TeamTurn: types.TeamTurn{ID: "tt-1"},
		}
		result := types.CallResult{Status: types.TurnWaitingTool}
		task := pendingToolTaskForCall(req, "call-1", result)
		assert.Equal(t, "tt-1:call-1", task.CallTurnID)
		assert.Equal(t, "tt-1:call-1", task.AgentTurnID)
	})

	t.Run("checkpoint fallback supplies agent and turn ids", func(t *testing.T) {
		req := types.TeamTurnRequest{
			TeamTurn: types.TeamTurn{ID: "tt-1"},
		}
		result := types.CallResult{
			Status: types.TurnWaitingTool,
			Checkpoint: &types.AgentCheckpoint{
				AgentID:     "cp-agent",
				AgentTurnID: "cp-turn",
			},
		}
		task := pendingToolTaskForCall(req, "call-1", result)
		assert.Equal(t, "cp-turn", task.CallTurnID)
		assert.Equal(t, "cp-turn", task.AgentTurnID)
		assert.Equal(t, "cp-agent", task.AgentID)
	})
}
