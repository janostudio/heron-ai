package observability

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogger_Levels(t *testing.T) {
	tests := []struct {
		name      string
		level     LogLevel
		logFunc   func(*Logger)
		shouldLog bool
	}{
		{
			name:  "Debug below Info",
			level: LogInfo,
			logFunc: func(l *Logger) {
				l.Debug("test", nil)
			},
			shouldLog: false,
		},
		{
			name:  "Info at Info",
			level: LogInfo,
			logFunc: func(l *Logger) {
				l.Info("test", nil)
			},
			shouldLog: true,
		},
		{
			name:  "Warn above Info",
			level: LogInfo,
			logFunc: func(l *Logger) {
				l.Warn("test", nil)
			},
			shouldLog: true,
		},
		{
			name:  "Error above Info",
			level: LogInfo,
			logFunc: func(l *Logger) {
				l.Error("test", nil)
			},
			shouldLog: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := NewLogger(tt.level, &buf)
			tt.logFunc(l)

			if tt.shouldLog {
				assert.NotEmpty(t, buf.String())
			} else {
				assert.Empty(t, buf.String())
			}
		})
	}
}

func TestLogger_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(LogInfo, &buf)

	l.Info("test message", map[string]any{
		"key": "value",
	})

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)

	assert.Equal(t, "test message", entry["msg"])
	assert.Equal(t, "info", entry["level"])
	assert.Equal(t, "value", entry["key"])
	assert.NotEmpty(t, entry["ts"])
}

func TestLogger_RunID(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(LogInfo, &buf)
	l.SetRunID("run-123")

	l.Info("test", nil)

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)

	assert.Equal(t, "run-123", entry["run_id"])
}

func TestLogger_NoRunID(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(LogInfo, &buf)

	l.Info("test", nil)

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)

	_, ok := entry["run_id"]
	assert.False(t, ok)
}

func TestLogger_SetIncludeSensitive(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(LogInfo, &buf)
	l.SetIncludeSensitive(true)

	l.Info("test", map[string]any{
		"sensitive": "data",
	})

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)
	assert.Equal(t, "data", entry["sensitive"])
}

func TestLogLevel_String(t *testing.T) {
	assert.Equal(t, "debug", LogDebug.String())
	assert.Equal(t, "info", LogInfo.String())
	assert.Equal(t, "warn", LogWarn.String())
	assert.Equal(t, "error", LogError.String())
	assert.Equal(t, "fatal", LogFatal.String())
}

func TestLogger_NilOutput(t *testing.T) {
	l := NewLogger(LogInfo, nil)
	l.Info("test", nil)
	// Should not panic
}

func TestEventBus_PublishSubscribe(t *testing.T) {
	bus := NewEventBus(10)

	ch := make(chan Event, 10)
	bus.Subscribe("test.event", ch, 0)

	event := NewBaseEvent("test.event")
	bus.Publish(event)

	select {
	case received := <-ch:
		assert.Equal(t, "test.event", received.Type())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBus_WildcardSubscription(t *testing.T) {
	bus := NewEventBus(10)

	ch := make(chan Event, 10)
	bus.Subscribe("*", ch, 0)

	event := NewBaseEvent("any.event")
	bus.Publish(event)

	select {
	case received := <-ch:
		assert.Equal(t, "any.event", received.Type())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBus_DroppedEvents(t *testing.T) {
	bus := NewEventBus(10)

	ch := make(chan Event, 1) // small buffer
	bus.Subscribe("test.event", ch, 0)

	// Fill the buffer
	ch <- NewBaseEvent("test.event")

	// Publish two more events (one will be dropped)
	bus.Publish(NewBaseEvent("test.event"))
	bus.Publish(NewBaseEvent("test.event"))

	assert.GreaterOrEqual(t, bus.GetDroppedCount("test.event"), int64(1))
}

func TestEventBus_DefaultBufferSize(t *testing.T) {
	bus := NewEventBus(0) // zero should default to 256
	assert.NotNil(t, bus)
}

func TestRunStartedEvent(t *testing.T) {
	event := RunStartedEvent{
		BaseEvent: NewBaseEvent("run.started"),
		RunID:     "run-001",
		FlowName:  "default",
	}
	assert.Equal(t, "run.started", event.Type())
	assert.Equal(t, "run-001", event.RunID)
	assert.Equal(t, "default", event.FlowName)
	assert.False(t, event.Timestamp().IsZero())
}

func TestAgentStartedEvent(t *testing.T) {
	event := AgentStartedEvent{
		BaseEvent: NewBaseEvent("agent.started"),
		RunID:     "run-001",
		RoundNum:  1,
		TeamName:  "team-a",
		AgentName: "agent-1",
	}
	assert.Equal(t, "agent.started", event.Type())
	assert.Equal(t, "agent-1", event.AgentName)
}

func TestLLMCallCompletedEvent(t *testing.T) {
	duration := 1500 * time.Millisecond
	event := LLMCallCompletedEvent{
		BaseEvent:        NewBaseEvent("llm.completed"),
		RunID:            "run-001",
		TeamName:         "team-a",
		AgentName:        "agent-1",
		Model:            "gpt-4",
		PromptTokens:     100,
		CompletionTokens: 50,
		Duration:         duration,
	}
	assert.Equal(t, "llm.completed", event.Type())
	assert.Equal(t, 100, event.PromptTokens)
	assert.Equal(t, 50, event.CompletionTokens)
	assert.Equal(t, duration, event.Duration)
}

func TestErrorOccurredEvent(t *testing.T) {
	event := ErrorOccurredEvent{
		BaseEvent:  NewBaseEvent("error.occurred"),
		RunID:      "run-001",
		RoundNum:   2,
		TeamName:   "team-a",
		AgentName:  "agent-1",
		Layer:      "agent",
		Module:     "tool",
		ToolName:   "bash",
		ErrorType:  "execution_error",
		Error:      "command failed",
		StackTrace: "at main.go:42",
		Context:    "executing bash command",
	}
	assert.Equal(t, "error.occurred", event.Type())
	assert.Equal(t, "command failed", event.Error)
}

func TestContextCompressedEvent(t *testing.T) {
	event := ContextCompressedEvent{
		BaseEvent:    NewBaseEvent("context.compressed"),
		RunID:        "run-001",
		AgentName:    "agent-1",
		Layer:        "memory",
		BeforeTokens: 10000,
		AfterTokens:  5000,
	}
	assert.Equal(t, "context.compressed", event.Type())
	assert.Equal(t, 10000, event.BeforeTokens)
	assert.Equal(t, 5000, event.AfterTokens)
}

func TestMetrics_Counters(t *testing.T) {
	m := NewMetrics()

	m.Inc("requests")
	m.Inc("requests")
	m.Inc("requests")

	assert.Equal(t, float64(3), m.Get("requests"))
}

func TestMetrics_Add(t *testing.T) {
	m := NewMetrics()

	m.Add("tokens", 150)
	assert.Equal(t, float64(150), m.Get("tokens"))

	m.Add("tokens", 50)
	assert.Equal(t, float64(200), m.Get("tokens"))
}

func TestMetrics_Gauges(t *testing.T) {
	m := NewMetrics()

	m.Set("cpu_usage", 75.5)
	assert.Equal(t, 75.5, m.Get("cpu_usage"))

	m.Set("cpu_usage", 80.0)
	assert.Equal(t, 80.0, m.Get("cpu_usage"))
}

func TestMetrics_Histogram(t *testing.T) {
	m := NewMetrics()

	m.Observe("latency", 100*time.Millisecond)
	m.Observe("latency", 200*time.Millisecond)
	m.Observe("latency", 300*time.Millisecond)
	m.Observe("latency", 500*time.Millisecond)
	m.Observe("latency", 1000*time.Millisecond)

	snap := m.Snapshot()
	histograms := snap["histograms"].(map[string]map[string]any)
	h := histograms["latency"]

	assert.Equal(t, int64(5), h["count"])
	assert.Greater(t, h["p95"].(float64), 0.5)
}

func TestMetrics_GetNonexistent(t *testing.T) {
	m := NewMetrics()
	assert.Equal(t, float64(0), m.Get("nonexistent"))
}

func TestMetrics_Snapshot(t *testing.T) {
	m := NewMetrics()

	m.Inc("counter1")
	m.Set("gauge1", 42.0)
	m.Observe("latency", 50*time.Millisecond)

	snap := m.Snapshot()

	counters := snap["counters"].(map[string]int64)
	assert.Equal(t, int64(1), counters["counter1"])

	gauges := snap["gauges"].(map[string]float64)
	assert.Equal(t, 42.0, gauges["gauge1"])

	histograms := snap["histograms"].(map[string]map[string]any)
	assert.Contains(t, histograms, "latency")
}

func TestHistogram_Percentile(t *testing.T) {
	h := NewHistogram([]float64{0.1, 0.5, 1.0, 5.0})

	h.Observe(0.2)
	h.Observe(0.3)
	h.Observe(0.6)
	h.Observe(2.0)

	assert.Equal(t, 1.0, h.Percentile(50))  // p50 = 1.0
	assert.Equal(t, 5.0, h.Percentile(95))  // p95 = 5.0
}

func TestHistogram_PercentileEmpty(t *testing.T) {
	h := NewHistogram([]float64{0.1, 0.5, 1.0})
	assert.Equal(t, float64(0), h.Percentile(50))
}

func TestHistogram_ObserveAboveAll(t *testing.T) {
	h := NewHistogram([]float64{0.1, 0.5, 1.0})

	h.Observe(10.0) // above all buckets

	assert.Equal(t, int64(1), h.Count)
	assert.Equal(t, 10.0, h.Sum)
}
