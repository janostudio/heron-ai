package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Tool names. They are PascalCase like every other builtin (Team, Collect,
// Spawn): the naming scheme is what lets a model tell a builtin from an MCP
// tool at a glance.
const (
	ScheduleWakeupToolName = "ScheduleWakeup"
	CancelWakeupToolName   = "CancelWakeup"
)

// ScheduleWakeupTool lets a model schedule a one-off wake-up for itself.
//
// It is the only way a session can start a turn without an external event:
// everything else is a response to something that already happened. The tool
// performs no work — it records a note and returns, so the turn can end and
// the session can go idle costing nothing until the time comes.
type ScheduleWakeupTool struct {
	scheduler types.WakeupScheduler
}

func NewScheduleWakeupTool(scheduler types.WakeupScheduler) *ScheduleWakeupTool {
	return &ScheduleWakeupTool{scheduler: scheduler}
}

// SetScheduler wires the flow runtime after it exists: the tool registry is
// built first, so this cannot be a constructor argument.
func (t *ScheduleWakeupTool) SetScheduler(scheduler types.WakeupScheduler) {
	if t == nil {
		return
	}
	t.scheduler = scheduler
}

func (t *ScheduleWakeupTool) Name() string { return ScheduleWakeupToolName }

func (t *ScheduleWakeupTool) Description() string {
	return "Schedule a one-off wake-up for yourself at a later time. When it fires you get back " +
		"the note you wrote, plus what changed in the meantime.\n\n" +
		"It fires once. It is not a cron and does not repeat — to look again later, call this " +
		"again from the wake-up turn.\n\n" +
		"Use it ONLY to come back to something nothing will notify you about — for example " +
		"whether a long-running task is still on the right track. Do NOT use it to wait for " +
		"completion: work handed to a Team or Spawn notifies you when it finishes, and polling " +
		"for it burns tokens for nothing.\n\n" +
		"If a check finds nothing that needs attention, do nothing: do not report, do not " +
		"schedule again."
}

func (t *ScheduleWakeupTool) Parameters() map[string]any {
	return map[string]any{
		"when": map[string]any{
			"type": "string",
			"description": "How far in the future: a duration like \"30m\", \"6h\" or \"2d\", " +
				"or an absolute RFC3339 timestamp with a timezone",
		},
		"note": map[string]any{
			"type": "string",
			"description": "What your future self should check, and what to do about what it finds. " +
				"This is handed back verbatim — write it as an instruction, not a reminder label",
		},
	}
}

func (t *ScheduleWakeupTool) NeedsApproval() bool { return false }

// Execution is serial: registering a wake-up appends to the session event
// stream, and two registrations racing would interleave their quota checks.
func (t *ScheduleWakeupTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial, RestartSafe: true}
}

// Execute records the wake-up and returns immediately. It never waits: the
// whole point is that the caller's turn can end.
func (t *ScheduleWakeupTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || t.scheduler == nil {
		return wakeupToolError("ScheduleWakeup is not configured"), nil
	}
	identity := spawnIdentityFromContext(ctx)
	if identity == nil {
		return wakeupToolError("ScheduleWakeup is only available inside an Agent turn"), nil
	}
	if identity.req.FlowSessionID == "" {
		return wakeupToolError("ScheduleWakeup requires a Flow session"), nil
	}

	// Validate here as well as in the runtime: the model should learn what is
	// wrong from the tool it called, not from a scheduler rejection one layer
	// down.
	note := strings.TrimSpace(stringParam(params, "note"))
	if note == "" {
		return wakeupToolError("note is required: what your future self should check, and what to do about what it finds"), nil
	}
	now := time.Now().UTC()
	fireAt, err := parseWakeupWhen(stringParam(params, "when"), now)
	if err != nil {
		return wakeupToolError(err.Error()), nil
	}
	record, err := t.scheduler.ScheduleWakeup(ctx, types.WakeupRequest{
		FlowSessionID: identity.req.FlowSessionID,
		TeamID:        identity.req.TeamID,
		CallID:        identity.req.CallID,
		FireAt:        fireAt,
		Note:          note,
	})
	if err != nil {
		return wakeupToolError(err.Error()), nil
	}

	payload, _ := json.Marshal(map[string]any{
		"wakeup_id":   record.ID,
		"fire_at":     record.FireAt.Format(time.RFC3339),
		"team_id":     record.TeamID,
		"instruction": "Registered. End the turn if you have nothing else to do; you will be woken with this note.",
	})
	return &types.ToolResult{
		Success: true,
		Content: string(payload),
		Metadata: map[string]any{
			"wakeup_id": record.ID,
			"fire_at":   record.FireAt.Format(time.RFC3339),
			"wait":      false,
		},
	}, nil
}

// CancelWakeupTool withdraws a wake-up that no longer matters.
type CancelWakeupTool struct {
	scheduler types.WakeupScheduler
}

func NewCancelWakeupTool(scheduler types.WakeupScheduler) *CancelWakeupTool {
	return &CancelWakeupTool{scheduler: scheduler}
}

func (t *CancelWakeupTool) SetScheduler(scheduler types.WakeupScheduler) {
	if t == nil {
		return
	}
	t.scheduler = scheduler
}

func (t *CancelWakeupTool) Name() string { return CancelWakeupToolName }

func (t *CancelWakeupTool) Description() string {
	return "Cancel a wake-up you scheduled earlier. Use it when the thing you were going to check " +
		"no longer needs checking. Cancelling one that already fired is a no-op."
}

func (t *CancelWakeupTool) Parameters() map[string]any {
	return map[string]any{
		"wakeup_id": map[string]any{
			"type":        "string",
			"description": "The wakeup_id returned by ScheduleWakeup",
		},
	}
}

func (t *CancelWakeupTool) NeedsApproval() bool { return false }

func (t *CancelWakeupTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial, RestartSafe: true}
}

func (t *CancelWakeupTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || t.scheduler == nil {
		return wakeupToolError("CancelWakeup is not configured"), nil
	}
	identity := spawnIdentityFromContext(ctx)
	if identity == nil || identity.req.FlowSessionID == "" {
		return wakeupToolError("CancelWakeup is only available inside a Flow session"), nil
	}
	wakeupID := strings.TrimSpace(stringParam(params, "wakeup_id"))
	if wakeupID == "" {
		return wakeupToolError("CancelWakeup requires a wakeup_id"), nil
	}
	if err := t.scheduler.CancelWakeup(ctx, identity.req.FlowSessionID, wakeupID); err != nil {
		return wakeupToolError(err.Error()), nil
	}
	payload, _ := json.Marshal(map[string]any{"wakeup_id": wakeupID, "cancelled": true})
	return &types.ToolResult{Success: true, Content: string(payload)}, nil
}

// parseWakeupWhen accepts a duration ("30m", "6h", "2d") or an absolute
// RFC3339 timestamp. A bare number is rejected rather than guessed: "6" could
// mean six minutes or six hours, and a wake-up that fires at the wrong scale
// is worse than one that fails.
func parseWakeupWhen(raw string, now time.Time) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, fmt.Errorf("when is required: a duration like \"30m\" or an RFC3339 timestamp")
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), nil
	}
	delay, err := parseWakeupDelay(value)
	if err != nil {
		return time.Time{}, err
	}
	return now.Add(delay).UTC(), nil
}

// parseWakeupDelay parses a delay, accepting the day suffix that
// time.ParseDuration does not ("2d"). Days matter here: the whole reason this
// tool exists is work measured in hours and days.
func parseWakeupDelay(value string) (time.Duration, error) {
	if delay, err := time.ParseDuration(value); err == nil {
		if delay <= 0 {
			return 0, fmt.Errorf("when must be in the future")
		}
		return delay, nil
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return 0, fmt.Errorf(
		"when must be a duration like \"30m\", \"6h\", \"2d\" or an RFC3339 timestamp; got %q", value)
}

func wakeupToolError(message string) *types.ToolResult {
	return &types.ToolResult{Success: false, Error: message}
}
