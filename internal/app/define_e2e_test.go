package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// scriptedDefineModel is a ModelProvider that emits one Define tool call and
// then answers. It exists instead of a real provider because the property under
// test is wiring, not model quality: what matters is that a tool call arriving
// from the model travels the whole path — schema offer, turn-loop allow-list,
// registry lookup, tool execute, definition apply, store publication, session
// event — with no half of the double registration missing.
type scriptedDefineModel struct {
	mu sync.Mutex

	toolCalls []types.ToolCall

	// offered records the schemas handed to the model on the first call, which
	// is the only direct evidence that the tool was VISIBLE to it rather than
	// merely executable.
	offered []types.JSONSchema

	chatCalls int
}

func (m *scriptedDefineModel) Chat(_ context.Context, _ []types.Message, tools []types.JSONSchema, _ types.ModelConfig) (*types.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.chatCalls == 0 {
		m.offered = append([]types.JSONSchema(nil), tools...)
	}
	m.chatCalls++

	// First round: ask for the Define call. Every later round: plain answer,
	// so the turn terminates normally instead of looping on the tool.
	if m.chatCalls == 1 && len(m.toolCalls) > 0 {
		return &types.ChatResponse{
			ToolCalls: append([]types.ToolCall(nil), m.toolCalls...),
			Usage:     types.TokenUsage{TotalTokens: 12},
		}, nil
	}
	return &types.ChatResponse{
		Text:  "defined",
		Usage: types.TokenUsage{TotalTokens: 8},
	}, nil
}

func (m *scriptedDefineModel) ChatStream(_ context.Context, _ []types.Message, _ []types.JSONSchema, _ types.ModelConfig) (<-chan types.ChatChunk, error) {
	ch := make(chan types.ChatChunk, 1)
	go func() {
		defer close(ch)
		ch <- types.ChatChunk{Text: "defined", Finished: true}
	}()
	return ch, nil
}

func (m *scriptedDefineModel) DefaultModel() string { return "scripted" }

func (m *scriptedDefineModel) exposedToolNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.offered))
	for _, schema := range m.offered {
		names = append(names, schema.Name)
	}
	return names
}

// readSessionEvents returns the decoded events of the newest session log under
// root. The Flow session id is generated at runtime, so the test finds the log
// rather than predicting its path.
//
// Three files exist per session — flow.jsonl, team.jsonl, agent.jsonl — because
// one file per layer is how the engine keeps a team's events from interleaving
// with its agents'. The tool call lives in agent.jsonl; all three are read so
// the test does not have to know which layer emitted what.
func readSessionEvents(t *testing.T, root string) []map[string]any {
	t.Helper()
	sessionsDir := filepath.Join(root, ".agents", "data", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	require.NoError(t, err, "no session directory was written")

	var events []map[string]any
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionDir := filepath.Join(sessionsDir, entry.Name())
		files, readErr := os.ReadDir(sessionDir)
		if readErr != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() || filepath.Ext(file.Name()) != ".jsonl" {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(sessionDir, file.Name()))
			if readErr != nil {
				continue
			}
			for _, line := range splitLines(string(data)) {
				var event map[string]any
				if json.Unmarshal([]byte(line), &event) != nil {
					continue
				}
				events = append(events, event)
			}
		}
	}
	require.NotEmpty(t, events, "no session events were written")
	return events
}

func splitLines(text string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			if i > start {
				lines = append(lines, text[start:i])
			}
			start = i + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

// TestDefineToolReachesStoreThroughAnAgentTurn is the end-to-end proof that the
// Define feature is reachable, not merely built.
//
// Every other Define test drives one seam: define_tool_test.go drives the tool
// against a fake writer, definitions/writer_test.go drives the writer against a
// real tree, and the schema guard test drives buildToolSchemas. None of them
// would notice the failure this test is written for — a tool registered with
// the tool registry but absent from the agent's schema map is UNREACHABLE by a
// model and completely symptom-free, because nothing else in the suite ever
// goes through a model to call it.
//
// So this test runs the real wiring: a scripted model asks for Define, and the
// assertions then cover the three claims that only hold together end to end —
// the model SAW the schema, the call SUCCEEDED, and the store's snapshot
// actually changed.
func TestDefineToolReachesStoreThroughAnAgentTurn(t *testing.T) {
	configRoot, flowPath, startup := fixtureConfigRoot(t, t.TempDir())

	// The agent under test declares Define. This is the third leg of
	// reachability: the schema map gates visibility, and tools.builtin gates
	// both visibility (buildToolSchemas) and execution (executeToolCalls'
	// allow-list, which neuters an undeclared call into "tool is not allowed").
	assistant := startup.Agents["helper"]
	assistant.Tools.Builtin = append(assistant.Tools.Builtin, "Define")
	startup.Agents["helper"] = assistant

	store := types.NewDefinitionStore(startup, configRoot, flowPath)

	model := &scriptedDefineModel{toolCalls: []types.ToolCall{{
		ID:   "tc-define-1",
		Name: "Define",
		Arguments: map[string]any{
			"action": "create_agent",
			"spec": map[string]any{
				"name":    "reviewer",
				"persona": map[string]any{"role": "code reviewer"},
			},
		},
	}}}

	// The command executor runs `sh`; the scripted model never emits a Command
	// call, so an inert workspace is enough and the flow runs to completion.
	// The workspace root is also where the session log lands: engine state
	// (storage/logging) stays on the local workspaceRoot even when the
	// workspace tool backend is remote.
	workspaceRoot := t.TempDir()

	bundle, err := BuildRuntime(context.Background(), store, model, workspaceRoot, "info")
	require.NoError(t, err)
	require.NotNil(t, bundle)

	runCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := bundle.Flow.Start(runCtx, types.StartFlowRequest{
		Input: "define a reviewer agent",
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Claim 1: the model was OFFERED the Define schema. This is the assertion
	// that fails if only the tool registry half of the registration was done.
	assert.Contains(t, model.exposedToolNames(), "Define",
		"the model was never told the Define tool exists")

	// Claim 2: the call executed and completed inside the turn. The tool name
	// lives inside the event's payload — the envelope carries routing identity
	// (flow session, team, call) and the payload carries what happened.
	events := readSessionEvents(t, workspaceRoot)
	var completed, failed []string
	for _, event := range events {
		payload, _ := event["payload"].(map[string]any)
		name, _ := payload["tool_name"].(string)
		switch event["type"] {
		case types.EventToolCallCompleted:
			completed = append(completed, name)
		case types.EventToolCallFailed:
			failed = append(failed, name)
		}
	}
	assert.NotContains(t, failed, "Define",
		"Define failed inside the turn; a failed event means the call never reached the writer")
	assert.Contains(t, completed, "Define",
		"the session log must record a completed Define tool call")

	// The completed event carries the tool's own report, including the fact the
	// tool result announced: the change is effective next turn. That is the
	// model-visible half of "the write landed live".
	for _, event := range events {
		payload, _ := event["payload"].(map[string]any)
		if event["type"] != types.EventToolCallCompleted || payload["tool_name"] != "Define" {
			continue
		}
		assert.Equal(t, "next_turn", payload["effective"])
		assert.Equal(t, []any{"reviewer"}, payload["created_agents"])
	}

	// Claim 3: the store's snapshot changed. This is what makes the change
	// effective on the NEXT turn rather than only having written bytes.
	snapshot := store.Snapshot()
	require.NotNil(t, snapshot)
	_, exists := snapshot.Agents["reviewer"]
	assert.True(t, exists,
		"the definition store must publish the new agent; the files on disk are not enough")

	// And the agent file is where the writer says it is, so the snapshot and
	// the tree agree.
	_, statErr := os.Stat(filepath.Join(configRoot, "agents", "reviewer", "AGENT.md"))
	assert.NoError(t, statErr, "the created agent definition must exist in the config tree")

	// The startup tree must NOT have been mutated in place: Snapshot() hands
	// out the live tree, so an apply that merged into it rather than writing a
	// fresh one would corrupt every reader in the process.
	_, mutated := startup.Agents["reviewer"]
	assert.False(t, mutated, "the startup tree must not be mutated in place; the store publishes a new tree")
}
