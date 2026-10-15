package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ResumeForMailbox drains the entry team's pending inbox messages and starts a
// new FlowTurn so the entry team can act on them. It is the wake-up path
// for async mailbox dispatch: unlike ResumeWithContext it does NOT consult
// pendingTeamResume/pendingAgentResume (those are for tool/agent resume), so it
// cannot mis-route when there are unrelated pending items.
//
// It is deliberately forgiving. A wake-up can be raced by another turn the
// session starts on its own, or arrive for a session that has already
// reached a terminal state; neither is an error the caller can act on, and the
// result message simply stays in the inbox for the next turn to drain. In those
// cases ResumeForMailbox returns a zero-value FlowTurnResult and a nil error
// rather than failing. Errors are reserved for the cases a caller can act on:
// a missing session, or a turn that actually started and then failed.
func (r *Runtime) ResumeForMailbox(ctx context.Context, sessionID string) (types.FlowTurnResult, error) {
	r.resumeMu.Lock()
	defer r.resumeMu.Unlock()

	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}

	switch session.Status {
	case types.SessionWaitingInput, types.SessionWaitingTool, types.SessionWaitingApproval, types.SessionCreated:
		// Idle enough to wake.
	default:
		// Running (a wake raced another turn) or terminal (failed/cancelled/
		// interrupted): wake-ups are dropped silently. The result message
		// stays in the inbox; a later turn drains it.
		return types.FlowTurnResult{}, nil
	}

	defs := r.definitions.Snapshot()

	// Wake whoever has mail, not a hard-coded Team. A dispatched Team's result
	// goes back to its activator, so any Team can be the one waiting; the
	// entry Team is just the Team a user's turn starts at. The pick is by
	// pending mail rather than by role, which is what makes the wake-up path
	// the same for every Team.
	target, messages, err := r.nextWakingTeam(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if target == "" || len(messages) == 0 {
		// No mail for anyone; nothing to summarize.
		return types.FlowTurnResult{}, nil
	}

	// Consume the messages: they are about to be folded into the new turn, so
	// they must no longer be pending. A failure to record a delivery is
	// logged rather than escalated, mirroring the async dispatch path — a
	// bookkeeping failure must not turn a waking turn into a failed one.
	for _, msg := range messages {
		if _, err := r.deliverMessage(ctx, session, msg, nil); err != nil {
			logging.Error("deliver mailbox message on resume", map[string]any{
				"flow_session_id": session.ID,
				"message_id":      msg.MessageID,
				"error":           err.Error(),
			})
		}
	}

	block := types.ContextBlock{
		Kind:      "mailbox",
		Text:      renderMailboxInbox(messages),
		Source:    "mailbox",
		Stability: "dynamic",
		Priority:  85,
	}
	// Wake the Team that has mail: its turn starts at that Team rather than at
	// the Flow entry. The entry Team keeps its usual start when it is the one
	// with mail, so a plain user turn is unchanged.
	//
	// The activation carries the Team's activator so its round routes back to
	// the right place. A Team first woken by a work message takes that
	// message's caller_team; a Team woken by its own dispatches' results has
	// already been activated (its work message is long delivered) and starts
	// with no caller, which is the same "no activator" case as the entry Team.
	wake := activation{teamID: target, callerTeam: activatorFromMessages(messages)}
	return r.runTurnWithActivationsAndContext(ctx, defs, session, "", []types.ContextBlock{block},
		[]activation{wake})
}

// activatorFromMessages returns the Team that dispatched the work whose mail is
// being drained. Work messages carry caller_team; result messages do not, so
// only the former can name an activator.
func activatorFromMessages(messages []types.MailboxMessage) string {
	for _, msg := range messages {
		if msg.Kind != mailboxKindWork {
			continue
		}
		if caller, ok := msg.Payload["caller_team"].(string); ok && caller != "" {
			return caller
		}
	}
	return ""
}

// nextWakingTeam picks the Team whose inbox holds pending mail, together with
// that mail. Teams are considered in a stable order (descending mail count,
// then TeamID) so a wake-up is deterministic when several Teams have mail.
// It returns an empty Team when nobody has mail.
func (r *Runtime) nextWakingTeam(ctx context.Context, sessionID string) (string, []types.MailboxMessage, error) {
	box, err := r.loadMailbox(ctx, sessionID)
	if err != nil {
		return "", nil, err
	}
	byTeam := make(map[string][]types.MailboxMessage)
	for _, msg := range box {
		if msg.Status != types.MailboxPending || msg.To.TeamID == "" {
			continue
		}
		byTeam[msg.To.TeamID] = append(byTeam[msg.To.TeamID], msg)
	}
	if len(byTeam) == 0 {
		return "", nil, nil
	}
	best := ""
	for team := range byTeam {
		if best == "" || len(byTeam[team]) > len(byTeam[best]) ||
			(len(byTeam[team]) == len(byTeam[best]) && team < best) {
			best = team
		}
	}
	messages := byTeam[best]
	sort.Slice(messages, func(i, j int) bool {
		if !messages[i].CreatedAt.Equal(messages[j].CreatedAt) {
			return messages[i].CreatedAt.Before(messages[j].CreatedAt)
		}
		return messages[i].MessageID < messages[j].MessageID
	})
	return best, messages, nil
}

// renderMailboxInbox renders a Team's inbox messages into a single
// human-readable text block for the waking turn. Each message contributes its
// originating team and, when present and structurally sound, the dispatched
// team's Reply (team_result is a TeamTurnResult whose reply field is the
// published PascalCase container field Reply).
//
// The renderer never fails and never panics: a message whose team_result is
// missing or of an unexpected shape degrades to a header carrying the message
// ID and whatever human-readable summary is available, so a malformed payload
// still shows up in the turn rather than silently vanishing.
func renderMailboxInbox(messages []types.MailboxMessage) string {
	if len(messages) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Mailbox results from dispatched teams:\n")
	for i, msg := range messages {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## Message %s", msg.MessageID)
		if msg.From.TeamID != "" {
			fmt.Fprintf(&b, " from %s", msg.From.TeamID)
		}
		b.WriteString("\n")

		if reply, ok := mailboxResultReply(msg.Payload); ok && reply != "" {
			b.WriteString(reply)
			b.WriteString("\n")
			continue
		}
		// Degrade gracefully: no usable team_result.Reply. Fall back to any
		// coarse error/summary the payload carries, else say so explicitly.
		if summary := mailboxResultSummary(msg.Payload); summary != "" {
			b.WriteString(summary)
			b.WriteString("\n")
			continue
		}
		b.WriteString("(no rendered reply; raw result unavailable)\n")
	}
	return b.String()
}

// mailboxResultReply extracts team_result.Reply from a message payload. The
// team_result value is a TeamTurnResult that arrives either as the typed struct
// (in-memory) or as a generic map (after a JSONL round-trip), so the value is
// re-marshalled into the struct to read the published Reply field.
func mailboxResultReply(payload map[string]any) (string, bool) {
	raw, ok := payload["team_result"]
	if !ok {
		return "", false
	}
	var result types.TeamTurnResult
	switch v := raw.(type) {
	case types.TeamTurnResult:
		result = v
	case map[string]any:
		data, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return "", false
		}
	default:
		return "", false
	}
	return result.Reply, true
}

// mailboxResultSummary extracts a coarse human-readable summary from a message
// payload when team_result.Reply is unavailable: the dispatch-recorded error
// first, then the message's own recorded result. It returns "" when nothing
// usable is present.
func mailboxResultSummary(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if e, ok := payload["error"].(string); ok && strings.TrimSpace(e) != "" {
		return "error: " + e
	}
	if raw, ok := payload["result"]; ok {
		if data, err := json.Marshal(raw); err == nil {
			return string(data)
		}
	}
	return ""
}
