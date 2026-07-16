package observability

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

type LogLevel int

const (
	LogDebug LogLevel = iota
	LogInfo
	LogWarn
	LogError
	LogFatal
)

func (l LogLevel) String() string {
	switch l {
	case LogDebug:
		return "debug"
	case LogInfo:
		return "info"
	case LogWarn:
		return "warn"
	case LogError:
		return "error"
	case LogFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

type Logger struct {
	level            LogLevel
	output           io.Writer
	runID            string
	includeSensitive bool
}

func NewLogger(level LogLevel, output io.Writer) *Logger {
	if output == nil {
		output = os.Stdout
	}
	return &Logger{
		level:  level,
		output: output,
	}
}

func (l *Logger) SetRunID(runID string) {
	l.runID = runID
}

func (l *Logger) SetIncludeSensitive(include bool) {
	l.includeSensitive = include
}

func (l *Logger) log(level LogLevel, msg string, fields map[string]any) {
	if level < l.level {
		return
	}

	entry := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"level": level.String(),
		"msg":   msg,
	}

	if l.runID != "" {
		entry["run_id"] = l.runID
	}

	for k, v := range fields {
		entry[k] = v
	}

	data, _ := json.Marshal(entry)
	fmt.Fprintln(l.output, string(data))

	if level == LogFatal {
		os.Exit(1)
	}
}

func (l *Logger) Debug(msg string, fields map[string]any) {
	l.log(LogDebug, msg, fields)
}

func (l *Logger) Info(msg string, fields map[string]any) {
	l.log(LogInfo, msg, fields)
}

func (l *Logger) Warn(msg string, fields map[string]any) {
	l.log(LogWarn, msg, fields)
}

func (l *Logger) Error(msg string, fields map[string]any) {
	l.log(LogError, msg, fields)
}

func (l *Logger) Fatal(msg string, fields map[string]any) {
	l.log(LogFatal, msg, fields)
}
