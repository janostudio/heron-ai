package observability

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogLevel_String(t *testing.T) {
	tests := []struct {
		name     string
		level    LogLevel
		expected string
	}{
		{"debug", LogDebug, "debug"},
		{"info", LogInfo, "info"},
		{"warn", LogWarn, "warn"},
		{"error", LogError, "error"},
		{"fatal", LogFatal, "fatal"},
		{"unknown", LogLevel(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.level.String())
		})
	}
}

func TestLogger_Debug(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(LogDebug, &buf)
	logger.Debug("debug message", map[string]any{"k": "v"})

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "debug message", entry["msg"])
	assert.Equal(t, "debug", entry["level"])
	assert.Equal(t, "v", entry["k"])
}

func TestLogger_Warn(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(LogInfo, &buf)
	logger.Warn("warn message", nil)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "warn message", entry["msg"])
	assert.Equal(t, "warn", entry["level"])
}

func TestLogger_Error(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(LogInfo, &buf)
	logger.Error("error message", map[string]any{"code": 500})

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "error message", entry["msg"])
	assert.Equal(t, "error", entry["level"])
	assert.Equal(t, float64(500), entry["code"])
}

func TestLogger_LevelThresholdFiltering(t *testing.T) {
	var buf bytes.Buffer
	// Level is LogWarn; debug and info messages should be suppressed.
	logger := NewLogger(LogWarn, &buf)

	logger.Debug("debug message", nil)
	logger.Info("info message", nil)
	logger.Warn("warn message", nil)

	assert.NotContains(t, buf.String(), "debug message")
	assert.NotContains(t, buf.String(), "info message")
	assert.Contains(t, buf.String(), "warn message")
}

func TestLogger_SetIncludeSensitive(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(LogInfo, &buf)

	// Default false.
	assert.False(t, logger.includeSensitive)

	logger.SetIncludeSensitive(true)
	assert.True(t, logger.includeSensitive)

	logger.SetIncludeSensitive(false)
	assert.False(t, logger.includeSensitive)
}

func TestLogger_NoRunIDWhenUnset(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(LogInfo, &buf)
	logger.Info("no run id", nil)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	_, exists := entry["run_id"]
	assert.False(t, exists)
}
