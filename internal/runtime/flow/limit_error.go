package flow

import (
	"fmt"
	"strings"
)

// limitError is a tripped backstop, carrying the facts a reader needs as
// fields rather than only as prose.
//
// The prose form ("flow turn exceeded max team turns: 100 ...") is for humans
// reading a log. The fields are for the event stream: a page that wants to
// mark the turn, highlight the looping edges, or count how often a limit trips
// cannot do any of that from a string.
//
// Everything here is diagnostic. Nothing decides behaviour from it: the check
// that produced it already made the decision.
type limitError struct {
	// Name is the settings.json key that tripped, e.g. "max_team_turns".
	Name string
	// Value is the configured threshold; Observed is what the turn reached.
	Value    int
	Observed int
	// TopPairs are the most frequent activation edges, already rendered
	// ("owner→worktree ×47"), most-used first.
	TopPairs []string
	// Recent is the tail of the activation sequence.
	Recent []string
	// Detail is the human-facing hint appended to the message.
	Detail string
}

// Error renders the one-line form that lands in logs and in the event's error
// field. It stays a single line: the event payload is JSON, and embedded
// newlines make it harder to scan.
func (e *limitError) Error() string {
	parts := []string{fmt.Sprintf("%s: %d", e.Name, e.Value)}
	if e.Observed > 0 {
		parts = append(parts, fmt.Sprintf("observed %d", e.Observed))
	}
	if len(e.TopPairs) > 0 {
		parts = append(parts, "top: "+strings.Join(e.TopPairs, ", "))
	}
	if len(e.Recent) > 0 {
		parts = append(parts, "recent: "+strings.Join(e.Recent, " → "))
	}
	if e.Detail != "" {
		parts = append(parts, e.Detail)
	}
	return strings.Join(parts, "; ")
}

// Payload is the structured form written into the event, so a reader does not
// have to parse Error() back into fields.
func (e *limitError) Payload() map[string]any {
	out := map[string]any{
		"name":     e.Name,
		"value":    e.Value,
		"observed": e.Observed,
	}
	if len(e.TopPairs) > 0 {
		out["top_pairs"] = e.TopPairs
	}
	if len(e.Recent) > 0 {
		out["recent"] = e.Recent
	}
	return out
}
