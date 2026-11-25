package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/tool/script"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// scriptTestExecutor stands in for the tool layer: it records which tools ran.
type scriptTestExecutor struct {
	executed []string
	approval map[string]bool
}

func (e *scriptTestExecutor) Execute(_ context.Context, name string, _ map[string]any) (*types.ToolResult, error) {
	e.executed = append(e.executed, name)
	return &types.ToolResult{Success: true, Content: "ok"}, nil
}

func (e *scriptTestExecutor) NeedsApproval(name string, _ map[string]any) (bool, error) {
	return e.approval[name], nil
}

func newScriptTestExecutor() *scriptTestExecutor {
	return &scriptTestExecutor{approval: map[string]bool{}}
}

func scriptTestAgent(tools ...string) types.AgentConfig {
	return types.AgentConfig{
		Name:  "script-agent",
		Model: types.ModelConfig{Model: "test"},
		Tools: types.ToolConfig{Builtin: tools},
		HITL:  &types.HITLConfig{Enabled: true},
	}
}

func scriptTestLoop(posture types.PermissionsConfig) (*TurnLoop, *scriptTestExecutor) {
	return scriptTestLoopWithModel(posture, &mockModelProvider{})
}

func scriptTestLoopWithModel(posture types.PermissionsConfig, model types.ModelProvider) (*TurnLoop, *scriptTestExecutor) {
	executor := newScriptTestExecutor()
	loop := NewTurnLoop(model, executor, nil, NewRouteParser(), NewHITLGate(0), NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "run"}}})
	loop.WithPermissions(posture, nil)
	return loop, executor
}

func scriptCall(code string) types.ToolCall {
	return types.ToolCall{Name: ScriptToolName, Arguments: map[string]any{"code": code}}
}

// A Flow that declares nothing is the pre-existing behaviour: allow, plus the
// built-in floor. REPL therefore needs no approval and no configuration — which is
// what makes it usable as a batch tool at all.
func TestScriptNeedsNoApprovalUnderDefaultPosture(t *testing.T) {
	loop, executor := scriptTestLoop(types.PermissionsConfig{})
	agent := scriptTestAgent("Read", "Write", "Bash", ScriptToolName)

	decision, reason, err := loop.toolDecision(context.Background(), agent, types.AgentRequest{}, scriptCall("return 1;"))
	require.NoError(t, err)
	require.Equal(t, ToolAllow, decision, reason)
	assert.Empty(t, executor.executed, "the decision must not execute anything")
}

// The program call itself is never a capability question any more: what a program
// may do is decided per step while it runs, so starting one needs no approval even
// when a governed tool is marked ask.
func TestScriptStartNeedsNoApprovalUnderAskPosture(t *testing.T) {
	loop, _ := scriptTestLoop(types.PermissionsConfig{Write: types.PermissionAsk})
	agent := scriptTestAgent("Read", "Write", "Bash", ScriptToolName)

	decision, reason, err := loop.toolDecision(context.Background(), agent, types.AgentRequest{}, scriptCall("return 1;"))
	require.NoError(t, err)
	require.Equal(t, ToolAllow, decision, reason)
}

// The inner call is where the question is asked: it comes back as require_approval,
// which the gate turns into a pause at that step.
func TestScriptInnerAskIsAnApprovalQuestion(t *testing.T) {
	loop, _ := scriptTestLoop(types.PermissionsConfig{Write: types.PermissionAsk})
	agent := scriptTestAgent("Read", "Write", ScriptToolName)
	ctx := script.WithStep(context.Background(), "script-1", 3)

	decision, reason, err := loop.toolDecision(ctx, agent, types.AgentRequest{},
		types.ToolCall{Name: "Write", Arguments: map[string]any{"file": "a.go", "content": "x"}})
	require.NoError(t, err)
	require.Equal(t, ToolRequireApproval, decision)
	assert.Contains(t, reason, "write=ask")
}

// Without HITL the question cannot be answered, so the step is refused with the
// reason instead of parking the turn forever.
func TestScriptInnerAskWithoutHITLIsReadable(t *testing.T) {
	loop, _ := scriptTestLoop(types.PermissionsConfig{Bash: types.PermissionAsk})
	agent := scriptTestAgent("Bash", ScriptToolName)
	agent.HITL = nil
	ctx := script.WithStep(context.Background(), "script-1", 0)

	decision, reason, err := loop.toolDecision(ctx, agent, types.AgentRequest{},
		types.ToolCall{Name: "Bash", Arguments: map[string]any{"command": "ls"}})
	require.NoError(t, err)
	require.Equal(t, ToolDeny, decision)
	assert.Contains(t, reason, "bash=ask")
	assert.Contains(t, reason, "hitl disabled")
}

// Inside a program the posture is enforced per call: a denied class is refused, an
// asked class runs once the program itself was approved.
func TestScriptPostureAppliesToInnerCalls(t *testing.T) {
	loop, executor := scriptTestLoop(types.PermissionsConfig{Bash: types.PermissionDeny, Write: types.PermissionAsk})
	agent := scriptTestAgent("Write", "Bash", ScriptToolName)
	ctx := script.WithStep(context.Background(), "script-1", 0)
	ctx = script.WithGrant(ctx, script.NewGrant([]string{"Write"}, "flow-permissions"))

	denied, _, err := loop.toolDecision(ctx, agent, types.AgentRequest{},
		types.ToolCall{Name: "Bash", Arguments: map[string]any{"command": "ls"}})
	require.NoError(t, err)
	require.Equal(t, ToolDeny, denied, "a grant must never upgrade a denied class")

	allowed, _, err := loop.toolDecision(ctx, agent, types.AgentRequest{},
		types.ToolCall{Name: "Write", Arguments: map[string]any{"file": "a.go", "content": "x"}})
	require.NoError(t, err)
	require.Equal(t, ToolAllow, allowed, "the approved class runs inside the program")
	assert.Empty(t, executor.executed)
}

// A restricted command inside a runtime that allows shell becomes an approval
// question at that step, not a failure: the whole point of the pause is that the
// program stops before the effect and asks.
func TestScriptRestrictedCommandInsideProgramAsks(t *testing.T) {
	loop, _ := scriptTestLoop(types.PermissionsConfig{Bash: types.PermissionAllow})
	agent := scriptTestAgent("Bash", ScriptToolName)
	ctx := script.WithStep(context.Background(), "script-1", 0)

	decision, reason, err := loop.toolDecision(ctx, agent, types.AgentRequest{},
		types.ToolCall{Name: "Bash", Arguments: map[string]any{"command": "rm -rf build"}})
	require.NoError(t, err)
	require.Equal(t, ToolRequireApproval, decision)
	assert.Contains(t, reason, "rm -rf")
}

// Binding is the Agent's tool set minus the hand-off tools and minus denied
// tools; the gate refuses anything outside it, including names passed as data to
// tools.call / tools.all.
func TestScriptGateBindsPostureAllowedToolsOnly(t *testing.T) {
	loop, executor := scriptTestLoop(types.PermissionsConfig{Bash: types.PermissionDeny})
	agent := scriptTestAgent("Read", "Write", "Bash", "Team", ScriptToolName)

	ctx := loop.withScriptGate(context.Background(), agent, types.AgentRequest{}, 1, []types.ToolCall{scriptCall("return 1;")})
	// Declaring REPL puts the whole code-tool set in the sandbox, so Read and
	// Write are bound whether or not the definition repeats them; Bash is listed
	// only in Blocked because this posture denies it.
	assert.Equal(t, []string{"Glob", "Grep", "Read", "Write"}, script.ToolNamesFromContext(ctx))
	assert.Equal(t, []string{"Bash"}, script.BlockedFromContext(ctx))

	gate, ok := script.GateFromContext(ctx)
	require.True(t, ok)

	// The real caller is the sandbox, which tags the step onto the context; the
	// direct-call refusal keys on that tag, so the harness must carry it too.
	step := script.WithStep(ctx, "script-1", 0)
	refused, err := gate(step, "Bash", map[string]any{"command": "ls"})
	require.NoError(t, err)
	require.False(t, refused.Success)
	assert.Contains(t, refused.Error, "this runtime's permissions do not allow it")

	allowed, err := gate(step, "Read", map[string]any{"file": "a"})
	require.NoError(t, err)
	require.True(t, allowed.Success)
	assert.Equal(t, []string{"Read"}, executor.executed)
}

// End to end: default posture, so the program runs on the first turn with no
// approval at all.
func TestScriptProgramRunsWithoutApproval(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{ToolCalls: []types.ToolCall{{ID: "repl-1", Name: ScriptToolName, Arguments: map[string]any{"code": "return 1;"}}}},
		{Text: "done"},
	}}
	loop, executor := scriptTestLoopWithModel(types.PermissionsConfig{}, model)
	loop.SetCheckpointStore(&memoryCheckpointStore{})

	result, err := loop.Run(context.Background(), scriptTestAgent("Read", ScriptToolName), types.AgentRequest{
		AgentID: "script-agent", AgentTurnID: "turn-readonly",
	})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Status)
	assert.Equal(t, []string{ScriptToolName}, executor.executed)
}

// The pause information has to be filled in by the turn loop: the sandbox knows the
// step and the tool, but only the loop can mint the request id a resume is matched
// against, and only the loop can attach the resume coordinates to the call.
func TestNormalizePauseRequestFillsIDAndResumeCoordinates(t *testing.T) {
	loop, _ := scriptTestLoop(types.PermissionsConfig{Bash: types.PermissionAsk})
	req := types.AgentRequest{AgentTurnID: "turn-1", CallID: "call-1"}
	call := types.ToolCall{ID: "tc-1", Name: ScriptToolName, Arguments: map[string]any{"code": "1;"}}
	result := &types.ToolResult{
		Success: false,
		Metadata: map[string]any{
			"paused": true, "script_id": "script-1", "step": 2, "tool": "Bash",
		},
		PendingApproval: &types.AgentPendingApproval{ToolName: "Bash", Reason: "permissions: bash=ask"},
	}

	loop.normalizePauseRequest(req, call, result)

	require.Equal(t, "turn-1:approval:tc-1", result.PendingApproval.RequestID)
	require.Equal(t, types.NextWaitApproval, result.Next.Action)
	require.Equal(t, "1;", result.PendingApproval.Arguments["code"], "the resume must re-issue the same program")
	require.Equal(t, "script-1", result.PendingApproval.Arguments["resume_script_id"])
	assert.Equal(t, 2, result.PendingApproval.Arguments["resume_step"])
}

// The resume arguments are what turn the approval into a grant for exactly that
// step; without them the resumed program would pause on the same step again.
func TestStepGrantReadsResumeArguments(t *testing.T) {
	grant := stepGrant(map[string]any{"code": "1;", "resume_script_id": "script-7", "resume_step": 4})
	require.NotNil(t, grant)
	assert.True(t, grant.CoversStep("script-7", 4))
	assert.False(t, grant.CoversStep("script-7", 5))
	assert.False(t, grant.CoversStep("script-8", 4))

	assert.Nil(t, stepGrant(map[string]any{"code": "1;"}), "a fresh program carries no approval")
}

// A denied tool is kept out of the model's tool list as well as out of the gate:
// offering what the policy refuses teaches the model that refusals are noise.
func TestScriptDeniedToolIsWithheldFromSchemas(t *testing.T) {
	loop, _ := scriptTestLoop(types.PermissionsConfig{Bash: types.PermissionDeny})
	agent := scriptTestAgent("Read", "Bash", ScriptToolName)

	withheld, err := loop.withheldTools(context.Background(), agent, types.AgentRequest{})
	require.NoError(t, err)
	require.Contains(t, withheld, "Bash")
	assert.Contains(t, withheld["Bash"], "Bash")

	schemas := loop.buildToolSchemasFiltered(context.Background(), agent, withheld)
	for _, schema := range schemas {
		assert.NotEqual(t, "Bash", schema.Name, "a withheld tool must not be advertised")
	}
}

// The approval is pinned to the step the human was asked about. A different step
// of the same program — and the same step of a different program — is still a
// question, which is what keeps a resumed run from inheriting a blanket yes.
func TestScriptStepGrantCoversOnlyItsOwnStep(t *testing.T) {
	loop, _ := scriptTestLoop(types.PermissionsConfig{Bash: types.PermissionAsk})
	agent := scriptTestAgent("Bash", ScriptToolName)
	call := types.ToolCall{Name: "Bash", Arguments: map[string]any{"command": "rm -rf build"}}

	granted := script.WithStep(context.Background(), "script-1", 2)
	granted = script.WithGrant(granted, script.NewStepGrant("script-1", 2))
	decision, reason, err := loop.toolDecision(granted, agent, types.AgentRequest{}, call)
	require.NoError(t, err)
	require.Equal(t, ToolAllow, decision, reason)

	otherStep := script.WithStep(context.Background(), "script-1", 3)
	otherStep = script.WithGrant(otherStep, script.NewStepGrant("script-1", 2))
	decision, _, err = loop.toolDecision(otherStep, agent, types.AgentRequest{}, call)
	require.NoError(t, err)
	require.Equal(t, ToolRequireApproval, decision, "a grant for step 2 must not cover step 3")

	otherScript := script.WithStep(context.Background(), "script-2", 2)
	otherScript = script.WithGrant(otherScript, script.NewStepGrant("script-1", 2))
	decision, _, err = loop.toolDecision(otherScript, agent, types.AgentRequest{}, call)
	require.NoError(t, err)
	require.Equal(t, ToolRequireApproval, decision, "a grant for one program must not cover another")
}

// Custom and MCP tools are part of the Agent's tool set, so a program sees them
// too: before this, an Agent whose custom tool worked directly lost it inside
// programs, which is a silent capability regression rather than a policy.
func TestScriptBindsCustomAndMCPTools(t *testing.T) {
	loop, _ := scriptTestLoop(types.PermissionsConfig{})
	agent := types.AgentConfig{
		Name:  "script-agent",
		Model: types.ModelConfig{Model: "test"},
		Tools: types.ToolConfig{
			Builtin: []string{"Read", ScriptToolName},
			Custom:  []string{"deploy"},
			MCP:     []string{"github_search"},
		},
		HITL: &types.HITLConfig{Enabled: true},
	}

	assert.Equal(t, []string{"Bash", "Glob", "Grep", "Read", "Write", "deploy", "github_search"}, loop.scriptBindableTools(agent))

	ctx := loop.withScriptGate(context.Background(), agent, types.AgentRequest{}, 1, []types.ToolCall{scriptCall("return 1;")})
	assert.Equal(t, []string{"Bash", "Glob", "Grep", "Read", "Write", "deploy", "github_search"}, script.ToolNamesFromContext(ctx))
}

// A paused program must resume as a program. The parked tool name is the one that
// ran; naming the inner tool made the resumed turn call Write with the program's
// arguments ("parameter file is required") and lose the approval.
func TestNormalizePauseRequestParksTheProgramNotTheStep(t *testing.T) {
	loop, _ := scriptTestLoop(types.PermissionsConfig{Write: types.PermissionAsk})
	req := types.AgentRequest{AgentTurnID: "turn-2"}
	call := types.ToolCall{ID: "tc-2", Name: ScriptToolName, Arguments: map[string]any{"code": "1;"}}
	result := &types.ToolResult{
		Metadata:        map[string]any{"script_id": "script-9", "step": 1, "tool": "Write"},
		PendingApproval: &types.AgentPendingApproval{ToolName: "Write"},
	}

	loop.normalizePauseRequest(req, call, result)

	require.Equal(t, ScriptToolName, result.PendingApproval.ToolName)
	assert.Equal(t, "1;", result.PendingApproval.Arguments["code"])
}
