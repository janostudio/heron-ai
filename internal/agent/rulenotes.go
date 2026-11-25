package agent

import (
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// ruleNote is one conditional rule that this turn has triggered.
type ruleNote struct {
	ID   string
	Path string
	Kind string
	Body string
}

// ruleNoteSet holds the conditional rules an Agent could apply and the ones this
// turn has already triggered.
//
// It lives for one turn and is never written into the conversation: a note is a
// statement about the work happening now, so replaying it into the next turn (or
// into the transcript) would tell the model it is writing documentation long
// after it stopped. Compaction cannot lose it for the same reason — it is not
// history to compact.
type ruleNoteSet struct {
	rules []types.ConditionalRule
	notes []ruleNote
	seen  map[string]struct{}
}

func newRuleNoteSet(rules []types.ConditionalRule) *ruleNoteSet {
	return &ruleNoteSet{rules: rules, seen: make(map[string]struct{}, len(rules))}
}

// empty reports whether there is anything to inject. A turn with no conditional
// rules never pays for this beyond the call.
func (s *ruleNoteSet) empty() bool { return s == nil || len(s.notes) == 0 }

// observe matches the paths this tool batch touched and returns only the notes
// it added, so the caller can emit one event per new rule instead of one per
// round.
func (s *ruleNoteSet) observe(targets []pathTarget) []ruleNote {
	if s == nil || len(s.rules) == 0 || len(targets) == 0 {
		return nil
	}
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.Path)
	}

	var added []ruleNote
	for _, rule := range s.rules {
		if _, done := s.seen[rule.ID]; done {
			continue
		}
		matched := firstMatchPath(rule.Paths, paths)
		if matched == "" {
			continue
		}
		kind := "read"
		for _, target := range targets {
			if normalizeRulePath(target.Path) == matched {
				kind = target.Kind
				break
			}
		}
		s.seen[rule.ID] = struct{}{}
		note := ruleNote{ID: rule.ID, Path: matched, Kind: kind, Body: rule.Body}
		s.notes = append(s.notes, note)
		added = append(added, note)
	}
	return added
}

// noteMessage renders every note triggered so far as one system message.
//
// One message, not one per note: the request already contains the earlier
// rounds, so a per-trigger message would repeat the same body on every round.
// This message is rebuilt each request from the current set, and the system
// prefix above it does not change, which is what keeps prompt caching intact.
func (s *ruleNoteSet) noteMessage() (types.Message, bool) {
	if s.empty() {
		return types.Message{}, false
	}
	var builder strings.Builder
	builder.WriteString("[rule-notes] 本轮触及的路径适用以下规则，立即生效；这不是对话历史，也不需要回应。\n")
	for _, note := range s.notes {
		fmt.Fprintf(&builder, "\n[rule:%s trigger=%s kind=%s]\n", note.ID, note.Path, note.Kind)
		builder.WriteString(strings.TrimSpace(note.Body))
		builder.WriteString("\n")
	}
	return types.Message{Role: "system", Content: builder.String()}, true
}

// seedFromOps marks the rules already triggered by an earlier attempt of this
// same turn, so a resumed turn does not inject them a second time.
//
// It replays the identity, not the text: the checkpoint stores the operations
// the turn performed, and those are exactly what triggered the notes before the
// pause. The body is taken from the current definitions, so a rule edited
// between pause and resume arrives in its current form.
func (s *ruleNoteSet) seedFromOps(ops []types.WorkspaceOperation) {
	if s == nil || len(ops) == 0 {
		return
	}
	targets := make([]pathTarget, 0, len(ops))
	for _, op := range ops {
		if op.Path == "" {
			continue
		}
		kind := "read"
		switch op.Kind {
		case "write":
			kind = "write"
		case "search":
			kind = "search"
		}
		targets = append(targets, pathTarget{Path: op.Path, Kind: kind})
	}
	s.observe(targets)
}
