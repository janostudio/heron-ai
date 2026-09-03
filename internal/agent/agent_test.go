package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================
// RouteParser Tests
// ============================================================

func TestRouteParser_Parse_Proceed(t *testing.T) {
	p := NewRouteParser()

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
			assert.Equal(t, types.NextProceed, p.Parse(tt.input))
		})
	}
}

func TestRouteParser_Parse_WaitInputEndsAsProceed(t *testing.T) {
	p := NewRouteParser()

	// The wait_input marker still ends the model reply, but a finished turn
	// is always resumable now, so it no longer selects a special route.
	tests := []struct {
		name  string
		input string
	}{
		{"suffix tag", "some text</wait_input>"},
		{"self-closing tag", "some text<wait_input/>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, types.NextProceed, p.Parse(tt.input))
		})
	}
}

func TestRouteParser_Parse_GoalAchievedEndsAsProceed(t *testing.T) {
	p := NewRouteParser()

	assert.Equal(t, types.NextProceed, p.Parse("done</goal_achieved>"))
	assert.Equal(t, types.NextProceed, p.Parse("done<goal_achieved/>"))
}

func TestRouteParser_Parse_Fail(t *testing.T) {
	p := NewRouteParser()

	assert.Equal(t, types.NextFail, p.Parse("failed</goal_failed>"))
	assert.Equal(t, types.NextFail, p.Parse("failed<goal_failed/>"))
}

func TestRouteParser_Parse_Impossible(t *testing.T) {
	p := NewRouteParser()

	assert.Equal(t, types.NextFail, p.Parse("impossible</goal_impossible>"))
	assert.Equal(t, types.NextFail, p.Parse("impossible<goal_impossible/>"))
}

func TestRouteParser_Parse_NoAction(t *testing.T) {
	p := NewRouteParser()

	assert.Equal(t, types.NextAction(""), p.Parse("hello world"))
	assert.Equal(t, types.NextAction(""), p.Parse(""))
}

func TestRouteParser_ParseWithMode_LoopMode(t *testing.T) {
	p := NewRouteParser()

	// A plain response is a completed answer even when the Agent has a
	// multi-round loop. Waiting for input requires an explicit route or Tool.
	action, clean := p.ParseWithMode("hello", true)
	assert.Equal(t, types.NextProceed, action)
	assert.Equal(t, "hello", clean)

	// Explicit action in loop mode
	action, clean = p.ParseWithMode("hello<continue/>", true)
	assert.Equal(t, types.NextProceed, action)
	assert.Equal(t, "hello", clean)
}

func TestRouteParser_ParseWithMode_NonLoopMode(t *testing.T) {
	p := NewRouteParser()

	// No action + non-loop mode = proceed
	action, clean := p.ParseWithMode("hello", false)
	assert.Equal(t, types.NextProceed, action)
	assert.Equal(t, "hello", clean)

	// Explicit action in non-loop mode: the wait_input marker ends the turn
	// like a normal completed answer.
	action, clean = p.ParseWithMode("hello</wait_input>", false)
	assert.Equal(t, types.NextProceed, action)
	assert.Equal(t, "hello", clean)
}

func TestRouteParser_ParseWithMode_CleansTags(t *testing.T) {
	p := NewRouteParser()

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
// HITLGate Tests
// ============================================================

func TestHITLGate_RequestAndSubmit(t *testing.T) {
	g := NewHITLGate(5 * time.Minute)

	req := types.HITLRequest{RequestID: "req-1"}

	// Submit in a goroutine
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = g.SubmitResponse(types.HITLResponse{
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
		_, _ = g.RequestApproval(context.Background(), types.HITLRequest{RequestID: "req-4"})
	}()
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 1, g.PendingCount())

	_ = g.SubmitResponse(types.HITLResponse{RequestID: "req-4"})
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
	assert.Equal(t, "on_error", HookOnError)
}

func TestHookExecutor_ExecuteSetsPayloadEventAndHonorsContext(t *testing.T) {
	h := NewHookExecutor()
	var payload types.HookPayload
	h.Register(HookOnStart, func(ctx context.Context, got types.HookPayload) error {
		payload = got
		return nil
	})

	err := h.Execute(context.Background(), HookOnStart, types.HookPayload{})
	require.NoError(t, err)
	assert.Equal(t, HookOnStart, payload.Event)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = h.Execute(ctx, HookOnStart, types.HookPayload{})
	assert.ErrorIs(t, err, context.Canceled)
}

// ============================================================
// StructuredOutputManager Tests
// ============================================================

func TestStructuredOutputManager_ParseValidJSON(t *testing.T) {
	m := NewStructuredOutputManager()

	schema := &types.StructuredOutput{
		Type: "json_schema",
		Schema: map[string]any{
			"name": map[string]any{"type": "string", "required": true},
			"age":  map[string]any{"type": "number", "required": false},
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

func TestStructuredOutputManager_ParseMarkdownFencedJSON(t *testing.T) {
	m := NewStructuredOutputManager()
	schema := &types.StructuredOutput{
		Type: "json_schema",
		Schema: map[string]any{
			"reply": map[string]any{"type": "string", "required": true},
			"next":  map[string]any{"type": "object", "required": true},
		},
	}

	result, err := m.ParseAndValidate("结论如下：\n\n```json\n"+
		`{"message_to_user":"已收到","next":{"action":"activate","teams":["diagnose"]}}`+
		"\n```\n", schema)
	require.NoError(t, err)

	resultMap, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "已收到", resultMap["reply"])
	assert.NotNil(t, resultMap["next"])
}

func TestStructuredOutputManager_ParseJSONAfterExplanation(t *testing.T) {
	m := NewStructuredOutputManager()
	schema := &types.StructuredOutput{
		Type: "json_schema",
		Schema: map[string]any{
			"reply": map[string]any{"type": "string", "required": true},
		},
	}

	result, err := m.ParseAndValidate(
		`我会继续处理。
{"reply":"继续","next":{"action":"proceed"}}`,
		schema,
	)
	require.NoError(t, err)
	assert.Equal(t, "继续", result.(map[string]any)["reply"])
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
	responses  []types.ChatResponse
	callCount  int
	attempts   int
	err        error
	lastConfig types.ModelConfig
}

func (m *mockModelProvider) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	m.attempts++
	m.lastConfig = config
	if m.err != nil {
		err := m.err
		m.err = nil
		return nil, err
	}
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

func TestTurnLoop_Run_HookLifecycle(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{
				Text: "reading",
				ToolCalls: []types.ToolCall{{
					ID:        "call-1",
					Name:      "Read",
					Arguments: map[string]any{"file": "test.txt"},
				}},
			},
			{Text: "done"},
		},
	}
	hooks := NewHookExecutor()
	var events []string
	var payloads []types.HookPayload
	for _, event := range []string{HookOnStart, HookOnToolStart, HookOnToolEnd, HookOnEnd} {
		event := event
		hooks.Register(event, func(_ context.Context, payload types.HookPayload) error {
			events = append(events, event)
			payloads = append(payloads, payload)
			return nil
		})
	}

	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		hooks,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{
		CallID:        "call-agent",
		AgentID:       "assistant",
		AgentTurnID:   "agent-turn-1",
		ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{
		HookOnStart,
		HookOnToolStart,
		HookOnToolEnd,
		HookOnEnd,
	}, events)
	require.Len(t, payloads, 4)
	assert.Equal(t, "assistant", payloads[0].AgentID)
	assert.Equal(t, "agent-turn-1", payloads[0].AgentTurnID)
	assert.Equal(t, "Read", payloads[1].ToolName)
	assert.Equal(t, "call-1", payloads[1].ToolCallID)
	assert.NotNil(t, payloads[2].ToolResult)
	assert.Equal(t, HookOnEnd, payloads[3].Event)
}

func TestTurnLoop_Run_HookErrorStopsBeforeModel(t *testing.T) {
	model := &mockModelProvider{}
	hooks := NewHookExecutor()
	hooks.Register(HookOnStart, func(context.Context, types.HookPayload) error {
		return assert.AnError
	})
	var errorHookCalled bool
	hooks.Register(HookOnError, func(context.Context, types.HookPayload) error {
		errorHookCalled = true
		return nil
	})

	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		hooks,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)
	result, err := loop.Run(context.Background(), types.AgentConfig{Loop: types.LoopConfig{MaxRounds: 1}}, types.AgentRequest{})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Error, assert.AnError.Error())
	assert.True(t, errorHookCalled)
	assert.Equal(t, 0, model.callCount)
}

func TestContextManager_PreservesCanonicalAndCompactsActiveMessages(t *testing.T) {
	manager := NewContextManagerWithEstimator(
		types.ContextConfig{
			MaxInputTokens:      100,
			TargetRatio:         0.40,
			CompactionThreshold: 0.50,
			HardLimitRatio:      0.80,
			OutputReserveRatio:  0,
			MaxToolOutputChars:  24,
		},
		charCountEstimator{},
	)
	require.NoError(t, manager.AddMessage(types.Message{Role: "system", Content: "sys"}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "user", Content: "first"}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "assistant", Content: "answer"}))
	require.NoError(t, manager.AddMessage(types.Message{
		Role:      "assistant",
		Content:   "tool request",
		ToolCalls: []types.ToolCall{{ID: "tool-1", Name: "Read"}},
	}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "tool", ToolCallID: "tool-1", Content: strings.Repeat("x", 100)}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "user", Content: "latest request"}))

	assert.Len(t, manager.CanonicalMessages(), 6)
	assert.Less(t, len(manager.Messages()), len(manager.CanonicalMessages()))
	assert.Contains(t, manager.Messages()[0].Content, "sys")
	assert.Contains(t, manager.Messages()[len(manager.Messages())-1].Content, "latest request")
	assert.LessOrEqual(t, manager.EstimateTokens(), 80)
	for _, message := range manager.Messages() {
		if strings.Contains(message.Content, "## Compacted Agent Context") {
			assert.Equal(t, "user", message.Role)
		}
	}
}

func TestContextManagerCompactionKeepsStableSystemAndRecentToolGroup(t *testing.T) {
	manager := NewContextManagerWithEstimator(
		types.ContextConfig{
			MaxInputTokens:      120,
			TargetRatio:         0.35,
			CompactionThreshold: 0.45,
			HardLimitRatio:      0.75,
			OutputReserveRatio:  0,
			MaxToolOutputChars:  24,
		},
		charCountEstimator{},
	)
	require.NoError(t, manager.AddMessage(types.Message{Role: "system", Content: "stable instructions"}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "user", Content: "initial task"}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "assistant", Content: "old answer"}))
	require.NoError(t, manager.AddMessage(types.Message{
		Role: "assistant", Content: "old tool call",
		ToolCalls: []types.ToolCall{{ID: "old", Name: "Read"}},
	}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "tool", ToolCallID: "old", Content: strings.Repeat("old-result ", 8)}))
	require.NoError(t, manager.AddMessage(types.Message{
		Role: "assistant", Content: "latest tool call",
		ToolCalls: []types.ToolCall{{ID: "latest", Name: "Read"}},
	}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "tool", ToolCallID: "latest", Content: "latest-result"}))

	active := manager.Messages()
	require.Greater(t, manager.CompactionCount(), 0)
	require.Equal(t, "system", active[0].Role)
	require.Contains(t, active[0].Content, "stable instructions")
	require.Contains(t, active[len(active)-1].Content, "late")
	require.NotContains(t, strings.Join(messageContents(active), "\n"), "old-result old-result old-result")

	for i, message := range active {
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			require.Less(t, i+1, len(active))
			require.Equal(t, "tool", active[i+1].Role)
			require.Equal(t, message.ToolCalls[0].ID, active[i+1].ToolCallID)
		}
		if strings.Contains(message.Content, "## Compacted Agent Context") {
			require.Equal(t, "user", message.Role)
		}
	}
}

func messageContents(messages []types.Message) []string {
	result := make([]string, 0, len(messages))
	for _, message := range messages {
		result = append(result, message.Content)
	}
	return result
}

func TestContextManager_ToolOutputTruncatedOnlyInActiveContext(t *testing.T) {
	manager := NewContextManagerWithEstimator(
		types.ContextConfig{MaxInputTokens: 100, MaxToolOutputChars: 8},
		charCountEstimator{},
	)
	content := strings.Repeat("z", 20)
	require.NoError(t, manager.AddMessage(types.Message{Role: "tool", Content: content}))
	assert.Equal(t, content, manager.CanonicalMessages()[0].Content)
	assert.LessOrEqual(t, len(manager.Messages()[0].Content), 8)
}

func TestContextManager_ContextCancel(t *testing.T) {
	manager := NewContextManagerWithEstimator(
		types.ContextConfig{MaxInputTokens: 20, CompactionThreshold: 0.5, HardLimitRatio: 0.9},
		charCountEstimator{},
	)
	require.NoError(t, manager.AddMessage(types.Message{Role: "user", Content: "1234567890"}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, manager.Compact(ctx), context.Canceled)
}

func TestContextManager_StatsReportsActiveAndCanonicalContext(t *testing.T) {
	manager := NewContextManager(types.ContextConfig{})
	manager.SetToolSchemas([]types.JSONSchema{{Name: "Read", Type: "object"}})
	require.NoError(t, manager.AddMessage(types.Message{Role: "system", Content: "stable"}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "user", Content: "input"}))

	stats := manager.Stats()
	assert.Equal(t, 2, stats.MessageCount)
	assert.Equal(t, 2, stats.CanonicalCount)
	assert.Equal(t, 1, stats.ToolSchemaCount)
	assert.Greater(t, stats.EstimatedTokens, 0)
}

func TestContextManager_MicrocompactsOldToolOutput(t *testing.T) {
	manager := NewContextManagerWithEstimator(
		types.ContextConfig{
			MaxInputTokens:             1000,
			MicrocompactThresholdChars: 100,
			MicrocompactMaxChars:       80,
			RecentMessageGroups:        1,
		},
		charCountEstimator{},
	)
	require.NoError(t, manager.AddMessage(types.Message{Role: "system", Content: "stable"}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "user", Content: "task"}))
	require.NoError(t, manager.AddMessage(types.Message{
		Role: "assistant", ToolCalls: []types.ToolCall{{ID: "old", Name: "Bash"}},
	}))
	require.NoError(t, manager.AddMessage(types.Message{
		Role: "tool", ToolCallID: "old", ToolName: "Bash",
		Content: strings.Repeat("line-1\nline-2\nline-3\nline-4\nline-5\nline-6\n", 8),
	}))
	require.NoError(t, manager.AddMessage(types.Message{
		Role: "assistant", ToolCalls: []types.ToolCall{{ID: "new", Name: "Read"}},
	}))
	require.NoError(t, manager.AddMessage(types.Message{
		Role: "tool", ToolCallID: "new", ToolName: "Read", Content: "latest",
	}))

	active := manager.Messages()
	require.Greater(t, manager.Stats().MicrocompactCount, 0)
	require.Contains(t, strings.Join(messageContents(active), "\n"), "microcompacted")
	require.Contains(t, strings.Join(messageContents(active), "\n"), "latest")
}

func TestIsContextLimitError(t *testing.T) {
	require.True(t, isContextLimitError(fmt.Errorf("maximum context length exceeded")))
	require.True(t, isContextLimitError(fmt.Errorf("too many tokens")))
	require.False(t, isContextLimitError(fmt.Errorf("network unavailable")))
}

func TestTurnLoop_RetriesOnceAfterProviderContextLimit(t *testing.T) {
	model := &mockModelProvider{
		err: errors.New("maximum context length exceeded"),
		responses: []types.ChatResponse{{
			Text:  "recovered",
			Usage: types.TokenUsage{TotalTokens: 10},
		}},
	}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{
			{Role: "system", Content: "stable"},
			{Role: "user", Content: "task"},
		}},
	)
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Loop:    types.LoopConfig{MaxRounds: 1},
		Context: types.ContextConfig{Summarizer: "mechanical"},
	}, types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, "recovered", result.Reply)
	require.Equal(t, 2, model.attempts)
	require.Equal(t, 1, model.callCount)
	require.Len(t, result.Requests, 2)
	require.True(t, result.Requests[1].Compacted)
}

func TestBudgetTracker_EnforcesIndependentLimits(t *testing.T) {
	now := time.Now().UTC()
	budget, err := newBudgetTracker(types.AgentBudget{
		MaxModelRounds: 2,
		MaxToolCalls:   1,
		MaxInputTokens: 10,
	}, 0, now)
	require.NoError(t, err)
	require.NoError(t, budget.beforeModel(context.Background()))
	budget.usage.ModelRounds++
	budget.usage.InputTokens = 11
	require.ErrorContains(t, budget.checkUsage(), "max_input_tokens")
	require.ErrorContains(t, budget.beforeTool(context.Background(), 2), "max_tool_calls")
	require.NoError(t, budget.beforeModel(context.Background()))
	budget.usage.ModelRounds++
	require.ErrorContains(t, budget.beforeModel(context.Background()), "max_model_rounds")
}

func TestBudgetTracker_ParsesWallTime(t *testing.T) {
	_, err := newBudgetTracker(types.AgentBudget{MaxWallTime: "not-a-duration"}, 0, time.Now())
	require.ErrorContains(t, err, "max_wall_time")
}

func TestFileCheckpointStore_SaveLoadDelete(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewFileCheckpointStore(files)
	checkpoint := types.AgentCheckpoint{
		Version: 1,
		ID:      "agent-turn-1",
		Status:  types.TurnWaitingInput,
		Messages: []types.Message{{
			Role: "user", Content: "hello",
		}},
		BudgetUsage: types.AgentBudgetUsage{ModelRounds: 1},
	}
	require.NoError(t, store.Save(context.Background(), checkpoint))
	loaded, err := store.Load(context.Background(), checkpoint.ID)
	require.NoError(t, err)
	require.Equal(t, checkpoint.ID, loaded.ID)
	require.Equal(t, checkpoint.Messages[0].Content, loaded.Messages[0].Content)
	require.NoError(t, store.Delete(context.Background(), checkpoint.ID))
	_, err = store.Load(context.Background(), checkpoint.ID)
	assert.ErrorIs(t, err, ErrCheckpointNotFound)
}

func TestTurnLoop_Run_BudgetLimitReturnsCheckpoint(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{{
			Text:  "first",
			Usage: types.TokenUsage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10},
			ToolCalls: []types.ToolCall{{
				ID: "tool-1", Name: "Read", Arguments: map[string]any{"file": "a.txt"},
			}},
		}},
	}
	store := &memoryCheckpointStore{}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)
	loop.SetCheckpointStore(store)
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Tools:  types.ToolConfig{Builtin: []string{"Read"}},
		Budget: types.AgentBudget{MaxToolCalls: 0, MaxOutputTokens: 1},
		Loop:   types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{AgentID: "assistant", AgentTurnID: "turn-1"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Error, "max_output_tokens")
	require.NotNil(t, result.Checkpoint)
	assert.Equal(t, "turn-1", result.Checkpoint.ID)
}

func TestTurnLoop_Run_AskUserQuestionSavesCheckpointAndResumes(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{{
			Text: "ask",
			ToolCalls: []types.ToolCall{{
				ID:        "ask-1",
				Name:      "AskUserQuestion",
				Arguments: map[string]any{"question": "Continue?"},
			}},
		}, {
			Text: "resumed answer",
		}},
	}
	store := &memoryCheckpointStore{}
	tools := &recordingToolExecutor{
		result: &types.ToolResult{
			Success:      true,
			Content:      `{"question":"Continue?"}`,
			PendingInput: &types.AgentPendingInput{Question: "Continue?"},
		},
	}
	loop := NewTurnLoop(
		model, tools, nil, NewRouteParser(), nil, NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)
	loop.SetCheckpointStore(store)
	agent := types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"AskUserQuestion"}},
		Loop:  types.LoopConfig{MaxRounds: 3},
	}
	first, err := loop.Run(context.Background(), agent, types.AgentRequest{
		AgentID: "assistant", AgentTurnID: "turn-resume",
	})
	require.NoError(t, err)
	require.NotNil(t, first.Checkpoint)
	assert.Equal(t, types.TurnWaitingInput, first.Status)

	model.responses[1] = types.ChatResponse{Text: "resumed answer"}
	second, err := loop.Run(context.Background(), agent, types.AgentRequest{
		AgentID: "assistant", AgentTurnID: "turn-resume",
		ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "yes"}}, ResumeCheckpointID: first.Checkpoint.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, "resumed answer", second.Reply)
	assert.Equal(t, 2, model.callCount)
	_, err = store.Load(context.Background(), first.Checkpoint.ID)
	assert.ErrorIs(t, err, ErrCheckpointNotFound)
}

func TestFileToolTaskStoreAndAsyncExecutor(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewFileToolTaskStore(files)
	executor := &recordingToolExecutor{result: &types.ToolResult{Success: true, Content: "done"}}
	async := NewAsyncToolExecutor(store, executor)

	require.NoError(t, async.Start(context.Background(), types.ToolTask{
		ID:         "task-1",
		ToolCallID: "call-1",
		ToolName:   "Read",
		Arguments:  map[string]any{"file": "a.txt"},
	}))

	require.Eventually(t, func() bool {
		task, err := store.Load(context.Background(), "task-1")
		return err == nil && task.Status == types.ToolTaskCompleted && task.Result != nil
	}, time.Second, 10*time.Millisecond)

	task, err := store.Load(context.Background(), "task-1")
	require.NoError(t, err)
	assert.Equal(t, types.ToolTaskCompleted, task.Status)
	assert.Equal(t, "done", task.Result.Content)
}

func TestFileToolTaskStoreProgressSubscription(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewFileToolTaskStore(files)
	require.NoError(t, store.Save(context.Background(), types.ToolTask{
		ID: "task-progress", Status: types.ToolTaskRunning,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, err := store.Subscribe(ctx, "task-progress")
	require.NoError(t, err)
	initial := <-updates
	assert.Equal(t, float64(0), initial.Progress)

	require.NoError(t, store.UpdateProgress(context.Background(), "task-progress", 0.45, "running build"))
	update := <-updates
	assert.InDelta(t, 0.45, update.Progress, 0.001)
	assert.Equal(t, "running build", update.Message)

	require.NoError(t, store.Save(context.Background(), types.ToolTask{
		ID: "task-progress", Status: types.ToolTaskCompleted,
		Progress: 1, Message: "done", UpdatedAt: time.Now().UTC(),
	}))
	final := <-updates
	assert.Equal(t, types.ToolTaskCompleted, final.Status)
}

func TestAsyncToolExecutorCompletionHandlerIsIdempotent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewFileToolTaskStore(files)
	async := NewAsyncToolExecutor(store, &recordingToolExecutor{
		result: &types.ToolResult{Success: true, Content: "done"},
	})
	done := make(chan types.ToolTask, 2)
	async.SetCompletionHandler(func(_ context.Context, task types.ToolTask) {
		done <- task
	})

	require.NoError(t, async.Start(context.Background(), types.ToolTask{
		ID: "task-completion", ToolName: "Bash",
	}))
	select {
	case task := <-done:
		assert.Equal(t, types.ToolTaskCompleted, task.Status)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completion handler")
	}
	select {
	case duplicate := <-done:
		t.Fatalf("completion handler called twice: %#v", duplicate)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAsyncToolExecutorRecoverDoesNotReplayUnsafeRunningTask(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewFileToolTaskStore(files)
	now := time.Now().UTC()
	require.NoError(t, store.Save(context.Background(), types.ToolTask{
		ID:          "task-running",
		ToolCallID:  "call-1",
		ToolName:    "Bash",
		Status:      types.ToolTaskRunning,
		RestartSafe: false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))
	executor := &recordingToolExecutor{}
	async := NewAsyncToolExecutor(store, executor)
	require.NoError(t, async.Recover(context.Background()))
	task, err := store.Load(context.Background(), "task-running")
	require.NoError(t, err)
	assert.Equal(t, types.ToolTaskFailed, task.Status)
	assert.Contains(t, task.Error, "process restart")
	assert.Empty(t, executor.names)
}

func TestRecoverCheckpointsReportsReadyAndOrphanedTasks(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	checkpoints := NewFileCheckpointStore(files)
	tasks := NewFileToolTaskStore(files)
	now := time.Now().UTC()
	require.NoError(t, tasks.Save(context.Background(), types.ToolTask{
		ID: "task-ready", Status: types.ToolTaskCompleted, UpdatedAt: now,
	}))
	require.NoError(t, checkpoints.Save(context.Background(), types.AgentCheckpoint{
		ID: "cp-ready", Status: types.TurnWaitingTool,
		PendingTool: &types.AgentPendingTool{TaskID: "task-ready"},
	}))
	require.NoError(t, checkpoints.Save(context.Background(), types.AgentCheckpoint{
		ID: "cp-orphan", Status: types.TurnWaitingTool,
		PendingTool: &types.AgentPendingTool{TaskID: "missing-task"},
	}))
	report, err := RecoverCheckpoints(context.Background(), checkpoints, tasks)
	require.NoError(t, err)
	assert.Equal(t, 2, report.Total)
	assert.Equal(t, 1, report.ReadyTasks)
	assert.Contains(t, report.Orphaned, "cp-orphan")
}

func TestTurnLoop_Run_AsyncToolWaitsAndResumes(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{Text: "start", ToolCalls: []types.ToolCall{{
				ID: "task-call", Name: "Bash", Arguments: map[string]any{"command": "echo ok"},
			}}},
			{Text: "finished"},
		},
	}
	files := storage.NewFileStore(t.TempDir())
	tasks := NewFileToolTaskStore(files)
	async := NewAsyncToolExecutor(tasks, &recordingToolExecutor{
		result: &types.ToolResult{Success: true, Content: "command output"},
	})
	checkpoints := NewFileCheckpointStore(files)
	loop := NewTurnLoop(
		model, &recordingToolExecutor{}, nil, NewRouteParser(), nil,
		NewHookExecutor(), &mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "run"}}},
	)
	loop.SetCheckpointStore(checkpoints)
	loop.SetTaskRunner(async)

	agentConfig := types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"Bash"}},
		Loop:  types.LoopConfig{MaxRounds: 3, AsyncTools: []string{"Bash"}},
	}
	first, err := loop.Run(context.Background(), agentConfig, types.AgentRequest{
		AgentID: "assistant", AgentTurnID: "turn-async",
	})
	require.NoError(t, err)
	require.NotNil(t, first.Checkpoint)
	assert.Equal(t, types.TurnWaitingTool, first.Status)
	require.NotEmpty(t, first.TaskID)

	require.Eventually(t, func() bool {
		task, loadErr := tasks.Load(context.Background(), first.TaskID)
		return loadErr == nil && task.Status == types.ToolTaskCompleted
	}, time.Second, 10*time.Millisecond)

	second, err := loop.Run(context.Background(), agentConfig, types.AgentRequest{
		AgentID: "assistant", AgentTurnID: "turn-async",
		ResumeCheckpointID: first.Checkpoint.ID, ResumeTaskID: first.TaskID,
	})
	require.NoError(t, err)
	assert.Equal(t, "finished", second.Reply)
}

type memoryCheckpointStore struct {
	items map[string]types.AgentCheckpoint
}

func (s *memoryCheckpointStore) List(_ context.Context) ([]types.AgentCheckpoint, error) {
	result := make([]types.AgentCheckpoint, 0, len(s.items))
	for _, checkpoint := range s.items {
		result = append(result, checkpoint)
	}
	return result, nil
}

func (s *memoryCheckpointStore) Save(_ context.Context, checkpoint types.AgentCheckpoint) error {
	if s.items == nil {
		s.items = make(map[string]types.AgentCheckpoint)
	}
	s.items[checkpoint.ID] = checkpoint
	return nil
}

func (s *memoryCheckpointStore) Load(_ context.Context, id string) (*types.AgentCheckpoint, error) {
	checkpoint, ok := s.items[id]
	if !ok {
		return nil, ErrCheckpointNotFound
	}
	return &checkpoint, nil
}

func (s *memoryCheckpointStore) Delete(_ context.Context, id string) error {
	delete(s.items, id)
	return nil
}

type charCountEstimator struct{}

func (charCountEstimator) EstimateMessages(messages []types.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content) + len(message.Role) + len(message.ToolCallID)
		for _, call := range message.ToolCalls {
			total += len(call.ID) + len(call.Name)
		}
	}
	return total
}

func (charCountEstimator) EstimateTools([]types.JSONSchema) int { return 0 }

func (m *mockPromptRenderer) Render(agent types.AgentConfig, req types.AgentRequest, rctx RenderContext) ([]types.Message, error) {
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
	routeParser := NewRouteParser()
	hitl := NewHITLGate(5 * time.Minute)
	hooks := NewHookExecutor()
	prompt := &mockPromptRenderer{
		messages: []types.Message{{Role: "user", Content: "hello"}},
	}

	loop := NewTurnLoop(model, toolExec, guardrail, routeParser, hitl, hooks, prompt)

	agent := types.AgentConfig{
		Name: "test-agent",
		Loop: types.LoopConfig{MaxRounds: 5},
	}

	result, err := loop.Run(context.Background(), agent, types.AgentRequest{ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}}})
	require.NoError(t, err)
	assert.Equal(t, "Hello! How can I help?", result.Reply)
	assert.Equal(t, types.NextProceed, result.Next.Action)
	assert.Equal(t, 50, result.Usage.TotalTokens)
}

func TestTurnLoop_Run_MaxAgentRoundsLimit(t *testing.T) {
	// The runtime-wide limit is applied to one AgentTurn. This test uses 3
	// explicitly so the mock can exercise the limit without running 200 rounds.
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
		NewRouteParser(),
		NewHITLGate(5*time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	agent := types.AgentConfig{Name: "test-agent"}

	result, err := loop.Run(context.Background(), agent, types.AgentRequest{
		ContextBlocks:  []types.ContextBlock{{Kind: "input", Text: "hello"}},
		MaxAgentRounds: 3,
	})
	require.NoError(t, err)
	assert.Equal(t, types.NextProceed, result.Next.Action)
	assert.Equal(t, 30, result.Usage.TotalTokens)
}

func TestTurnLoop_Run_ContextCanceled(t *testing.T) {
	model := &mockModelProvider{}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		NewGuardrailChecker(nil, nil),
		NewRouteParser(),
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

	_, err := loop.Run(ctx, agent, types.AgentRequest{ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}}})
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
		NewRouteParser(),
		NewHITLGate(5*time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	agent := types.AgentConfig{
		Name: "test-agent",
		Loop: types.LoopConfig{MaxRounds: 5},
	}

	result, err := loop.Run(context.Background(), agent, types.AgentRequest{ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "this is blocked"}}})
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
		NewRouteParser(),
		NewHITLGate(5*time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	agent := types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Read", "Write"}},
		Loop:  types.LoopConfig{MaxRounds: 5},
	}

	result, err := loop.Run(context.Background(), agent, types.AgentRequest{ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}}})
	require.NoError(t, err)
	assert.Equal(t, "Done reading", result.Reply)
	assert.Equal(t, 150, result.Usage.TotalTokens)
}

func TestTurnLoop_Run_RejectsToolOutsideAgentAllowlist(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{
				Text: "try tool",
				ToolCalls: []types.ToolCall{{
					ID:        "call-1",
					Name:      "Write",
					Arguments: map[string]any{"file": "x", "content": "y"},
				}},
				Usage: types.TokenUsage{TotalTokens: 10},
			},
			{
				Text:  "done",
				Usage: types.TokenUsage{TotalTokens: 10},
			},
		},
	}
	toolExec := &recordingToolExecutor{}
	loop := NewTurnLoop(
		model,
		toolExec,
		NewGuardrailChecker(nil, nil),
		NewRouteParser(),
		NewHITLGate(time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	result, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}}})
	require.NoError(t, err)
	require.Equal(t, 2, model.callCount)
	require.Empty(t, toolExec.names)
	require.Contains(t, result.Reply, "done")
}

type recordingToolExecutor struct {
	names  []string
	result *types.ToolResult
}

func (e *recordingToolExecutor) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	e.names = append(e.names, name)
	if e.result != nil {
		return e.result, nil
	}
	return &types.ToolResult{Success: true, Content: "ok"}, nil
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
		NewRouteParser(),
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

	_, err := loop.Run(context.Background(), agent, types.AgentRequest{ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}}})
	require.NoError(t, err)
}

func TestTurnLoop_BuildToolSchemasUsesDeterministicSortedUniqueOrder(t *testing.T) {
	loop := NewTurnLoop(
		&mockModelProvider{responses: []types.ChatResponse{{Text: "ok"}}},
		&mockToolExecutor{},
		NewGuardrailChecker(nil, nil),
		NewRouteParser(),
		NewHITLGate(5*time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{
			Builtin: []string{"Write", "Read", "Write", "Bash"},
		},
	})

	require.Equal(t, []string{"Bash", "Read", "Write"}, []string{
		schemas[0].Name,
		schemas[1].Name,
		schemas[2].Name,
	})
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
		NewRouteParser(),
		NewHITLGate(5*time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	agent := types.AgentConfig{
		Name: "test-agent",
		Loop: types.LoopConfig{MaxRounds: 5},
	}

	result, err := loop.Run(context.Background(), agent, types.AgentRequest{ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}}})
	require.NoError(t, err)
	// The goal_achieved marker ends the reply; the turn ends like any normal
	// answer and stays resumable, so the route is a plain proceed.
	assert.Equal(t, types.NextProceed, result.Next.Action)
	assert.Equal(t, "Task completed successfully", result.Reply)
}
