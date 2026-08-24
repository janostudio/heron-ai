package observability

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogger_LevelsAndJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(LogInfo, &buf)
	logger.SetRunID("fs-123")
	logger.Info("test message", map[string]any{"key": "value"})

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "test message", entry["msg"])
	assert.Equal(t, "info", entry["level"])
	assert.Equal(t, "value", entry["key"])
	assert.Equal(t, "fs-123", entry["run_id"])
}

func TestEventBus_PublishSubscribe(t *testing.T) {
	bus := NewEventBus(10)
	ch := make(chan Event, 10)
	bus.Subscribe("test.event", ch, 0)
	bus.Publish(NewBaseEvent("test.event"))

	select {
	case received := <-ch:
		assert.Equal(t, "test.event", received.Type())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBus_DroppedEvents(t *testing.T) {
	bus := NewEventBus(10)
	ch := make(chan Event, 1)
	bus.Subscribe("test.event", ch, 0)
	ch <- NewBaseEvent("test.event")
	bus.Publish(NewBaseEvent("test.event"))
	bus.Publish(NewBaseEvent("test.event"))
	assert.GreaterOrEqual(t, bus.GetDroppedCount("test.event"), int64(1))
}

func TestFlowSessionStartedEvent(t *testing.T) {
	event := FlowSessionStartedEvent{
		BaseEvent:     NewBaseEvent("flow_session.started"),
		FlowSessionID: "fs-001",
		FlowID:        "code-fix",
	}
	assert.Equal(t, "flow_session.started", event.Type())
	assert.Equal(t, "fs-001", event.FlowSessionID)
	assert.False(t, event.Timestamp().IsZero())
}

func TestMemberStartedEvent(t *testing.T) {
	event := MemberStartedEvent{
		BaseEvent:     NewBaseEvent("member.started"),
		FlowSessionID: "fs-001",
		TeamID:        "diagnose",
		MemberID:      "inspect",
		MemberType:    "subagent",
	}
	assert.Equal(t, "member.started", event.Type())
	assert.Equal(t, "inspect", event.MemberID)
}

func TestModelCallCompletedEvent(t *testing.T) {
	event := ModelCallCompletedEvent{
		BaseEvent:        NewBaseEvent("model.completed"),
		FlowSessionID:    "fs-001",
		TeamID:           "diagnose",
		MemberID:         "inspect",
		Model:            "gpt-4o-mini",
		PromptTokens:     100,
		CompletionTokens: 50,
		Duration:         1500 * time.Millisecond,
	}
	assert.Equal(t, "model.completed", event.Type())
	assert.Equal(t, 100, event.PromptTokens)
	assert.Equal(t, 50, event.CompletionTokens)
}

func TestErrorOccurredEvent(t *testing.T) {
	event := ErrorOccurredEvent{
		BaseEvent:     NewBaseEvent("error.occurred"),
		FlowSessionID: "fs-001",
		TeamID:        "verify",
		MemberID:      "test",
		Layer:         "member",
		Module:        "command",
		ToolName:      "shell",
		ErrorType:     "execution_error",
		Error:         "command failed",
	}
	assert.Equal(t, "command failed", event.Error)
}
