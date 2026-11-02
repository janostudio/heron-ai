package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Wake-up outcomes that are not failures. The scheduler reads them as "retry
// later" and "stop trying"; anything else is an error worth logging.
var (
	// errWakeupDeferred means the session is not idle enough to take a turn
	// right now. The record stays scheduled and the caller retries.
	errWakeupDeferred = errors.New("wake-up deferred: session is not idle")
	// errWakeupTerminal means the conversation is over and can never be woken
	// again. The record is dropped.
	errWakeupTerminal = errors.New("wake-up dropped: session is in a terminal state")
)

// wakeupDeferredError says why a due wake-up was postponed. The reason travels
// into the event stream: the model's next wake-up reads "I was pushed back
// because the user had just spoken" and can adjust, where a single
// "session was busy" tells it nothing it can act on.
type wakeupDeferredError struct {
	reason string
}

func (e wakeupDeferredError) Error() string { return "wake-up deferred: " + e.reason }

// Is keeps errors.Is(err, errWakeupDeferred) working for callers that only
// care about the category.
func (e wakeupDeferredError) Is(target error) bool { return target == errWakeupDeferred }

func deferWakeup(reason string) error { return wakeupDeferredError{reason: reason} }

func wakeupDeferralReason(err error) string {
	var deferred wakeupDeferredError
	if errors.As(err, &deferred) && strings.TrimSpace(deferred.reason) != "" {
		return deferred.reason
	}
	return "session was busy when the wake-up came due"
}

// ResumeForWakeup runs one wake-up: a new FlowTurn that starts at the Team
// which scheduled it, carrying that Team's own note plus what changed since.
//
// The turn is not a response to anything the user said. Its input blocks are
// placed in the system section, its origin is turnOriginWakeup, and nothing
// about it reaches the conversation as a user message — a model that mistook
// its own note for a user request would re-plan the whole task instead of
// checking on it.
func (r *Runtime) ResumeForWakeup(ctx context.Context, sessionID string, record types.WakeupRecord) (types.FlowTurnResult, error) {
	defer r.resumeLock(sessionID)()

	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}

	switch session.Status {
	case types.SessionFailed, types.SessionCancelled:
		return types.FlowTurnResult{}, errWakeupTerminal
	case types.SessionRunning, types.SessionWaitingTool, types.SessionWaitingApproval:
		// A turn is in flight, or the session is parked on a Tool/approval
		// that will wake it itself. HandleInputWithContext refuses a second
		// turn in exactly these states for the same reason: the first one
		// would keep running with nobody collecting it.
		return types.FlowTurnResult{}, deferWakeup("the session was " + string(session.Status))
	}
	if r.isTurnRunning(sessionID) {
		return types.FlowTurnResult{}, deferWakeup("a turn was still running in this process")
	}
	if running, runningErr := r.runningTeams(ctx, sessionID); runningErr != nil {
		return types.FlowTurnResult{}, runningErr
	} else if len(running) > 0 {
		return types.FlowTurnResult{}, deferWakeup("teams were still running: " + strings.Join(running, ", "))
	}
	if quiet := r.wakeupLimits().QuietAfterUserTurn; quiet > 0 {
		if last := r.lastUserTurnAt(ctx, sessionID); !last.IsZero() && time.Since(last) < quiet {
			return types.FlowTurnResult{}, deferWakeup(fmt.Sprintf(
				"the user spoke %s ago, inside the %s quiet window", time.Since(last).Round(time.Second), quiet))
		}
	}

	defs := r.definitions.Snapshot()
	now := time.Now().UTC()

	// Record the firing before the turn starts: if the process dies mid-turn,
	// the event is what recovery reads to know this wake-up was already spent.
	fired := record
	fired.Status = types.WakeupFired
	fired.FiredAt = &now
	fired.LastSeq = r.latestSessionSeq(ctx, sessionID)
	if err := r.markWakeup(ctx, fired, types.EventWakeupFired, nil); err != nil {
		return types.FlowTurnResult{}, err
	}

	blocks := wakeupBlocks(record, r.buildWakeupFacts(ctx, sessionID, record, now))
	logging.Info("wake-up firing", map[string]any{
		"flow_session_id": sessionID,
		"team_id":         record.TeamID,
		"wakeup_id":       record.ID,
		"late_by_ms":      now.Sub(record.FireAt).Milliseconds(),
	})
	// The note travels in the system blocks, not as the turn's input: input is
	// rendered where a user message would be, which is the one place this text
	// must never appear.
	return r.runTurnWithOrigin(ctx, defs, session, "", blocks,
		[]activation{{teamID: record.TeamID}}, turnOriginWakeup, record.CreatedAt, nil)
}

// wakeupBlocks builds the two blocks a wake-up turn carries: what this is, and
// what changed. Both are system-placed, stable-first.
func wakeupBlocks(record types.WakeupRecord, facts string) []types.ContextBlock {
	blocks := []types.ContextBlock{{
		Kind: "wakeup_note",
		Text: fmt.Sprintf(
			"You scheduled this wake-up yourself at %s. It is not a message from the user.\n\nWhat you asked yourself to check:\n%s",
			record.CreatedAt.Format("2006-01-02 15:04"), record.Note),
		Placement: "system",
		Stability: "stable",
		Priority:  95,
	}}
	if strings.TrimSpace(facts) != "" {
		blocks = append(blocks, types.ContextBlock{
			Kind:      "wakeup_facts",
			Text:      facts,
			Placement: "system",
			Stability: "dynamic",
			Priority:  90,
		})
	}
	return blocks
}

// buildWakeupFacts summarises what happened since the session last saw this
// wake-up. Every line is a fact read from the event stream — task state, tool
// calls, published records, user turns, earlier deferrals. It deliberately
// draws no conclusion: "no output for 40 minutes" is reported as a duration,
// not as "stalled", because deciding that is the model's job.
func (r *Runtime) buildWakeupFacts(ctx context.Context, sessionID string, record types.WakeupRecord, now time.Time) string {
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return ""
	}

	var (
		actions   []string
		records   []string
		userTurns int
		skipped   int
		lastSeen  = now
	)
	for _, event := range replay.Events {
		if event.Seq <= record.LastSeq {
			continue
		}
		switch event.Type {
		case types.EventToolCallCompleted, types.EventToolCallFailed:
			if record.TeamID != "" && event.TeamID != "" && event.TeamID != record.TeamID {
				continue
			}
			if line := describeToolAction(event, now); line != "" {
				actions = append(actions, line)
			}
		case types.EventSharedRecordPublished:
			if name := publishedRecordLabel(event.Payload); name != "" {
				records = append(records, name)
			}
		case types.EventFlowTurnStarted:
			if origin, _ := turnOriginAndStart(event.Payload); origin == turnOriginUser {
				userTurns++
			}
		case types.EventWakeupSkipped:
			skipped++
		}
		if event.CreatedAt.After(lastSeen) {
			lastSeen = event.CreatedAt
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Now: %s. You scheduled this at %s.\n",
		now.Format("2006-01-02 15:04 UTC"), record.CreatedAt.Format("2006-01-02 15:04 UTC"))

	if len(actions) == 0 {
		b.WriteString("Downstream activity since then: none recorded.\n")
	} else {
		if len(actions) > wakeupFactsActions {
			actions = actions[len(actions)-wakeupFactsActions:]
		}
		fmt.Fprintf(&b, "Downstream activity since then (%d most recent, oldest first):\n", len(actions))
		for _, line := range actions {
			b.WriteString("  - ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	if len(records) > 0 {
		fmt.Fprintf(&b, "Records published: %s\n", strings.Join(records, "、"))
	} else {
		b.WriteString("Records published: none.\n")
	}
	fmt.Fprintf(&b, "User turns since then: %d.\n", userTurns)
	if skipped > 0 {
		fmt.Fprintf(&b, "Earlier wake-ups deferred because the session was busy: %d.\n", skipped)
	}
	return truncateRunes(b.String(), maxWakeupFactsChars)
}

// describeToolAction renders one finished tool call. Failures carry the error
// text; successes carry a short argument summary, which is what tells a reader
// whether the work is still on track.
func describeToolAction(event storage.SessionEvent, now time.Time) string {
	name, _ := event.Payload["tool_name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	status := "ok"
	detail := ""
	if event.Type == types.EventToolCallFailed {
		status = "failed"
		if message, ok := event.Payload["error"].(string); ok {
			detail = truncateRunes(strings.TrimSpace(message), 120)
		}
	} else if args, ok := event.Payload["arguments"]; ok {
		detail = summarizeArguments(args)
	}
	age := humanAge(now.Sub(event.CreatedAt))
	if detail == "" {
		return fmt.Sprintf("[%s] %s — %s ago", status, name, age)
	}
	return fmt.Sprintf("[%s] %s %s — %s ago", status, name, detail, age)
}

// summarizeArguments flattens a tool call's arguments into one short line. The
// goal is recognisability ("go test ./internal/runtime"), not fidelity.
func summarizeArguments(raw any) string {
	data, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	text = strings.TrimPrefix(text, "{")
	text = strings.TrimSuffix(text, "}")
	text = strings.Trim(text, `"`)
	text = strings.ReplaceAll(text, `","`, ", ")
	text = strings.ReplaceAll(text, `":"`, "=")
	text = strings.ReplaceAll(text, `":`, "=")
	return truncateRunes(text, 120)
}

// publishedRecordLabel names one published record for the facts block. The
// summary is deliberately not included: it can be a whole document, and the
// model can read the record itself when it wants the detail.
func publishedRecordLabel(payload map[string]any) string {
	record, ok := payload["record"]
	if !ok {
		return ""
	}
	data, err := json.Marshal(record)
	if err != nil {
		return ""
	}
	var parsed struct {
		RecordID string `json:"record_id"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ""
	}
	name := strings.TrimSpace(parsed.Name)
	if name == "" {
		name = strings.TrimSpace(parsed.RecordID)
	}
	if name == "" {
		return ""
	}
	if parsed.RecordID != "" && parsed.Name != "" {
		return fmt.Sprintf("%s (%s)", name, parsed.RecordID)
	}
	return name
}

// humanAge renders a duration the way a report reads: 45s / 12m / 3h05m.
func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%02dm", hours, minutes)
	}
}

// lastUserTurnAt is when the user last spoke in this session, zero if never.
// It is what keeps a background wake-up from talking over a live conversation.
func (r *Runtime) lastUserTurnAt(ctx context.Context, sessionID string) time.Time {
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return time.Time{}
	}
	var latest time.Time
	for _, event := range replay.Events {
		if event.Type != types.EventFlowTurnStarted {
			continue
		}
		origin, startedAt := turnOriginAndStart(event.Payload)
		if origin != turnOriginUser || startedAt.IsZero() {
			continue
		}
		if startedAt.After(latest) {
			latest = startedAt
		}
	}
	return latest
}
