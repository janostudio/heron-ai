package script

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// fakeCaller records calls and replays canned results so the engine can be
// exercised without a workspace.
type fakeCaller struct {
	mu      sync.Mutex
	calls   []string
	results map[string]*types.ToolResult
	specs   map[string]types.ToolExecutionSpec
	delay   time.Duration
}

func newFakeCaller() *fakeCaller {
	return &fakeCaller{
		results: map[string]*types.ToolResult{},
		specs:   map[string]types.ToolExecutionSpec{},
	}
}

func (f *fakeCaller) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
	if r, ok := f.results[name]; ok {
		return r, nil
	}
	return &types.ToolResult{Success: true, Content: "ok:" + name}, nil
}

func (f *fakeCaller) ExecutionSpec(name string) types.ToolExecutionSpec {
	if s, ok := f.specs[name]; ok {
		return s
	}
	// Empty Class means "not declared read-only", which the engine must treat as
	// effectful. Tests that need a read-only tool set the spec explicitly.
	return types.ToolExecutionSpec{}
}

func (f *fakeCaller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// memStore is an in-memory StepStore.
type memStore struct {
	mu   sync.Mutex
	recs map[string][]StepRecord
}

func newMemStore() *memStore { return &memStore{recs: map[string][]StepRecord{}} }

func (m *memStore) Load(_ context.Context, scriptID string) ([]StepRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]StepRecord(nil), m.recs[scriptID]...), nil
}

func (m *memStore) Append(_ context.Context, scriptID string, rec StepRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs[scriptID] = append(m.recs[scriptID], rec)
	return nil
}

func newRunner(caller ToolCaller) *Runner {
	return &Runner{Caller: caller, Store: newMemStore()}
}

func TestRunReturnsValueAndConsole(t *testing.T) {
	r := newRunner(newFakeCaller())
	ex, err := r.Run(context.Background(), RunRequest{
		ScriptID: "s1",
		Code:     "console.log('hello'); return {count: 2};",
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"count":2}`, ex.Value)
	assert.Equal(t, []string{"hello"}, ex.Console)
}

func TestRunCallsToolsSynchronously(t *testing.T) {
	caller := newFakeCaller()
	caller.results["Read"] = &types.ToolResult{Success: true, Content: "file body"}
	r := newRunner(caller)

	ex, err := r.Run(context.Background(), RunRequest{
		ScriptID:  "s2",
		Code:      `const r = tools.Read({file: "a.txt"}); return r.content;`,
		ToolNames: []string{"Read"},
	})
	require.NoError(t, err)
	assert.Equal(t, "file body", ex.Value)
	assert.Equal(t, 1, caller.callCount())
	require.Len(t, ex.Steps, 1)
	assert.Equal(t, "Read", ex.Steps[0].ToolName)
}

// A second run of the same script must not touch the executor again: this is
// the whole point of the replay log.
func TestReplayHitsRecordedSteps(t *testing.T) {
	caller := newFakeCaller()
	store := newMemStore()
	r := &Runner{Caller: caller, Store: store}
	req := RunRequest{
		ScriptID:  "s3",
		Code:      `const a = tools.Read({file: "a"}); const b = tools.Read({file: "b"}); return [a.content, b.content];`,
		ToolNames: []string{"Read"},
	}

	if _, err := r.Run(context.Background(), req); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := caller.callCount()
	require.Equal(t, 2, first)

	ex, err := r.Run(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, first, caller.callCount(), "replay must not execute tools again")
	assert.Empty(t, ex.Steps, "replayed calls are not new work")
}

// Changing one argument must invalidate that step and everything after it.
func TestReplayInvalidatedByArguments(t *testing.T) {
	caller := newFakeCaller()
	store := newMemStore()
	r := &Runner{Caller: caller, Store: store}

	first := RunRequest{ScriptID: "s4", Code: `return tools.Read({file: "a"}).content;`, ToolNames: []string{"Read"}}
	_, err := r.Run(context.Background(), first)
	require.NoError(t, err)
	require.Equal(t, 1, caller.callCount())

	second := first
	second.Code = `return tools.Read({file: "b"}).content;`
	second.ScriptID = "s4" // same id, different program text is the edge case
	_, err = r.Run(context.Background(), second)
	require.NoError(t, err)
	assert.Equal(t, 2, caller.callCount(), "different arguments must re-execute")
}

// An effectful call that started but never recorded a result must stop the
// script instead of repeating the write.
func TestInterruptedEffectIsRefused(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.Append(context.Background(), "s5", StepRecord{
		Step: 0, ToolName: "Write", ArgsHash: "x", Status: stepStarted,
	}))
	r := &Runner{Caller: newFakeCaller(), Store: store}

	_, err := r.Run(context.Background(), RunRequest{ScriptID: "s5", Code: `return 1;`})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEffectInterrupted))
}

func TestEffectfulCallRecordsIntentFirst(t *testing.T) {
	caller := newFakeCaller()
	// Absence of a spec means "treat as effectful", which is the safe default.
	store := newMemStore()
	r := &Runner{Caller: caller, Store: store}

	_, err := r.Run(context.Background(), RunRequest{
		ScriptID:  "s6",
		Code:      `return tools.Write({file: "a", content: "x", mode: "create"}).content;`,
		ToolNames: []string{"Write"},
	})
	require.NoError(t, err)

	recs, err := store.Load(context.Background(), "s6")
	require.NoError(t, err)
	require.Len(t, recs, 2)
	assert.Equal(t, stepStarted, recs[0].Status)
	assert.Equal(t, stepDone, recs[1].Status)
}

func TestToolFailureThrowsCatchableError(t *testing.T) {
	caller := newFakeCaller()
	caller.results["Bash"] = &types.ToolResult{Success: false, Error: "denied by policy"}
	r := newRunner(caller)

	ex, err := r.Run(context.Background(), RunRequest{
		ScriptID:  "s7",
		Code:      `try { tools.Bash({command: "rm -rf /"}); return "no"; } catch (e) { return e.name + ":" + e.message; }`,
		ToolNames: []string{"Bash"},
	})
	require.NoError(t, err)
	assert.Contains(t, ex.Value, "ToolCallError")
	assert.Contains(t, ex.Value, "denied by policy")
}

func TestTimeoutInterruptsRunawayLoop(t *testing.T) {
	r := newRunner(newFakeCaller())
	start := time.Now()
	ex, err := r.Run(context.Background(), RunRequest{
		ScriptID: "s8",
		Code:     `while (true) {}`,
		Timeout:  120 * time.Millisecond,
	})
	require.Error(t, err)
	assert.True(t, ex.TimedOut, "watchdog must report a timeout")
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestCancelStopsRun(t *testing.T) {
	r := newRunner(newFakeCaller())
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	ex, err := r.Run(ctx, RunRequest{ScriptID: "s9", Code: `while (true) {}`})
	require.Error(t, err)
	assert.True(t, ex.Canceled)
}

// The same script must observe the same clock and the same random sequence on
// every run; otherwise replay could take a different branch.
func TestDeterminismAcrossRuns(t *testing.T) {
	r := newRunner(newFakeCaller())
	req := RunRequest{
		ScriptID: "s10",
		Code:     `const a = Date.now(); const b = Math.random(); const c = Math.random(); return [a, b, c];`,
	}
	first, err := r.Run(context.Background(), req)
	require.NoError(t, err)
	second, err := r.Run(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, first.Value, second.Value)
}

func TestDeterminismDiffersBetweenScripts(t *testing.T) {
	r := newRunner(newFakeCaller())
	a, err := r.Run(context.Background(), RunRequest{ScriptID: "s11a", Code: `return [Date.now(), Math.random()];`})
	require.NoError(t, err)
	b, err := r.Run(context.Background(), RunRequest{ScriptID: "s11b", Code: `return [Date.now(), Math.random()];`})
	require.NoError(t, err)
	assert.NotEqual(t, a.Value, b.Value)
}

func TestSourceRejectsAsyncConstructs(t *testing.T) {
	cases := []struct {
		name string
		code string
		want string
	}{
		{"await", `const x = await tools.Read({file: "a"}); return x;`, "await is not available"},
		{"promise", `return Promise.resolve(1);`, "Promise is not available"},
		{"timer", `setTimeout(() => {}, 1); return 1;`, "timers are not available"},
		{"newDate", `return new Date().getTime();`, "new Date()"},
		{"require", `const fs = require("fs"); return 1;`, "require is not available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(newFakeCaller())
			_, err := r.Run(context.Background(), RunRequest{ScriptID: "s12", Code: tc.code})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestStepBudgetStopsRunawayToolCalls(t *testing.T) {
	caller := newFakeCaller()
	r := newRunner(caller)
	_, err := r.Run(context.Background(), RunRequest{
		ScriptID:  "s13",
		Code:      `for (let i = 0; i < 10; i++) { tools.Read({file: "a"}); } return 1;`,
		ToolNames: []string{"Read"},
		MaxSteps:  3,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "budget exceeded")
	assert.Equal(t, 3, caller.callCount())
}

func TestCodeSizeLimit(t *testing.T) {
	r := newRunner(newFakeCaller())
	big := "return 1; // " + strings.Repeat("x", MaxCodeBytes)
	_, err := r.Run(context.Background(), RunRequest{ScriptID: "s14", Code: big})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit is")
}

func TestToolArgumentMustBeObject(t *testing.T) {
	r := newRunner(newFakeCaller())
	_, err := r.Run(context.Background(), RunRequest{
		ScriptID:  "s15",
		Code:      `return tools.Read("a.txt");`,
		ToolNames: []string{"Read"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expects an object argument")
}
