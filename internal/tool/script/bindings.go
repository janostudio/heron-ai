package script

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/dop251/goja"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// installTools binds the session's tools onto the `tools` global. Each binding
// is a synchronous function: there is no event loop, so a call returns the
// tool's result or throws a ToolCallError.
func (s *scriptRun) installTools() error {
	obj := s.rt.NewObject()
	for _, name := range s.toolNames {
		toolName := name
		if err := obj.Set(toolName, func(call goja.FunctionCall) goja.Value {
			return s.invoke(toolName, call, 0)
		}); err != nil {
			return fmt.Errorf("bind tool %s: %w", name, err)
		}
	}
	if err := obj.Set("call", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		if name == "" || goja.IsUndefined(call.Argument(0)) {
			panic(s.rt.NewTypeError("tools.call requires a tool name"))
		}
		return s.invoke(name, call, 1)
	}); err != nil {
		return fmt.Errorf("bind tools.call: %w", err)
	}
	if err := obj.Set("all", func(call goja.FunctionCall) goja.Value {
		return s.invokeAll(call)
	}); err != nil {
		return fmt.Errorf("bind tools.all: %w", err)
	}
	return s.rt.Set("tools", obj)
}

// invoke runs one tool call, replaying a recorded result when it matches.
// argOffset skips leading positional arguments used by the generic entry point.
func (s *scriptRun) invoke(name string, call goja.FunctionCall, argOffset int) goja.Value {
	args := map[string]any{}
	if arg := call.Argument(argOffset); arg != nil && !goja.IsUndefined(arg) && !goja.IsNull(arg) {
		exported := arg.Export()
		switch value := exported.(type) {
		case map[string]any:
			args = value
		case nil:
		default:
			panic(s.rt.NewTypeError("tool %s expects an object argument, got %T", name, exported))
		}
	}

	result, err := s.callTool(name, args)
	if err != nil {
		panic(s.toolError(name, err.Error(), nil))
	}
	if result == nil {
		result = &types.ToolResult{Success: false, Error: "tool returned no result"}
	}
	if !result.Success {
		panic(s.toolError(name, toolFailureMessage(result), result))
	}
	return s.rt.ToValue(toJSResult(result))
}

// callTool resolves replay records, enforces the call budget and the effectful
// write ordering, then delegates to the executor.
func (s *scriptRun) callTool(name string, args map[string]any) (*types.ToolResult, error) {
	if s.ctx != nil {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
	}
	step := s.step
	hash := argsHash(args)

	if rec, ok := s.recorded[step]; ok && rec.Status == stepDone && rec.ToolName == name && rec.ArgsHash == hash {
		// A replayed call still advances the position: otherwise the next call
		// would look up the same step again and miss.
		s.step++
		return rec.Result, nil
	}

	if step >= s.maxSteps {
		return nil, fmt.Errorf("tool call budget exceeded (%d calls). Narrow the loop or raise max_steps", s.maxSteps)
	}
	s.step++

	effectful := s.isEffectful(name)
	if effectful && s.runner.Store != nil {
		// Record intent before the effect lands. If the process dies mid-call the
		// next run sees started-without-result and refuses to continue rather
		// than repeating a write (design doc §5.3).
		if err := s.runner.Store.Append(s.ctx, s.scriptID, StepRecord{
			Step: step, ToolName: name, ArgsHash: hash, Status: stepStarted,
		}); err != nil {
			return nil, fmt.Errorf("record step intent: %w", err)
		}
	}

	end := s.beginToolCall()
	// Tag the context so the runtime can label the tool events it emits: without
	// this the inner call is indistinguishable from a direct one in the event
	// stream, and the UI cannot group it under its script.
	callCtx := WithStep(s.ctx, s.scriptID, step)
	result, execErr := s.runner.Caller.Execute(callCtx, name, args)
	end()
	if execErr != nil {
		result = &types.ToolResult{Success: false, Error: execErr.Error()}
	}

	rec := StepRecord{Step: step, ToolName: name, ArgsHash: hash, Status: stepDone, Result: result}
	if s.runner.Store != nil {
		// A result that is not durable would be re-executed on the next turn.
		// Failing the script is the safe outcome; silently continuing is not.
		if err := s.runner.Store.Append(s.ctx, s.scriptID, rec); err != nil {
			return nil, fmt.Errorf("record step result: %w", err)
		}
	}
	s.recordStep(rec)
	return result, nil
}

// isEffectful defaults to true when the executor cannot describe the tool:
// treating an unknown tool as read-only is the failure mode that repeats writes.
func (s *scriptRun) isEffectful(name string) bool {
	provider, ok := s.runner.Caller.(SpecProvider)
	if !ok {
		return true
	}
	return provider.ExecutionSpec(name).Class != types.ToolReadOnly
}

// batchItem is one entry of tools.all([...]).
type batchItem struct {
	name string
	args map[string]any
}

// invokeAll runs a batch of independent calls. Read-only members run
// concurrently, effectful ones in submission order, and the result array keeps
// the caller's order either way.
//
// The contract is "these calls do not depend on each other": that is what makes
// the concurrency sound. A script that needs one call's output as another's
// input must write them as two statements.
func (s *scriptRun) invokeAll(call goja.FunctionCall) goja.Value {
	arg := call.Argument(0)
	if arg == nil || goja.IsUndefined(arg) || goja.IsNull(arg) {
		panic(s.rt.NewTypeError("tools.all requires an array of {name, args} entries"))
	}
	items, err := parseBatchItems(arg.Export())
	if err != nil {
		panic(s.rt.NewTypeError("tools.all: %s", err.Error()))
	}
	results, err := s.callToolBatch(items)
	if err != nil {
		panic(s.toolError("tools.all", err.Error(), nil))
	}
	out := make([]any, len(results))
	for i, result := range results {
		out[i] = toJSResult(result)
	}
	return s.rt.ToValue(out)
}

func parseBatchItems(exported any) ([]batchItem, error) {
	raw, ok := exported.([]any)
	if !ok {
		return nil, fmt.Errorf("expected an array, got %T", exported)
	}
	items := make([]batchItem, 0, len(raw))
	for i, entry := range raw {
		object, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entry %d must be an object, got %T", i, entry)
		}
		name, _ := object["name"].(string)
		if name == "" {
			if alias, ok := object["tool"].(string); ok {
				name = alias
			}
		}
		if name == "" {
			return nil, fmt.Errorf("entry %d is missing \"name\"", i)
		}
		args := map[string]any{}
		if raw, ok := object["args"].(map[string]any); ok {
			args = raw
		}
		items = append(items, batchItem{name: name, args: args})
	}
	return items, nil
}

// batchCall is one resolved slot: its step number is assigned up front so the
// replay log stays dense even when the calls run concurrently.
type batchCall struct {
	index     int
	step      int
	name      string
	hash      string
	args      map[string]any
	effectful bool
}

func (s *scriptRun) callToolBatch(items []batchItem) ([]*types.ToolResult, error) {
	if s.ctx != nil {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
	}
	results := make([]*types.ToolResult, len(items))
	var todo []batchCall

	for i, item := range items {
		step := s.step
		hash := argsHash(item.args)
		if rec, ok := s.recorded[step]; ok && rec.Status == stepDone && rec.ToolName == item.name && rec.ArgsHash == hash {
			s.step++
			results[i] = rec.Result
			continue
		}
		if step >= s.maxSteps {
			return nil, fmt.Errorf("tool call budget exceeded (%d calls). Narrow the loop or raise max_steps", s.maxSteps)
		}
		s.step++
		todo = append(todo, batchCall{
			index:     i,
			step:      step,
			name:      item.name,
			hash:      hash,
			args:      item.args,
			effectful: s.isEffectful(item.name),
		})
	}

	for _, call := range todo {
		if !call.effectful {
			continue
		}
		result, err := s.executeBatchCall(call)
		if err != nil {
			return nil, err
		}
		results[call.index] = result
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, maxBatchConcurrency)
	errCh := make(chan error, len(todo))
	for _, call := range todo {
		if call.effectful {
			continue
		}
		wg.Add(1)
		go func(call batchCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			result, err := s.executeBatchCall(call)
			if err != nil {
				errCh <- err
				return
			}
			results[call.index] = result
		}(call)
	}
	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		return nil, err
	}
	return results, nil
}

// maxBatchConcurrency keeps a wide tools.all from opening one goroutine per
// entry; the value mirrors the engine's default parallel tool limit.
const maxBatchConcurrency = 8

// executeBatchCall is callTool's body for a batch slot whose step is already
// assigned. It records intent and result exactly like the sequential path.
func (s *scriptRun) executeBatchCall(call batchCall) (*types.ToolResult, error) {
	if call.effectful && s.runner.Store != nil {
		if err := s.runner.Store.Append(s.ctx, s.scriptID, StepRecord{
			Step: call.step, ToolName: call.name, ArgsHash: call.hash, Status: stepStarted,
		}); err != nil {
			return nil, fmt.Errorf("record step intent: %w", err)
		}
	}

	end := s.beginToolCall()
	callCtx := WithStep(s.ctx, s.scriptID, call.step)
	result, execErr := s.runner.Caller.Execute(callCtx, call.name, call.args)
	end()
	if execErr != nil {
		result = &types.ToolResult{Success: false, Error: execErr.Error()}
	}

	rec := StepRecord{Step: call.step, ToolName: call.name, ArgsHash: call.hash, Status: stepDone, Result: result}
	if s.runner.Store != nil {
		if err := s.runner.Store.Append(s.ctx, s.scriptID, rec); err != nil {
			return nil, fmt.Errorf("record step result: %w", err)
		}
	}
	s.recordStep(rec)
	return result, nil
}

// recordStep appends to the execution summary under a lock: batch members run
// concurrently, and the summary is read by the caller afterwards.
func (s *scriptRun) recordStep(rec StepRecord) {
	s.stepsMu.Lock()
	defer s.stepsMu.Unlock()
	s.exec.Steps = append(s.exec.Steps, rec)
}

// toolError builds the JS-visible error. It is a real Error instance so an
// uncaught throw reports the message rather than "[object Object]", with the
// name overridden so a script can branch on it:
// catch (e) { if (e.name === "ToolCallError") ... }
func (s *scriptRun) toolError(toolName, message string, result *types.ToolResult) *goja.Object {
	errCtor, ok := goja.AssertConstructor(s.rt.Get("Error"))
	if !ok {
		return s.rt.NewTypeError("%s: %s", toolName, message)
	}
	value, err := errCtor(nil, s.rt.ToValue(toolName+": "+message))
	if err != nil || value == nil {
		return s.rt.NewTypeError("%s: %s", toolName, message)
	}
	obj := value.ToObject(s.rt)
	_ = obj.Set("name", "ToolCallError")
	_ = obj.Set("tool", toolName)
	if result != nil {
		_ = obj.Set("result", s.rt.ToValue(toJSResult(result)))
	}
	return obj
}

// toJSResult projects a ToolResult onto the shape the catalog promises. These
// field names are the contract the model writes against, so they must match
// what catalog.go prints for the tool.
func toJSResult(result *types.ToolResult) map[string]any {
	out := map[string]any{
		"ok":      result.Success,
		"content": result.Content,
	}
	if result.Error != "" {
		out["error"] = result.Error
	}
	if len(result.Metadata) > 0 {
		out["metadata"] = result.Metadata
	}
	if len(result.WorkspaceOps) > 0 {
		ops := make([]map[string]any, 0, len(result.WorkspaceOps))
		for _, op := range result.WorkspaceOps {
			ops = append(ops, map[string]any{
				"kind":     op.Kind,
				"path":     op.Path,
				"revision": op.Revision,
			})
		}
		out["workspace_ops"] = ops
	}
	return out
}

func toolFailureMessage(result *types.ToolResult) string {
	if result.Error != "" {
		return result.Error
	}
	return "tool reported failure without a message"
}

// argsHash canonicalises arguments so a replay can tell "same call" from "same
// position, different call". json.Marshal sorts map keys, so equal maps always
// produce equal bytes; arrays keep their order, which is intended.
func argsHash(args map[string]any) string {
	if len(args) == 0 {
		return "0"
	}
	raw, err := json.Marshal(args)
	if err != nil {
		// Non-JSON-able arguments (a function passed as an argument) already fail
		// validation in the executor; a marker keeps the step distinct.
		return "unhashable"
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:12])
}

// sortedToolNames keeps binding order stable so the runtime's object property
// order does not depend on map iteration.
func sortedToolNames(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}
