package agent

import (
	"context"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================
// SignalParser Tests
// ============================================================

func TestSignalParser_Parse_Continue(t *testing.T) {
	p := NewSignalParser()

	tests := []struct {
		name  string
		input string
	}{
		{"suffix tag", "some text</continue>"},
		{"self-closing tag", "some text<continue/>"},
		{"self-closing only", "<continue/>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, types.SignalContinue, p.Parse(tt.input))
		})
	}
}

func TestSignalParser_Parse_WaitInput(t *testing.T) {
	p := NewSignalParser()

	tests := []struct {
		name  string
		input string
	}{
		{"suffix tag", "some text</wait_input>"},
		{"self-closing tag", "some text<wait_input/>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, types.SignalWaitInput, p.Parse(tt.input))
		})
	}
}

func TestSignalParser_Parse_GoalAchieved(t *testing.T) {
	p := NewSignalParser()

	assert.Equal(t, types.SignalGoalAchieved, p.Parse("done</goal_achieved>"))
	assert.Equal(t, types.SignalGoalAchieved, p.Parse("done<goal_achieved/>"))
}

func TestSignalParser_Parse_GoalFailed(t *testing.T) {
	p := NewSignalParser()

	assert.Equal(t, types.SignalGoalFailed, p.Parse("failed</goal_failed>"))
	assert.Equal(t, types.SignalGoalFailed, p.Parse("failed<goal_failed/>"))
}

func TestSignalParser_Parse_GoalImpossible(t *testing.T) {
	p := NewSignalParser()

	assert.Equal(t, types.SignalGoalImpossible, p.Parse("impossible</goal_impossible>"))
	assert.Equal(t, types.SignalGoalImpossible, p.Parse("impossible<goal_impossible/>"))
}

func TestSignalParser_Parse_NoSignal(t *testing.T) {
	p := NewSignalParser()

	assert.Equal(t, types.Signal(""), p.Parse("hello world"))
	assert.Equal(t, types.Signal(""), p.Parse(""))
}

func TestSignalParser_ParseWithMode_LoopMode(t *testing.T) {
	p := NewSignalParser()

	// No signal + loop mode = wait_input
	signal, clean := p.ParseWithMode("hello", true)
	assert.Equal(t, types.SignalWaitInput, signal)
	assert.Equal(t, "hello", clean)

	// Explicit signal in loop mode
	signal, clean = p.ParseWithMode("hello<continue/>", true)
	assert.Equal(t, types.SignalContinue, signal)
	assert.Equal(t, "hello", clean)
}

func TestSignalParser_ParseWithMode_NonLoopMode(t *testing.T) {
	p := NewSignalParser()

	// No signal + non-loop mode = continue
	signal, clean := p.ParseWithMode("hello", false)
	assert.Equal(t, types.SignalContinue, signal)
	assert.Equal(t, "hello", clean)

	// Explicit signal in non-loop mode
	signal, clean = p.ParseWithMode("hello</wait_input>", false)
	assert.Equal(t, types.SignalWaitInput, signal)
	assert.Equal(t, "hello", clean)
}

func TestSignalParser_ParseWithMode_CleansTags(t *testing.T) {
	p := NewSignalParser()

	// All tags should be stripped
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"continue suffix", "hello</continue>", "hello"},
		{"continue self-close", "hello<continue/>", "hello"},
		{"wait_input suffix", "hello</wait_input>", "hello"},
		{"goal_achieved", "hello<goal_achieved/>", "hello"},
		{"goal_failed suffix", "hello</goal_failed>", "hello"},
		{"goal_impossible suffix", "hello</goal_impossible>", "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, clean := p.ParseWithMode(tt.input, false)
			assert.Equal(t, tt.expected, clean)
		})
	}
}

// ============================================================
// GuardrailChecker Tests
// ============================================================

func TestGuardrailChecker_RegexMatchTriggersError(t *testing.T) {
	rules := []types.GuardrailRule{
		{Type: "regex", Pattern: "password\\s*=", Message: "do not include passwords"},
	}
	g := NewGuardrailChecker(rules, nil)

	err := g.CheckInput("my password = 12345")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not include passwords")
}

func TestGuardrailChecker_RegexNoMatchPasses(t *testing.T) {
	rules := []types.GuardrailRule{
		{Type: "regex", Pattern: "password\\s*=", Message: "do not include passwords"},
	}
	g := NewGuardrailChecker(rules, nil)

	err := g.CheckInput("hello world")
	assert.NoError(t, err)
}

func TestGuardrailChecker_ContainsMatchTriggers(t *testing.T) {
	rules := []types.GuardrailRule{
		{Type: "contains", Pattern: "malware", Message: "do not discuss malware"},
	}
	g := NewGuardrailChecker(nil, rules)

	err := g.CheckOutput("let me tell you about malware")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not discuss malware")
}

func TestGuardrailChecker_ContainsNoMatchPasses(t *testing.T) {
	rules := []types.GuardrailRule{
		{Type: "contains", Pattern: "malware", Message: "do not discuss malware"},
	}
	g := NewGuardrailChecker(nil, rules)

	err := g.CheckOutput("let me tell you about software")
	assert.NoError(t, err)
}

func TestGuardrailChecker_EmptyRulesPass(t *testing.T) {
	g := NewGuardrailChecker(nil, nil)

	assert.NoError(t, g.CheckInput("anything"))
	assert.NoError(t, g.CheckOutput("anything"))
}

func TestGuardrailChecker_InputAndOutputRules(t *testing.T) {
	inputRules := []types.GuardrailRule{
		{Type: "contains", Pattern: "hack", Message: "no hacking"},
	}
	outputRules := []types.GuardrailRule{
		{Type: "contains", Pattern: "secret", Message: "no secrets in output"},
	}
	g := NewGuardrailChecker(inputRules, outputRules)

	assert.Error(t, g.CheckInput("how to hack"))
	assert.NoError(t, g.CheckInput("hello"))
	assert.Error(t, g.CheckOutput("the secret is"))
	assert.NoError(t, g.CheckOutput("hello"))
}

// ============================================================
// HandoffRouter Tests
// ============================================================

func TestHandoffRouter_GetAgentExists(t *testing.T) {
	agents := map[string]types.AgentConfig{
		"coder": {Name: "coder", Handoffs: []string{"reviewer"}},
	}
	r := NewHandoffRouter(agents)

	agent, err := r.GetAgent("coder")
	require.NoError(t, err)
	assert.Equal(t, "coder", agent.Name)
}

func TestHandoffRouter_GetAgentNotFound(t *testing.T) {
	r := NewHandoffRouter(map[string]types.AgentConfig{})

	_, err := r.GetAgent("unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestHandoffRouter_CanHandoffValid(t *testing.T) {
	agents := map[string]types.AgentConfig{
		"coder":    {Name: "coder", Handoffs: []string{"reviewer"}},
		"reviewer": {Name: "reviewer"},
	}
	r := NewHandoffRouter(agents)

	assert.True(t, r.CanHandoff("coder", "reviewer"))
}

func TestHandoffRouter_CanHandoffInvalid(t *testing.T) {
	agents := map[string]types.AgentConfig{
		"coder": {Name: "coder", Handoffs: []string{"reviewer"}},
	}
	r := NewHandoffRouter(agents)

	assert.False(t, r.CanHandoff("coder", "tester"))
}

func TestHandoffRouter_CanHandoffSelfDenied(t *testing.T) {
	agents := map[string]types.AgentConfig{
		"coder": {Name: "coder", Handoffs: []string{"coder", "reviewer"}},
	}
	r := NewHandoffRouter(agents)

	assert.False(t, r.CanHandoff("coder", "coder"))
}

func TestHandoffRouter_CanHandoffFromAgentNotFound(t *testing.T) {
	r := NewHandoffRouter(map[string]types.AgentConfig{})

	assert.False(t, r.CanHandoff("unknown", "anyone"))
}

func TestHandoffRouter_BuildContext(t *testing.T) {
	r := NewHandoffRouter(nil)

	history := []types.Message{
		{Role: "user", Content: "hello"},
	}
	hc := r.BuildContext("do task", "some input", history)

	assert.Equal(t, "do task", hc.Task)
	assert.Equal(t, "some input", hc.Input)
	assert.Len(t, hc.History, 1)
}

// ============================================================
// HITLGate Tests
// ============================================================

func TestHITLGate_RequestAndSubmit(t *testing.T) {
	g := NewHITLGate(5 * time.Minute)

	req := types.HITLRequest{RequestID: "req-1"}

	// Submit in a goroutine
	go func() {
		time.Sleep(50 * time.Millisecond)
		g.SubmitResponse(types.HITLResponse{
			RequestID: "req-1",
			Approved:  true,
			Reason:    "looks good",
		})
	}()

	resp, err := g.RequestApproval(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, resp.Approved)
	assert.Equal(t, "looks good", resp.Reason)
}

func TestHITLGate_Timeout(t *testing.T) {
	g := NewHITLGate(100 * time.Millisecond)

	req := types.HITLRequest{RequestID: "req-2"}

	resp, err := g.RequestApproval(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, resp.Approved)
	assert.Equal(t, "approval timeout", resp.Reason)
}

func TestHITLGate_ContextCancel(t *testing.T) {
	g := NewHITLGate(5 * time.Minute)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	req := types.HITLRequest{RequestID: "req-3"}
	_, err := g.RequestApproval(ctx, req)
	assert.Error(t, err)
}

func TestHITLGate_PendingCount(t *testing.T) {
	g := NewHITLGate(5 * time.Minute)

	assert.Equal(t, 0, g.PendingCount())

	go func() {
		g.RequestApproval(context.Background(), types.HITLRequest{RequestID: "req-4"})
	}()
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 1, g.PendingCount())

	g.SubmitResponse(types.HITLResponse{RequestID: "req-4"})
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 0, g.PendingCount())
}

func TestHITLGate_SubmitNonExistent(t *testing.T) {
	g := NewHITLGate(5 * time.Minute)

	err := g.SubmitResponse(types.HITLResponse{RequestID: "nonexistent"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pending request")
}

// ============================================================
// HookExecutor Tests
// ============================================================

func TestHookExecutor_RegisterAndExecute(t *testing.T) {
	h := NewHookExecutor()

	called := false
	h.Register("on_start", func(ctx context.Context, payload types.HookPayload) error {
		called = true
		assert.Equal(t, "on_start", payload.Event)
		return nil
	})

	err := h.Execute(context.Background(), "on_start", types.HookPayload{Event: "on_start"})
	require.NoError(t, err)
	assert.True(t, called)
}

func TestHookExecutor_MultipleHooks(t *testing.T) {
	h := NewHookExecutor()

	count := 0
	h.Register("on_end", func(ctx context.Context, payload types.HookPayload) error {
		count++
		return nil
	})
	h.Register("on_end", func(ctx context.Context, payload types.HookPayload) error {
		count++
		return nil
	})

	err := h.Execute(context.Background(), "on_end", types.HookPayload{Event: "on_end"})
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestHookExecutor_NoHooksForEvent(t *testing.T) {
	h := NewHookExecutor()

	// Should not error when no hooks registered
	err := h.Execute(context.Background(), "nonexistent", types.HookPayload{})
	assert.NoError(t, err)
}

func TestHookExecutor_HookError(t *testing.T) {
	h := NewHookExecutor()

	h.Register("on_error", func(ctx context.Context, payload types.HookPayload) error {
		return assert.AnError
	})

	err := h.Execute(context.Background(), "on_error", types.HookPayload{})
	assert.Error(t, err)
}

func TestHookExecutor_HookConstants(t *testing.T) {
	// Verify constants exist
	assert.Equal(t, "on_start", HookOnStart)
	assert.Equal(t, "on_end", HookOnEnd)
	assert.Equal(t, "on_tool_start", HookOnToolStart)
	assert.Equal(t, "on_tool_end", HookOnToolEnd)
	assert.Equal(t, "on_handoff", HookOnHandoff)
	assert.Equal(t, "on_error", HookOnError)
}

// ============================================================
// StructuredOutputManager Tests
// ============================================================

func TestStructuredOutputManager_ParseValidJSON(t *testing.T) {
	m := NewStructuredOutputManager()

	schema := &types.StructuredOutput{
		Type: "json_schema",
		Schema: map[string]any{
			"name":     map[string]any{"type": "string", "required": true},
			"age":      map[string]any{"type": "number", "required": false},
		},
	}

	result, err := m.ParseAndValidate(`{"name": "Alice", "age": 30}`, schema)
	require.NoError(t, err)

	resultMap, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Alice", resultMap["name"])
	assert.Equal(t, float64(30), resultMap["age"])
}

func TestStructuredOutputManager_ParseInvalidJSON(t *testing.T) {
	m := NewStructuredOutputManager()

	schema := &types.StructuredOutput{Type: "json_schema", Schema: map[string]any{}}

	_, err := m.ParseAndValidate(`not json`, schema)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse structured output")
}

func TestStructuredOutputManager_NilSchemaReturnsRaw(t *testing.T) {
	m := NewStructuredOutputManager()

	result, err := m.ParseAndValidate("just text", nil)
	require.NoError(t, err)
	assert.Equal(t, "just text", result)
}

func TestStructuredOutputManager_MissingRequiredField(t *testing.T) {
	m := NewStructuredOutputManager()

	schema := &types.StructuredOutput{
		Type: "json_schema",
		Schema: map[string]any{
			"name": map[string]any{"type": "string", "required": true},
		},
	}

	_, err := m.ParseAndValidate(`{"age": 30}`, schema)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required field: name")
}

func TestStructuredOutputManager_ToProviderFormat(t *testing.T) {
	m := NewStructuredOutputManager()

	schema := &types.StructuredOutput{
		Type: "json_schema",
		Schema: map[string]any{
			"name": map[string]any{"type": "string"},
		},
	}

	result := m.ToProviderFormat(schema)
	require.NotNil(t, result)
	assert.Equal(t, "json_schema", result["type"])
	assert.NotNil(t, result["json_schema"])
}

func TestStructuredOutputManager_ToProviderFormatNil(t *testing.T) {
	m := NewStructuredOutputManager()

	result := m.ToProviderFormat(nil)
	assert.Nil(t, result)
}

func TestStructuredOutputManager_ValidateNonRequiredFieldMissing(t *testing.T) {
	m := NewStructuredOutputManager()

	schema := &types.StructuredOutput{
		Type: "json_schema",
		Schema: map[string]any{
			"name": map[string]any{"type": "string", "required": false},
		},
	}

	// Should not error when non-required field is missing
	result, err := m.ParseAndValidate(`{"age": 30}`, schema)
	require.NoError(t, err)
	assert.NotNil(t, result)
}

// ============================================================
// TurnLoop Tests (mock-based)
// ============================================================

type mockModelProvider struct {
	responses []types.ChatResponse
	callCount int
}

func (m *mockModelProvider) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	if m.callCount < len(m.responses) {
		resp := m.responses[m.callCount]
		m.callCount++
		return &resp, nil
	}
	return &types.ChatResponse{
		Text:  "default response",
		Usage: types.TokenUsage{TotalTokens: 10},
	}, nil
}

func (m *mockModelProvider) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	ch := make(chan types.ChatChunk, 1)
	go func() {
		defer close(ch)
		ch <- types.ChatChunk{Text: "stream response", Finished: true}
	}()
	return ch, nil
}

type mockToolExecutor struct{}

func (m *mockToolExecutor) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	return &types.ToolResult{
		Success: true,
		Content: "tool result for " + name,
	}, nil
}

type mockPromptRenderer struct {
	messages []types.Message
}

func (m *mockPromptRenderer) Render(agent types.AgentConfig, task types.TaskConfig, input string, rctx RenderContext) ([]types.Message, error) {
	return m.messages, nil
}

func TestTurnLoop_Run_SimpleResponse(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{
				Text:  "Hello! How can I help?",
				Usage: types.TokenUsage{TotalTokens: 50},
			},
		},
	}
	toolExec := &mockToolExecutor{}
	guardrail := NewGuardrailChecker(nil, nil)
	signalParser := NewSignalParser()
	hitl := NewHITLGate(5 * time.Minute)
	hooks := NewHookExecutor()
	prompt := &mockPromptRenderer{
		messages: []types.Message{{Role: "user", Content: "hello"}},
	}

	loop := NewTurnLoop(model, toolExec, guardrail, signalParser, hitl, hooks, prompt)

	agent := types.AgentConfig{
		Name: "test-agent",
		Loop: types.LoopConfig{MaxRounds: 5},
	}

	result, err := loop.Run(context.Background(), agent, types.TaskConfig{}, "hello")
	require.NoError(t, err)
	assert.Equal(t, "Hello! How can I help?", result.Raw)
	assert.Equal(t, types.SignalWaitInput, result.Signal) // maxRounds=5 > 1, loop mode defaults to wait_input
	assert.Equal(t, 50, result.Usage.TotalTokens)
}

func TestTurnLoop_Run_MaxRoundsDefault(t *testing.T) {
	// Agent with MaxRounds=0 should default to 3
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{Text: "round 1", ToolCalls: []types.ToolCall{{ID: "1", Name: "Read", Arguments: map[string]any{"file": "test.txt"}}}, Usage: types.TokenUsage{TotalTokens: 10}},
			{Text: "round 2", ToolCalls: []types.ToolCall{{ID: "2", Name: "Write", Arguments: map[string]any{"file": "test.txt", "content": "x"}}}, Usage: types.TokenUsage{TotalTokens: 10}},
			{Text: "round 3", ToolCalls: []types.ToolCall{{ID: "3", Name: "Read", Arguments: map[string]any{"file": "test.txt"}}}, Usage: types.TokenUsage{TotalTokens: 10}},
		},
	}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		NewGuardrailChecker(nil, nil),
		NewSignalParser(),
		NewHITLGate(5*time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	agent := types.AgentConfig{
		Name: "test-agent",
		Loop: types.LoopConfig{MaxRounds: 0}, // should default to 3
	}

	result, err := loop.Run(context.Background(), agent, types.TaskConfig{}, "hello")
	require.NoError(t, err)
	assert.Equal(t, types.SignalWaitInput, result.Signal) // loop mode
	assert.Equal(t, 30, result.Usage.TotalTokens)
}

func TestTurnLoop_Run_ContextCanceled(t *testing.T) {
	model := &mockModelProvider{}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		NewGuardrailChecker(nil, nil),
		NewSignalParser(),
		NewHITLGate(5*time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	agent := types.AgentConfig{
		Name: "test-agent",
		Loop: types.LoopConfig{MaxRounds: 1},
	}

	_, err := loop.Run(ctx, agent, types.TaskConfig{}, "hello")
	assert.Error(t, err)
}

func TestTurnLoop_Run_GuardrailBlocksInput(t *testing.T) {
	model := &mockModelProvider{}
	guardrail := NewGuardrailChecker(
		[]types.GuardrailRule{{Type: "contains", Pattern: "blocked", Message: "input blocked"}},
		nil,
	)
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		guardrail,
		NewSignalParser(),
		NewHITLGate(5*time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	agent := types.AgentConfig{
		Name: "test-agent",
		Loop: types.LoopConfig{MaxRounds: 5},
	}

	result, err := loop.Run(context.Background(), agent, types.TaskConfig{}, "this is blocked")
	require.NoError(t, err)
	assert.Contains(t, result.Error, "input blocked")
}

func TestTurnLoop_Run_ToolCallLoop(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{
				Text: "Let me read the file",
				ToolCalls: []types.ToolCall{
					{ID: "call-1", Name: "Read", Arguments: map[string]any{"file": "test.txt"}},
				},
				Usage: types.TokenUsage{TotalTokens: 100},
			},
			{
				Text:  "Done reading",
				Usage: types.TokenUsage{TotalTokens: 50},
			},
		},
	}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		NewGuardrailChecker(nil, nil),
		NewSignalParser(),
		NewHITLGate(5*time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	agent := types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Read", "Write"}},
		Loop:  types.LoopConfig{MaxRounds: 5},
	}

	result, err := loop.Run(context.Background(), agent, types.TaskConfig{}, "hello")
	require.NoError(t, err)
	assert.Equal(t, "Done reading", result.Raw)
	assert.Equal(t, 150, result.Usage.TotalTokens)
}

func TestTurnLoop_Run_BuildToolSchemas(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{Text: "ok", Usage: types.TokenUsage{TotalTokens: 10}},
		},
	}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		NewGuardrailChecker(nil, nil),
		NewSignalParser(),
		NewHITLGate(5*time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	// Verify buildToolSchemas returns correct schemas
	agent := types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Read", "Write"}},
		Loop:  types.LoopConfig{MaxRounds: 1},
	}

	_, err := loop.Run(context.Background(), agent, types.TaskConfig{}, "hello")
	require.NoError(t, err)
}

func TestTurnLoop_Run_SignalInResponse(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{
				Text:  "Task completed successfully</goal_achieved>",
				Usage: types.TokenUsage{TotalTokens: 30},
			},
		},
	}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		NewGuardrailChecker(nil, nil),
		NewSignalParser(),
		NewHITLGate(5*time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	agent := types.AgentConfig{
		Name: "test-agent",
		Loop: types.LoopConfig{MaxRounds: 5},
	}

	result, err := loop.Run(context.Background(), agent, types.TaskConfig{}, "hello")
	require.NoError(t, err)
	assert.Equal(t, types.SignalGoalAchieved, result.Signal)
	assert.Equal(t, "Task completed successfully", result.Raw)
}
