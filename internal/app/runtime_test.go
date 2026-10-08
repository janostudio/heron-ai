package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// readLogFile returns the concatenated contents of all log files under
// root/.agents/data/logs.
func readLogFiles(t *testing.T, root string) string {
	t.Helper()
	logsDir := filepath.Join(root, ".agents", "data", "logs")
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(logsDir, e.Name()))
		if err == nil {
			b.Write(data)
		}
	}
	return b.String()
}

// TestBuildLoggerLogLevelOverride verifies the command-line log-level override
// wins over settings.json logging.level, so debug messages are emitted.
func TestBuildLoggerLogLevelOverride(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"),
		[]byte(`{"logging":{"level":"info"}}`), 0o644))

	logger := buildLogger(root, "debug")
	require.NotNil(t, logger)
	logger.Debug("override-debug-line", nil)
	require.NoError(t, logger.Close())

	require.Contains(t, readLogFiles(t, root), "override-debug-line")
}

// TestBuildLoggerNoOverrideUsesConfig verifies that without an override the
// settings.json level (info) filters out debug messages.
func TestBuildLoggerNoOverrideUsesConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"),
		[]byte(`{"logging":{"level":"info"}}`), 0o644))

	logger := buildLogger(root, "")
	require.NotNil(t, logger)
	logger.Debug("filtered-debug-line", nil)
	require.NoError(t, logger.Close())

	require.NotContains(t, readLogFiles(t, root), "filtered-debug-line")
}

// capturedLog is one entry written to captureLogger.
type capturedLog struct {
	Level  string
	Msg    string
	Fields map[string]any
}

// captureLogger is a logging.Logger that keeps entries in memory so a test can
// assert what the engine would have written to its log files.
type captureLogger struct {
	mu      sync.Mutex
	entries []capturedLog
}

func (l *captureLogger) Debug(msg string, fields map[string]any) {
	l.record("debug", msg, fields)
}

func (l *captureLogger) Info(msg string, fields map[string]any) {
	l.record("info", msg, fields)
}

func (l *captureLogger) Warn(msg string, fields map[string]any) {
	l.record("warn", msg, fields)
}

func (l *captureLogger) Error(msg string, fields map[string]any) {
	l.record("error", msg, fields)
}

func (l *captureLogger) record(level, msg string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, capturedLog{Level: level, Msg: msg, Fields: fields})
}

func (l *captureLogger) warns() []capturedLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []capturedLog
	for _, entry := range l.entries {
		if entry.Level == "warn" || entry.Level == "error" {
			out = append(out, entry)
		}
	}
	return out
}

// stubFlowRuntime is a types.FlowRuntime whose Status/Resume behaviour is
// scripted by the test. statusFn receives the 1-based Status call number, so a
// test can model "the Flow has not reached waiting_tool yet".
type stubFlowRuntime struct {
	mu          sync.Mutex
	statusCalls int
	resumeCalls int
	statusFn    func(call int) (types.FlowSession, error)
	resumeFn    func() (types.FlowTurnResult, error)
}

func (r *stubFlowRuntime) Start(context.Context, types.StartFlowRequest) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, errors.New("not implemented")
}

func (r *stubFlowRuntime) HandleInput(context.Context, string, string) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, errors.New("not implemented")
}

func (r *stubFlowRuntime) Resume(context.Context, string, string) (types.FlowTurnResult, error) {
	r.mu.Lock()
	r.resumeCalls++
	fn := r.resumeFn
	r.mu.Unlock()
	if fn == nil {
		return types.FlowTurnResult{Session: types.FlowSession{Status: types.SessionWaitingInput}}, nil
	}
	return fn()
}

func (r *stubFlowRuntime) Cancel(context.Context, string) error { return nil }

func (r *stubFlowRuntime) Status(context.Context, string) (types.FlowSession, error) {
	r.mu.Lock()
	r.statusCalls++
	call := r.statusCalls
	fn := r.statusFn
	r.mu.Unlock()
	if fn == nil {
		return types.FlowSession{}, errors.New("status is not scripted")
	}
	return fn(call)
}

func (r *stubFlowRuntime) counts() (status, resume int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.statusCalls, r.resumeCalls
}

// scriptedRuntime returns a runtime that reports status until the given call
// number and waitingTool from then on.
func scriptedRuntime(until int, status types.SessionStatus, waitingTool types.SessionStatus) *stubFlowRuntime {
	return &stubFlowRuntime{
		statusFn: func(call int) (types.FlowSession, error) {
			if call <= until {
				return types.FlowSession{ID: "flow-1", Status: status}, nil
			}
			return types.FlowSession{ID: "flow-1", Status: waitingTool}, nil
		},
	}
}

func doneTask() types.ToolTask {
	return types.ToolTask{
		ID:            "task-1",
		ToolName:      "Bash",
		FlowSessionID: "flow-1",
		Status:        types.ToolTaskCompleted,
	}
}

// withWakeupLimit shortens the wake-up wait window for one test. The
// production window is deliberately long (a Flow may take tens of seconds to
// flush its checkpoint); tests must not wait that long.
func withWakeupLimit(t *testing.T, limit time.Duration) {
	t.Helper()
	previous := toolWakeupWaitLimit
	toolWakeupWaitLimit = limit
	t.Cleanup(func() { toolWakeupWaitLimit = previous })
}

// TestWakeFlowOnToolTaskDoneWaitsForWaitingTool verifies the wake-up survives
// the window in which the Flow turn has not flushed waiting_tool yet: the
// callback keeps waiting and resumes once the session is parked.
func TestWakeFlowOnToolTaskDoneWaitsForWaitingTool(t *testing.T) {
	withWakeupLimit(t, 5*time.Second)
	runtime := scriptedRuntime(3, types.SessionRunning, types.SessionWaitingTool)
	logger := &captureLogger{}

	err := wakeFlowOnToolTaskDone(context.Background(), runtime, logger, doneTask())

	require.NoError(t, err)
	status, resume := runtime.counts()
	require.GreaterOrEqual(t, status, 4, "must poll past the first running observations")
	require.Equal(t, 1, resume, "the wake-up must be consumed exactly once")
	require.Empty(t, logger.warns())
}

// TestWakeFlowOnToolTaskDoneRetriesFailedResume verifies a transient Resume
// failure (the session is waiting_tool but the turn cannot start yet) is
// retried instead of dropping the wake-up.
func TestWakeFlowOnToolTaskDoneRetriesFailedResume(t *testing.T) {
	withWakeupLimit(t, 5*time.Second)
	runtime := scriptedRuntime(0, types.SessionRunning, types.SessionWaitingTool)
	runtime.resumeFn = func() (types.FlowTurnResult, error) {
		if statusCalls, _ := runtime.counts(); statusCalls < 4 {
			return types.FlowTurnResult{}, errors.New("session has unfinished execution")
		}
		return types.FlowTurnResult{Session: types.FlowSession{Status: types.SessionWaitingInput}}, nil
	}
	logger := &captureLogger{}

	err := wakeFlowOnToolTaskDone(context.Background(), runtime, logger, doneTask())

	require.NoError(t, err)
	_, resume := runtime.counts()
	require.GreaterOrEqual(t, resume, 2)
	require.Empty(t, logger.warns())
}

// TestWakeFlowOnToolTaskDoneAbandonsOnlyLoudly is the regression test for the
// silent 2-second window: when the Flow never reaches waiting_tool the wake-up
// is still not delivered, but it is reported instead of vanishing.
func TestWakeFlowOnToolTaskDoneAbandonsOnlyLoudly(t *testing.T) {
	withWakeupLimit(t, 150*time.Millisecond)
	runtime := scriptedRuntime(1<<30, types.SessionRunning, types.SessionWaitingTool)
	logger := &captureLogger{}

	err := wakeFlowOnToolTaskDone(context.Background(), runtime, logger, doneTask())

	require.Error(t, err)
	require.Contains(t, err.Error(), "flow-1")
	require.Contains(t, err.Error(), "task-1")
	require.Contains(t, err.Error(), string(types.SessionRunning))
	_, resume := runtime.counts()
	require.Equal(t, 0, resume, "a session that never reached waiting_tool must not be resumed")

	warns := logger.warns()
	require.Len(t, warns, 1)
	require.Equal(t, "flow-1", warns[0].Fields["flow_session_id"])
	require.Equal(t, "task-1", warns[0].Fields["task_id"])
	require.Equal(t, string(types.SessionRunning), warns[0].Fields["last_session_status"])
}

// TestWakeFlowOnToolTaskDoneIgnoresNonToolStates verifies the existing
// contract is preserved: waiting_input and terminal sessions are never resumed
// by a tool completion callback.
func TestWakeFlowOnToolTaskDoneIgnoresNonToolStates(t *testing.T) {
	for _, status := range []types.SessionStatus{
		types.SessionWaitingInput,
		types.SessionCompleted,
		types.SessionFailed,
		types.SessionCancelled,
		types.SessionWaitingApproval,
	} {
		t.Run(string(status), func(t *testing.T) {
			withWakeupLimit(t, 5*time.Second)
			runtime := scriptedRuntime(1<<30, status, status)
			logger := &captureLogger{}

			require.NoError(t, wakeFlowOnToolTaskDone(context.Background(), runtime, logger, doneTask()))
			_, resume := runtime.counts()
			require.Equal(t, 0, resume)
			require.Empty(t, logger.warns())
		})
	}
}

// TestWakeFlowOnToolTaskDoneCancelledContextIsSilent verifies a cancelled
// caller context stops the wait without reporting a lost wake-up: the process
// is shutting down, not stuck.
func TestWakeFlowOnToolTaskDoneCancelledContextIsSilent(t *testing.T) {
	withWakeupLimit(t, 5*time.Second)
	runtime := scriptedRuntime(1<<30, types.SessionRunning, types.SessionWaitingTool)
	logger := &captureLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := wakeFlowOnToolTaskDone(ctx, runtime, logger, doneTask())

	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, logger.warns())
}

// TestWakeFlowOnToolTaskDoneConsumesOnceWhenStillWaitingTool verifies the
// multi-task case is unchanged: a Resume that leaves the session in
// waiting_tool (a sibling task is still running) consumes this callback, and
// the sibling's own callback performs the final resume.
func TestWakeFlowOnToolTaskDoneConsumesOnceWhenStillWaitingTool(t *testing.T) {
	withWakeupLimit(t, 5*time.Second)
	runtime := scriptedRuntime(0, types.SessionRunning, types.SessionWaitingTool)
	runtime.resumeFn = func() (types.FlowTurnResult, error) {
		return types.FlowTurnResult{Session: types.FlowSession{Status: types.SessionWaitingTool}}, nil
	}
	logger := &captureLogger{}

	require.NoError(t, wakeFlowOnToolTaskDone(context.Background(), runtime, logger, doneTask()))
	_, resume := runtime.counts()
	require.Equal(t, 1, resume)
	require.Empty(t, logger.warns())
}
