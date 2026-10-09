// Package sessionlist reads the on-disk session tree under
// .agents/data/sessions/ into a flat, display-ready list. It powers the
// `heron --list` command: enumerate FlowSessions, show a one-line summary of
// each, and let the user pick one to resume.
//
// It deliberately does not go through storage.Replay: that reader returns an
// error on a malformed line, and a single corrupt line must not hide a whole
// session from the list. Here bad lines are skipped and the rest of the
// session is still summarized.
package sessionlist

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// SessionsDir is the workspace-relative directory holding one subdirectory per
// FlowSession.
const SessionsDir = ".agents/data/sessions"

// TitleMaxRunes bounds the display title taken from the first user input.
const TitleMaxRunes = 60

// Session is one row of the list.
type Session struct {
	ID        string
	Title     string
	Turns     int
	Tokens    int64
	UpdatedAt time.Time
	// Active is true when the session's last turn has not reached a resting
	// state, i.e. a FlowTurn is still executing. It is derived from the last
	// flow_turn.* event rather than session.status: flow.jsonl leaves the
	// last flow_session.updated at "running" and never rewrites it to
	// waiting_input, so session.status alone would mark every session active.
	Active bool
}

// List enumerates the sessions under dir. A missing directory is not an error:
// it yields an empty list so a fresh workspace reports "no sessions" rather
// than failing. Individual unreadable or malformed sessions are skipped.
func List(fs storage.FileStore, dir string) ([]Session, error) {
	top, err := fs.List(dir)
	if err != nil {
		return nil, err
	}

	var sessions []Session
	for _, name := range top {
		sessionDir := dir + "/" + name
		// List returns files and directories alike; a session is a directory,
		// which is exactly a name that itself lists successfully with at least
		// one child. A plain file lists to an empty result, so require the
		// child marker explicitly.
		children, childErr := fs.List(sessionDir)
		if childErr != nil || len(children) == 0 {
			continue
		}
		session, ok := readSession(fs, name, sessionDir)
		if !ok {
			continue
		}
		sessions = append(sessions, session)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

// readSession summarizes one session directory. It reports ok=false only when
// the session yields nothing usable at all (no flow.jsonl).
func readSession(fs storage.FileStore, id, dir string) (Session, bool) {
	flowEvents, err := readEvents(fs, dir+"/flow.jsonl")
	if err != nil {
		return Session{}, false
	}

	session := Session{ID: id}
	var sessionUpdatedAt time.Time
	var lastTurnEvent string
	titleSet := false

	for _, event := range flowEvents {
		if !event.CreatedAt.IsZero() {
			session.UpdatedAt = event.CreatedAt
		}
		switch event.Type {
		case types.EventFlowSessionCreated, types.EventFlowSessionUpdated:
			// payload.session carries the session's own updated_at.
			var wrapper struct {
				Session types.FlowSession `json:"session"`
			}
			if decode(event.Payload, &wrapper) == nil && !wrapper.Session.UpdatedAt.IsZero() {
				sessionUpdatedAt = wrapper.Session.UpdatedAt
			}
		case types.EventFlowTurnStarted:
			session.Turns++
			if !titleSet {
				if input, _ := event.Payload["input"].(string); strings.TrimSpace(input) != "" {
					session.Title = truncateTitle(input)
					titleSet = true
				}
			}
			lastTurnEvent = event.Type
		case types.EventFlowTurnCompleted, types.EventFlowTurnWaitingInput,
			types.EventFlowTurnWaitingTool, types.EventFlowTurnWaitingApproval:
			lastTurnEvent = event.Type
		}
	}

	// Prefer the session's own updated_at when it is newer than the last event
	// header (both track the same clock, but the session field is the contract).
	if sessionUpdatedAt.After(session.UpdatedAt) {
		session.UpdatedAt = sessionUpdatedAt
	}
	// A turn that only ever emitted flow_turn.started (or is parked on a tool /
	// approval gate) is still executing; completed / waiting_input are at rest.
	switch lastTurnEvent {
	case types.EventFlowTurnStarted, types.EventFlowTurnWaitingTool, types.EventFlowTurnWaitingApproval:
		session.Active = true
	}

	session.Tokens = sumTeamTokens(fs, dir+"/team.jsonl")
	return session, true
}

// sumTeamTokens totals the token usage recorded on agent_turn.completed events.
// The usage lives at payload.call_result.Usage (Go field name, PascalCase) with
// its leaves in snake_case — that mixed casing is the published contract, not a
// typo, so CallResult is decoded as-is rather than flattened by hand.
func sumTeamTokens(fs storage.FileStore, path string) int64 {
	events, err := readEvents(fs, path)
	if err != nil {
		return 0
	}
	var total int64
	for _, event := range events {
		if event.Type != types.EventAgentTurnCompleted {
			continue
		}
		var wrapper struct {
			CallResult types.CallResult `json:"call_result"`
		}
		if decode(event.Payload, &wrapper) != nil {
			continue
		}
		total += int64(wrapper.CallResult.Usage.TotalTokens)
	}
	return total
}

// event is the subset of the on-disk event envelope this package needs.
type event struct {
	Type      string         `json:"type"`
	CreatedAt time.Time      `json:"created_at"`
	Payload   map[string]any `json:"payload"`
}

// readEvents parses a jsonl file, skipping blank and malformed lines. A missing
// file returns an empty slice, not an error, so a session with no team.jsonl
// still lists.
func readEvents(fs storage.FileStore, path string) ([]event, error) {
	data, err := fs.Read(path)
	if err != nil {
		if err == storage.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}

	var events []event
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // a corrupt line must not hide the session
		}
		events = append(events, ev)
	}
	return events, nil
}

// decode round-trips a payload value into a typed struct via JSON, so the
// caller gets the same field names the file carries.
func decode(payload map[string]any, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func truncateTitle(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	runes := []rune(s)
	if len(runes) <= TitleMaxRunes {
		return s
	}
	return string(runes[:TitleMaxRunes]) + "…"
}
