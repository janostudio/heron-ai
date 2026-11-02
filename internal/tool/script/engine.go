package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// ToolCaller is the slice of the engine's tool executor that the script sandbox
// needs. Keeping it an interface (rather than importing internal/tool) is what
// lets the package be unit-tested without a real workspace, and keeps the
// dependency direction the same as every other tool.
type ToolCaller interface {
	Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error)
}

// SpecProvider reports how a tool may be scheduled. The engine uses it to tell
// read-only calls from effectful ones: only the former are safe to re-run when
// a replay finds no recorded result.
type SpecProvider interface {
	ExecutionSpec(name string) types.ToolExecutionSpec
}

// Step status values recorded in the memo log.
const (
	stepStarted = "started"
	stepDone    = "done"
)

// StepRecord is one inner tool call. Status started without a matching done
// means the process died mid-call; effectful calls in that state are never
// retried (see design doc §5.3).
type StepRecord struct {
	Step     int               `json:"step"`
	ToolName string            `json:"tool_name"`
	ArgsHash string            `json:"args_hash"`
	Status   string            `json:"status"`
	Result   *types.ToolResult `json:"result,omitempty"`
}

// StepStore persists inner calls for replay. Implementations must make Append
// durable before it returns: an effectful call whose result is lost would be
// re-executed on the next turn.
type StepStore interface {
	Load(ctx context.Context, scriptID string) ([]StepRecord, error)
	Append(ctx context.Context, scriptID string, rec StepRecord) error
}

// Execution is the outcome of one Run call.
type Execution struct {
	// Value is the script's return value, JSON-encoded. Empty when the script
	// returned nothing.
	Value string
	// Console is everything the script logged, in order.
	Console []string
	// Steps are the inner calls that were actually executed in this run
	// (replayed calls are not included: they cost nothing and are not news).
	Steps []StepRecord
	// Canceled reports a caller-initiated stop; TimedOut reports the watchdog.
	Canceled bool
	TimedOut bool
	// Truncated reports that Value or Console hit MaxOutputBytes.
	Truncated bool
}

// RunRequest is one script execution.
type RunRequest struct {
	ScriptID  string
	Code      string
	Timeout   time.Duration
	MaxSteps  int
	MaxOutput int
	// ToolNames are the tools the script may call. The caller decides this set
	// (agent tool whitelist ∩ deny rules); the engine has no registry of its
	// own, so the visible set has exactly one source.
	ToolNames []string
	// Catalog is the pre-rendered tool catalog. It lives on the `code`
	// parameter description, not in the runtime.
	Catalog string
}

// ErrEffectInterrupted reports a replay that found an effectful call started but
// never completed. The script must not be resumed automatically: the effect may
// or may not have landed.
var ErrEffectInterrupted = errors.New("script has an effectful tool call that started but never completed; it will not be retried")

// Runner executes scripts. It holds no per-call state: everything a run needs
// arrives in RunRequest, and everything a replay needs leaves in StepStore.
type Runner struct {
	Caller ToolCaller
	Store  StepStore
}

// Run executes one script.
func (r *Runner) Run(ctx context.Context, req RunRequest) (Execution, error) {
	if err := CheckSource(req.Code); err != nil {
		return Execution{}, err
	}
	if len(req.Code) > MaxCodeBytes {
		return Execution{}, fmt.Errorf("script is %d bytes, limit is %d", len(req.Code), MaxCodeBytes)
	}

	timeout := ClampTimeout(req.Timeout)
	maxSteps := ClampMaxSteps(req.MaxSteps)
	outputLimit := req.MaxOutput
	if outputLimit <= 0 {
		outputLimit = MaxOutputBytes
	}

	var recorded []StepRecord
	if r.Store != nil {
		loaded, err := r.Store.Load(ctx, req.ScriptID)
		if err != nil {
			return Execution{}, fmt.Errorf("load replay log: %w", err)
		}
		recorded = loaded
	}
	if err := checkInterruptedEffects(recorded, r.Store != nil); err != nil {
		return Execution{}, err
	}

	rt := goja.New()
	if err := installDeterminism(rt, req.ScriptID, deterministicBaseMillis(req.ScriptID)); err != nil {
		return Execution{}, err
	}

	console := newLimitedBuffer(outputLimit)
	if err := installConsole(rt, console); err != nil {
		return Execution{}, err
	}

	ex := Execution{}
	run := &scriptRun{
		rt:        rt,
		runner:    r,
		ctx:       ctx,
		scriptID:  req.ScriptID,
		recorded:  indexSteps(recorded),
		maxSteps:  maxSteps,
		exec:      &ex,
		toolNames: sortedToolNames(req.ToolNames),
	}
	if err := run.installTools(); err != nil {
		return Execution{}, err
	}

	start := time.Now()
	cancelWatch := run.startWatchdog(start, timeout)
	value, err := run.eval(req.Code)
	cancelWatch()

	ex.Console = splitConsole(console.String())
	if console.Overflowed() {
		ex.Truncated = true
	}

	if err != nil {
		if run.interrupted != nil {
			ex.Canceled = errors.Is(run.interrupted, errCanceled)
			ex.TimedOut = errors.Is(run.interrupted, errTimeout)
		}
		return ex, err
	}

	encoded, truncated, encErr := encodeValue(value, outputLimit)
	if encErr != nil {
		return ex, encErr
	}
	ex.Value = encoded
	if truncated {
		ex.Truncated = true
	}
	return ex, nil
}

var (
	errCanceled = errors.New("script canceled")
	errTimeout  = errors.New("script timed out")
)

// scriptRun is the mutable state of a single execution. It exists as a struct
// so the tool bindings can capture it without capturing the caller's stack.
type scriptRun struct {
	rt        *goja.Runtime
	runner    *Runner
	ctx       context.Context
	scriptID  string
	recorded  map[int]StepRecord
	maxSteps  int
	exec      *Execution
	toolNames []string

	step        int
	inflight    int32
	lastActive  int64
	interrupted error
	// stepsMu guards exec.Steps: tools.all runs read-only members concurrently.
	stepsMu sync.Mutex
}

func indexSteps(records []StepRecord) map[int]StepRecord {
	out := make(map[int]StepRecord, len(records))
	for _, rec := range records {
		// Both done and started are indexed: done satisfies a lookup, started
		// only exists so checkInterruptedEffects can see it.
		out[rec.Step] = rec
	}
	return out
}

// checkInterruptedEffects refuses to continue a script whose last effectful
// call has a started record but no result. Without a store every run is the
// first run, so there is nothing to refuse.
func checkInterruptedEffects(records []StepRecord, hasStore bool) error {
	if !hasStore {
		return nil
	}
	pending := map[int]StepRecord{}
	for _, rec := range records {
		switch rec.Status {
		case stepStarted:
			pending[rec.Step] = rec
		case stepDone:
			delete(pending, rec.Step)
		}
	}
	for _, rec := range pending {
		return fmt.Errorf("%w (step %d, tool %s)", ErrEffectInterrupted, rec.Step, rec.ToolName)
	}
	return nil
}

func installConsole(rt *goja.Runtime, buf *limitedBuffer) error {
	write := func(prefix string) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			parts := make([]string, 0, len(call.Arguments))
			for _, arg := range call.Arguments {
				parts = append(parts, arg.String())
			}
			line := strings.Join(parts, " ")
			if prefix != "" {
				line = prefix + line
			}
			_, _ = buf.Write([]byte(line + "\n"))
			return goja.Undefined()
		}
	}
	obj := rt.NewObject()
	if err := obj.Set("log", write("")); err != nil {
		return err
	}
	if err := obj.Set("warn", write("warn: ")); err != nil {
		return err
	}
	if err := obj.Set("error", write("error: ")); err != nil {
		return err
	}
	return rt.Set("console", obj)
}

// eval wraps the source in an IIFE so a top-level `return` works and top-level
// declarations stay local to this run (there is no persistent kernel).
func (s *scriptRun) eval(code string) (goja.Value, error) {
	var value goja.Value
	var runErr error

	func() {
		defer func() {
			if rec := recover(); rec != nil {
				if _, ok := rec.(*goja.InterruptedError); ok {
					runErr = s.interruptedErr()
					return
				}
				panic(rec)
			}
		}()
		value, runErr = s.rt.RunString("(function(){\n" + code + "\n})()")
	}()
	if runErr != nil {
		var interrupted *goja.InterruptedError
		if errors.As(runErr, &interrupted) {
			return nil, s.interruptedErr()
		}
		return nil, fmt.Errorf("script error: %w", runErr)
	}
	return value, nil
}

func (s *scriptRun) interruptedErr() error {
	if s.interrupted == nil {
		s.interrupted = errCanceled
	}
	return s.interrupted
}

// startWatchdog runs a 50ms ticker until the returned function is called. The
// deadline is measured from the last moment the script was not waiting on a
// tool: a slow Bash must not consume the script's compute budget.
func (s *scriptRun) startWatchdog(start time.Time, timeout time.Duration) func() {
	atomic.StoreInt64(&s.lastActive, start.UnixNano())
	done := make(chan struct{})
	var stopped int32
	go func() {
		ticker := time.NewTicker(watchDogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if s.ctx != nil && s.ctx.Err() != nil {
					s.interrupted = errCanceled
					s.rt.Interrupt(errCanceled)
					return
				}
				if atomic.LoadInt32(&s.inflight) > 0 {
					continue
				}
				last := time.Unix(0, atomic.LoadInt64(&s.lastActive))
				if time.Since(last) >= timeout {
					s.interrupted = errTimeout
					s.rt.Interrupt(errTimeout)
					return
				}
			}
		}
	}()
	return func() {
		if atomic.CompareAndSwapInt32(&stopped, 0, 1) {
			close(done)
		}
	}
}

// beginToolCall marks a tool as in flight so the watchdog does not charge its
// latency to the script budget. The returned function must always be called.
func (s *scriptRun) beginToolCall() func() {
	atomic.AddInt32(&s.inflight, 1)
	return func() {
		atomic.StoreInt64(&s.lastActive, time.Now().UnixNano())
		atomic.AddInt32(&s.inflight, -1)
	}
}

// encodeValue serialises a script return value. undefined and null both map to
// the empty string: there is nothing for the model to read.
func encodeValue(v goja.Value, limit int) (string, bool, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return "", false, nil
	}
	exported := v.Export()
	if s, ok := exported.(string); ok {
		text, truncated := truncateText(s, limit)
		return text, truncated, nil
	}
	raw, err := json.Marshal(exported)
	if err != nil {
		// A value that cannot be JSON-encoded (a function, a cyclic object) is a
		// script bug worth reporting as text rather than losing the run.
		text, truncated := truncateText(v.String(), limit)
		return text, truncated, nil
	}
	text, truncated := truncateText(string(raw), limit)
	return text, truncated, nil
}

func splitConsole(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}
