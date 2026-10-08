package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/definitions"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// fakeDefinitionWriter records what the tool forwarded and returns a canned
// result. The tool's job is translation — decode params, dispatch, render a
// report — and everything that decides whether a spec is valid lives in the
// writer, so a fake is the whole surface this test needs. No filesystem tree
// appears here on purpose: writer_test.go owns that.
type fakeDefinitionWriter struct {
	agentReq   *definitions.CreateAgentRequest
	teamReq    *definitions.CreateTeamRequest
	calls      int
	result     *definitions.ApplyResult
	err        error
	createFunc func(context.Context, definitions.CreateAgentRequest) (*definitions.ApplyResult, error)
	teamFunc   func(context.Context, definitions.CreateTeamRequest) (*definitions.ApplyResult, error)
}

func (f *fakeDefinitionWriter) CreateAgent(_ context.Context, req definitions.CreateAgentRequest) (*definitions.ApplyResult, error) {
	f.calls++
	f.agentReq = &req
	if f.createFunc != nil {
		return f.createFunc(context.Background(), req)
	}
	return f.result, f.err
}

func (f *fakeDefinitionWriter) CreateTeam(_ context.Context, req definitions.CreateTeamRequest) (*definitions.ApplyResult, error) {
	f.calls++
	f.teamReq = &req
	if f.teamFunc != nil {
		return f.teamFunc(context.Background(), req)
	}
	return f.result, f.err
}

// newDefineFixture builds the tool over a fake writer plus a store. The store
// is never written through — the fake stands in for that — but the tool
// refuses to run without one, so every test passes a real empty store.
func newDefineFixture(t *testing.T, fake *fakeDefinitionWriter) *DefineTool {
	t.Helper()
	store := types.NewDefinitionStore(&types.Definitions{}, "/tmp/config", "/tmp/config/flows/default.yml")
	return NewDefineTool(fake, store)
}

// defineReport is the decoded content the tool hands the model.
type defineReport struct {
	Action        string   `json:"action"`
	Mode          string   `json:"mode"`
	Files         []string `json:"files"`
	CreatedAgents []string `json:"created_agents"`
	UpdatedAgents []string `json:"updated_agents"`
	BoundToFlow   string   `json:"bound_to_flow"`
	Reload        string   `json:"reload"`
	Effective     string   `json:"effective"`
	Hint          string   `json:"hint"`
}

func decodeDefineReport(t *testing.T, result *types.ToolResult) defineReport {
	t.Helper()
	require.True(t, result.Success, "expected a successful result, got error: %s", result.Error)
	var report defineReport
	require.NoError(t, json.Unmarshal([]byte(result.Content), &report))
	return report
}

func TestDefineToolRejectsOutsideAgentContext(t *testing.T) {
	fake := &fakeDefinitionWriter{}
	tool := newDefineFixture(t, fake)

	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "create_agent",
		"spec":   map[string]any{"name": "reviewer"},
	})
	require.NoError(t, err, "model-visible failures are reported in the result, not as a Go error")
	require.False(t, result.Success)
	assert.Contains(t, result.Error, "only available inside an Agent execution context")
	assert.Zero(t, fake.calls, "the writer must not be reached without an agent identity")
}

func TestDefineToolRejectsUnknownAction(t *testing.T) {
	fake := &fakeDefinitionWriter{}
	tool := newDefineFixture(t, fake)

	result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
		"action": "delete_everything",
		"spec":   map[string]any{"name": "reviewer"},
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Contains(t, result.Error, `unknown action "delete_everything"`)
	assert.Zero(t, fake.calls)
}

func TestDefineToolRejectsUnknownMode(t *testing.T) {
	fake := &fakeDefinitionWriter{}
	tool := newDefineFixture(t, fake)

	// "patch" was deliberately folded into upsert rather than given its own
	// mode: upsert already only touches the fields it is given.
	result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
		"action": "create_agent",
		"mode":   "patch",
		"spec":   map[string]any{"name": "reviewer"},
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Contains(t, result.Error, `unknown mode "patch"`)
	assert.Zero(t, fake.calls)
}

func TestDefineToolRejectsMissingSpec(t *testing.T) {
	fake := &fakeDefinitionWriter{}
	tool := newDefineFixture(t, fake)

	result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{"action": "create_agent"})
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Contains(t, result.Error, "spec is required")
	assert.Zero(t, fake.calls)
}

func TestDefineToolRejectsSpecNotAnObject(t *testing.T) {
	fake := &fakeDefinitionWriter{}
	tool := newDefineFixture(t, fake)

	result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
		"action": "create_agent",
		"spec":   "name: reviewer",
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Contains(t, result.Error, "spec must be an object")
	assert.Zero(t, fake.calls)
}

func TestDefineToolForwardsModeAndTemplate(t *testing.T) {
	fake := &fakeDefinitionWriter{result: &definitions.ApplyResult{Action: "create_agent", Mode: "updated"}}
	tool := newDefineFixture(t, fake)

	spec := map[string]any{"name": "reviewer", "persona": map[string]any{"role": "code reviewer"}}
	result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
		"action":   "create_agent",
		"mode":     "upsert",
		"template": "agent.generic",
		"spec":     spec,
	})

	require.NoError(t, err)
	require.True(t, result.Success)
	require.NotNil(t, fake.agentReq)
	assert.Equal(t, definitions.ModeUpsert, fake.agentReq.Mode)
	assert.Equal(t, "agent.generic", fake.agentReq.Template)
	assert.Equal(t, spec, fake.agentReq.Spec)
	assert.Nil(t, fake.teamReq, "action=create_agent must not dispatch to CreateTeam")
}

func TestDefineToolDefaultsToCreateMode(t *testing.T) {
	fake := &fakeDefinitionWriter{result: &definitions.ApplyResult{Action: "create_team", Mode: "created"}}
	tool := newDefineFixture(t, fake)

	_, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
		"action": "create_team",
		"spec":   map[string]any{"id": "qa_team"},
	})
	require.NoError(t, err)
	require.NotNil(t, fake.teamReq)
	assert.Equal(t, definitions.ModeCreate, fake.teamReq.Mode)
	assert.Empty(t, fake.teamReq.Template)
}

func TestDefineToolReturnsNextTurnHint(t *testing.T) {
	fake := &fakeDefinitionWriter{result: &definitions.ApplyResult{
		Action:        "create_agent",
		Mode:          "created",
		Files:         []string{"/tmp/config/agents/reviewer/AGENT.md"},
		CreatedAgents: []string{"reviewer"},
		Reloaded:      true,
	}}
	tool := newDefineFixture(t, fake)

	result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
		"action": "create_agent",
		"spec":   map[string]any{"name": "reviewer"},
	})
	require.NoError(t, err)

	report := decodeDefineReport(t, result)
	assert.Equal(t, "next_turn", report.Effective)
	assert.Equal(t, "created", report.Mode)
	assert.Equal(t, []string{"reviewer"}, report.CreatedAgents)
	assert.Equal(t, "ok", report.Reload)
	assert.Empty(t, report.BoundToFlow, "an unbound agent must not report a flow binding")
	// The create_agent trap: the agent exists but nothing schedules it.
	assert.Contains(t, report.Hint, "not scheduled")
	assert.Contains(t, report.Hint, "create_team")
	assert.Equal(t, result.Metadata["hint"], report.Hint)
	assert.Equal(t, result.Metadata["effective"], "next_turn")
}

func TestDefineToolHintForTeamMentionsBinding(t *testing.T) {
	fake := &fakeDefinitionWriter{result: &definitions.ApplyResult{
		Action:      "create_team",
		Mode:        "created",
		BoundToFlow: "default",
		Reloaded:    true,
		CreatedAgents: []string{
			"reviewer",
		},
	}}
	tool := newDefineFixture(t, fake)

	result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
		"action": "create_team",
		"spec": map[string]any{
			"id":   "qa_team",
			"bind": map[string]any{"key": "qa"},
		},
	})
	require.NoError(t, err)

	report := decodeDefineReport(t, result)
	assert.Equal(t, "default", report.BoundToFlow)
	assert.Contains(t, report.Hint, "default")
	assert.Contains(t, report.Hint, "routable on the next turn")
}

func TestDefineToolEchoesCreatedVsUpdated(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		result *definitions.ApplyResult
	}{
		{
			name:   "created",
			mode:   "create",
			result: &definitions.ApplyResult{Action: "create_agent", Mode: "created", CreatedAgents: []string{"reviewer"}, Reloaded: true},
		},
		{
			name:   "updated",
			mode:   "upsert",
			result: &definitions.ApplyResult{Action: "create_agent", Mode: "updated", UpdatedAgents: []string{"reviewer"}, Reloaded: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDefinitionWriter{result: tc.result}
			tool := newDefineFixture(t, fake)

			result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
				"action": "create_agent",
				"mode":   tc.mode,
				"spec":   map[string]any{"name": "reviewer"},
			})
			require.NoError(t, err)

			report := decodeDefineReport(t, result)
			assert.Equal(t, tc.name, report.Mode, "the report must echo what the writer actually did")
		})
	}
}

func TestDefineToolUpsertHintExplainsMerge(t *testing.T) {
	fake := &fakeDefinitionWriter{result: &definitions.ApplyResult{
		Action:        "create_agent",
		Mode:          "updated",
		UpdatedAgents: []string{"reviewer"},
		Reloaded:      true,
	}}
	tool := newDefineFixture(t, fake)

	result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
		"action": "create_agent",
		"mode":   "upsert",
		"spec":   map[string]any{"name": "reviewer"},
	})
	require.NoError(t, err)

	report := decodeDefineReport(t, result)
	assert.Contains(t, report.Hint, "merged, not replaced")
	assert.Contains(t, report.Hint, "lists")
}

func TestDefineToolReportsPendingReload(t *testing.T) {
	fake := &fakeDefinitionWriter{result: &definitions.ApplyResult{
		Action:   "create_team",
		Mode:     "created",
		Reloaded: false,
	}}
	tool := newDefineFixture(t, fake)

	result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
		"action": "create_team",
		"spec":   map[string]any{"id": "qa_team"},
	})
	require.NoError(t, err)

	report := decodeDefineReport(t, result)
	assert.NotEqual(t, "ok", report.Reload)
	assert.Contains(t, report.Reload, "on disk")
	assert.Equal(t, "next_turn", report.Effective)
}

func TestDefineToolSurfacesWriterError(t *testing.T) {
	fake := &fakeDefinitionWriter{err: errors.New(`agent "reviewer" already exists: use mode upsert to modify it`)}
	tool := newDefineFixture(t, fake)

	result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
		"action": "create_agent",
		"spec":   map[string]any{"name": "reviewer"},
	})
	require.NoError(t, err, "a validation failure is the model's problem to fix, not a Go error")
	require.False(t, result.Success)
	assert.Contains(t, result.Error, "already exists")
	assert.Contains(t, result.Error, "mode upsert")
	assert.Empty(t, result.Content)
}

func TestDefineToolRejectsUnconfiguredTool(t *testing.T) {
	tool := NewDefineTool(nil, types.NewDefinitionStore(&types.Definitions{}, "", ""))
	result, err := tool.Execute(stateToolContext("agent-a"), map[string]any{
		"action": "create_agent",
		"spec":   map[string]any{"name": "reviewer"},
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Contains(t, result.Error, "not configured")

	empty := &DefineTool{}
	result, err = empty.Execute(stateToolContext("agent-a"), map[string]any{"action": "create_agent"})
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Contains(t, result.Error, "not configured")
}

func TestDefineToolDeclaresSerialExecution(t *testing.T) {
	tool := newDefineFixture(t, &fakeDefinitionWriter{})
	assert.Equal(t, types.ToolSerial, tool.Execution().Class,
		"two concurrent Define calls would interleave one flow file's read-modify-write")
	assert.False(t, tool.NeedsApproval())
	assert.Equal(t, "Define", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.Contains(t, tool.Description(), "NEXT turn")
}

func TestDefineToolParametersAreValid(t *testing.T) {
	tool := newDefineFixture(t, &fakeDefinitionWriter{})
	params := tool.Parameters()

	action, ok := params["action"].(map[string]any)
	require.True(t, ok, "action must be declared")
	assert.Equal(t, "string", action["type"])
	assert.Equal(t, []string{"create_agent", "create_team"}, action["enum"])
	assert.Equal(t, true, action["required"])
	assert.NotEmpty(t, action["description"])

	mode, ok := params["mode"].(map[string]any)
	require.True(t, ok, "mode must be declared")
	assert.Equal(t, []string{"create", "upsert"}, mode["enum"])

	spec, ok := params["spec"].(map[string]any)
	require.True(t, ok, "spec must be declared")
	assert.Equal(t, "object", spec["type"])
	assert.Equal(t, true, spec["required"])
	// The spec description is the model's only documentation of the two
	// action-dependent shapes, so both must be named there.
	specDoc, _ := spec["description"].(string)
	assert.Contains(t, specDoc, "create_agent")
	assert.Contains(t, specDoc, "create_team")

	// The template list is read from definitions.TemplateNames() rather than
	// hardcoded, so it cannot drift when a template is added.
	template, ok := params["template"].(map[string]any)
	require.True(t, ok, "template must be declared")
	templateDoc, _ := template["description"].(string)
	for _, name := range definitions.TemplateNames() {
		assert.Contains(t, templateDoc, name, "template description must list %q", name)
	}
}
