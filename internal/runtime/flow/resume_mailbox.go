package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

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
		logging.Debug("resume for mailbox skipped: session not idle", map[string]any{
			"flow_session_id": sessionID,
			"session_status":  string(session.Status),
		})
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
		// No mail for anyone; nothing to summarize. Every Team with mail is
		// also skipped when it is still executing, so this fires both for an
		// empty inbox and for one whose only mail belongs to a running Team.
		logging.Debug("resume for mailbox skipped: no drainable mail", map[string]any{
			"flow_session_id": sessionID,
			"team_id":         target,
		})
		return types.FlowTurnResult{}, nil
	}

	// Claim the messages before running the turn, and consume them only after it
	// succeeds. Delivering first (the old order) meant a turn that failed to
	// start — or a process that died before it ran — left the messages marked
	// delivered while nothing had consumed them: the result the user was
	// waiting for disappeared with no dead-letter record and no way to retry.
	// Claiming takes them out of the pending set just as effectively, but says
	// "in progress", which a crash can be recognised from.
	claimed := make([]types.MailboxMessage, 0, len(messages))
	for _, msg := range messages {
		updated, claimErr := r.claimMessage(ctx, session, msg)
		if claimErr != nil {
			logging.Error("claim mailbox message on resume", map[string]any{
				"flow_session_id": session.ID,
				"message_id":      msg.MessageID,
				"error":           claimErr.Error(),
			})
			continue
		}
		claimed = append(claimed, updated)
	}

	// One block per sender, so the renderer can turn each into its own message
	// named after that Team. A single concatenated block would carry no source.
	blocks := mailboxBlocks(messages)
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
	logging.Debug("resume for mailbox waking team", map[string]any{
		"flow_session_id": sessionID,
		"team_id":         target,
		"caller_team":     wake.callerTeam,
		"message_count":   len(messages),
	})
	result, runErr := r.runTurnWithOrigin(ctx, defs, session, taskInputFromMessages(messages), blocks,
		[]activation{wake}, turnOriginMailbox,
		r.taskChainSince(ctx, sessionID, mailboxScopeSince(messages)),
		userDeliveries(messages, defs.Flow.EntryTeamID))

	// Bookkeeping failures stay logged, never escalated: they must not turn a
	// completed turn into a failed one.
	for _, msg := range claimed {
		if runErr != nil {
			// The mail was not consumed. Record why, so the loss is visible in
			// the stream instead of the message sitting claimed forever.
			if _, deadErr := r.markMessageDead(ctx, session, msg, "resume turn failed: "+runErr.Error()); deadErr != nil {
				logging.Error("mark mailbox message dead on resume", map[string]any{
					"flow_session_id": session.ID,
					"message_id":      msg.MessageID,
					"error":           deadErr.Error(),
				})
			}
			continue
		}
		if _, deliverErr := r.deliverMessage(ctx, session, msg, nil); deliverErr != nil {
			logging.Error("deliver mailbox message on resume", map[string]any{
				"flow_session_id": session.ID,
				"message_id":      msg.MessageID,
				"error":           deliverErr.Error(),
			})
		}
	}
	return result, runErr
}

// taskChainSince returns where the task the woken Team is working on started:
// the most recent user-originated FlowTurn.
//
// The mailbox timestamp is not a safe boundary on its own. A result message is
// enqueued a millisecond or two after the record it announces is published
// (s15: OwnerReply at 05:47:17.234, the result message at 05:47:17.235), so
// the record that just arrived fell outside the window, the coordinator saw
// only the user's original question, and re-dispatched the task instead of
// relaying the answer. The user's turn is where the current question started,
// which is exactly what "produced for an earlier question" must be measured
// against: records from the previous question stay out, everything produced
// while answering this one stays in.
func (r *Runtime) taskChainSince(ctx context.Context, sessionID string, fallback time.Time) time.Time {
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return fallback
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
	if latest.IsZero() {
		return fallback
	}
	return latest
}

// turnOriginAndStart reads origin and start time out of a turn.started payload.
// The payload carries a typed value when the event is still in memory and a
// decoded map after a JSONL round-trip; both shapes must work.
func turnOriginAndStart(payload map[string]any) (string, time.Time) {
	if payload == nil {
		return "", time.Time{}
	}
	switch turn := payload["turn"].(type) {
	case types.FlowTurn:
		return turn.Origin, turn.StartedAt
	case map[string]any:
		origin, _ := turn["origin"].(string)
		return origin, payloadTime(turn["started_at"])
	default:
		return "", time.Time{}
	}
}

// payloadTime decodes a timestamp that may still be typed or already be a JSON
// string after a replay.
func payloadTime(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed
	case string:
		if at, err := time.Parse(time.RFC3339Nano, typed); err == nil {
			return at
		}
		if at, err := time.Parse(time.RFC3339, typed); err == nil {
			return at
		}
	}
	return time.Time{}
}

// mailboxScopeSince bounds the records a woken Team may treat as current.
// Anything produced before the mail arrived answers an earlier question, and a
// Team that reads it as "already confirmed" skips the work it was just given.
func mailboxScopeSince(messages []types.MailboxMessage) time.Time {
	var earliest time.Time
	for _, msg := range messages {
		if msg.CreatedAt.IsZero() {
			continue
		}
		if earliest.IsZero() || msg.CreatedAt.Before(earliest) {
			earliest = msg.CreatedAt
		}
	}
	return earliest
}

// maxDeliveryChars caps a single user-facing delivery. A Team's reply is
// written for its activator, and the activator may be another Team that wants
// the whole structured document; the user only needs a readable summary, and a
// message long enough to swamp the transcript is worse than a truncation note.
const maxDeliveryChars = 4000

// userDeliveries lists the results drained in this turn that belong to the
// user. Each drained message is delivered exactly once (the mailbox claims
// it), so no extra bookkeeping is needed to avoid repeats.
//
// Only results addressed to the Flow entry Team are user-facing. The entry
// Team is the one the user talks to; a result addressed to an intermediate
// Team is an internal hand-over that the receiving Team still has to turn into
// an answer. Relaying those verbatim is how s16 showed the user 11832
// characters of `@decision status: needs_input` protocol instead of a
// conclusion.
//
// The engine does not look at the coordinating reply here: whether the model
// said "done" or "still running" does not change that the result exists and
// belongs to the user.
func userDeliveries(messages []types.MailboxMessage, entryTeamID string) []types.UserDelivery {
	deliveries := make([]types.UserDelivery, 0, len(messages))
	for _, msg := range messages {
		if msg.Kind == mailboxKindWork {
			continue
		}
		if entryTeamID != "" && msg.To.TeamID != "" && msg.To.TeamID != entryTeamID {
			continue
		}
		// 只交付真正有内容的结果；占位文案（如 "raw result unavailable"）
		// 对模型是提示，对用户是噪音。
		text := ""
		if reply, ok := mailboxResultReply(msg.Payload); ok {
			text = strings.TrimSpace(reply)
		}
		if text == "" {
			text = strings.TrimSpace(mailboxResultSummary(msg.Payload))
		}
		if text == "" {
			continue
		}
		deliveries = append(deliveries, types.UserDelivery{
			MessageID: msg.MessageID,
			TeamID:    mailboxSender(msg),
			Text:      truncateDelivery(text, maxDeliveryChars),
		})
	}
	return deliveries
}

// truncateDelivery keeps a delivery readable without dropping the fact that it
// was cut. Truncation is a length decision; it never interprets the text.
func truncateDelivery(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "\n\n…（结果过长，已截断；完整内容见会话记录）"
}

// taskInputFromMessages returns the task the woken Team is working on.
//
// A dispatched message carries the original user input next to its payload.
// Without it the Team sees only the inbox block and whatever its own history
// remembers — enough to answer a question nobody asked, which is how a Team
// asked about one repository replied with another's branch. The newest message
// wins: it is the one the wake-up is for.
func taskInputFromMessages(messages []types.MailboxMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		input, ok := messages[i].Payload["input"].(string)
		if ok && strings.TrimSpace(input) != "" {
			return input
		}
	}
	return ""
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
//
// A Team that is already executing is skipped: its round was launched by the
// background dispatcher, which owns the message it is working on. Waking that
// Team here would run a second round for it, and the two would race over the
// same message.
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
		if r.teamBusy(sessionID, msg.To.TeamID) {
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
// mailboxBlocks turns the drained inbox into one block per message, carrying
// the sender so the renderer can emit each as its own protocol-level message.
//
// The block Kind separates results from dispatch instructions: a result and the
// record it produced describe the same thing, and the renderer keeps only one
// of them (the record wins). Work messages carry no record, so they must not be
// folded away.
func mailboxBlocks(messages []types.MailboxMessage) []types.ContextBlock {
	blocks := make([]types.ContextBlock, 0, len(messages))
	for _, msg := range messages {
		body := strings.TrimSpace(mailboxBodyText(msg))
		if body == "" {
			continue
		}
		kind := "mailbox_result"
		if msg.Kind == mailboxKindWork {
			kind = "mailbox"
		}
		blocks = append(blocks, types.ContextBlock{
			Kind: kind, Text: body, Source: mailboxSender(msg),
			Stability: "dynamic", Priority: 85,
		})
	}
	return blocks
}

// mailboxBodyText is the part of a message worth showing the model, without the
// "## Message mb_x from y" header: the sender now travels in the protocol, so
// repeating it in the text would say the same thing twice.
func mailboxBodyText(msg types.MailboxMessage) string {
	if reply, ok := mailboxResultReply(msg.Payload); ok && reply != "" {
		return reply
	}
	if summary := mailboxResultSummary(msg.Payload); summary != "" {
		return summary
	}
	if msg.Kind == mailboxKindWork {
		// A work message is the task this Team was handed. Showing its text is
		// what tells the Team what to do; the previous "dispatched to ..." line
		// only reported routing.
		if input, ok := msg.Payload["input"].(string); ok && strings.TrimSpace(input) != "" {
			return strings.TrimSpace(input)
		}
		target := msg.To.TeamID
		if target == "" {
			target = "the dispatched Team"
		}
		return fmt.Sprintf("dispatched to %s; the Team has not reported back yet", target)
	}
	return "(no rendered reply; raw result unavailable)"
}

// mailboxSender resolves the flow key of the Team that sent a message. From is
// normally empty (enqueueMessage never populated it), so the payload is the
// only reliable source: work messages carry caller_team, results carry
// team_result.Turn.TeamID.
func mailboxSender(msg types.MailboxMessage) string {
	if from := strings.TrimSpace(msg.From.TeamID); from != "" {
		return from
	}
	if caller, ok := msg.Payload["caller_team"].(string); ok && strings.TrimSpace(caller) != "" {
		return strings.TrimSpace(caller)
	}
	return mailboxResultTeam(msg.Payload)
}

func mailboxResultTeam(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	raw, ok := payload["team_result"]
	if !ok {
		container, isMap := payload["result"].(map[string]any)
		if !isMap {
			return ""
		}
		raw, ok = container["team_result"]
		if !ok {
			return ""
		}
	}
	var result types.TeamTurnResult
	switch v := raw.(type) {
	case types.TeamTurnResult:
		result = v
	case map[string]any:
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return ""
		}
	default:
		return ""
	}
	return strings.TrimSpace(result.Turn.TeamID)
}

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
		_, _ = fmt.Fprintf(&b, "## Message %s", msg.MessageID)
		if msg.From.TeamID != "" {
			_, _ = fmt.Fprintf(&b, " from %s", msg.From.TeamID)
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
		// A work message is a dispatch instruction, not a result: it is
		// still waiting in the target Team's inbox. Reporting it as "result
		// unavailable" told the waking Agent that its own dispatch had
		// failed, which is the opposite of what the message means.
		if msg.Kind == mailboxKindWork {
			target := msg.To.TeamID
			if target == "" {
				target = "the dispatched Team"
			}
			_, _ = fmt.Fprintf(&b, "dispatched to %s; the Team has not reported back yet\n", target)
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
//
// deliverMessage stores the produced output under "result", so a delivered
// message carries it at payload.result.team_result. Records written before
// that wrapper existed (and the folded in-memory form some tests build) put
// team_result at the top level; both shapes are accepted.
func mailboxResultReply(payload map[string]any) (string, bool) {
	raw, ok := payload["team_result"]
	if !ok {
		container, isMap := payload["result"].(map[string]any)
		if !isMap {
			return "", false
		}
		raw, ok = container["team_result"]
		if !ok {
			return "", false
		}
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
