package sessionlist

import (
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Turn is one user input and the replies it produced, reconstructed from a
// session's flow.jsonl for repopulating the TUI on resume.
type Turn struct {
	Input string
	// Messages are the replies produced by this input, one per call, carrying
	// their team/agent identity so a resumed transcript attributes each reply
	// the same way a live one does.
	Messages []TurnMessage
	// Usage is the token cost recorded for this input's team turns, summed
	// across the teams that ran.
	Usage types.TokenUsage
}

// TurnMessage is one call's reply inside a Turn.
type TurnMessage struct {
	TeamID  string
	CallID  string
	AgentID string
	Reply   string
	Ops     []Operation
}

// Operation is a workspace operation summarized for display.
type Operation struct {
	Kind     string
	Subject  string
	ExitCode int
	Failed   bool
}

// History reconstructs the user inputs of a session from its flow.jsonl, each
// paired with the replies it produced, so a resumed TUI can show both the
// conversation the model remembers and who answered.
//
// Malformed lines are skipped (same tolerance as List); a session with no
// usable turns returns an empty slice and no error.
func History(fs storage.FileStore, sessionDir string) ([]Turn, error) {
	events, err := readEvents(fs, sessionDir+"/flow.jsonl")
	if err != nil {
		return nil, err
	}

	var turns []Turn
	for _, event := range events {
		switch event.Type {
		case types.EventFlowTurnStarted:
			input, _ := event.Payload["input"].(string)
			turns = append(turns, Turn{Input: input})
		case types.EventTeamTurnCompleted:
			data := teamTurnData(event.Payload)
			// Every team turn belongs to the most recent user input; if none
			// exists yet (an activation turn the user did not type), start an
			// input-less turn to hold it.
			if len(turns) == 0 {
				turns = append(turns, Turn{})
			}
			cur := &turns[len(turns)-1]
			cur.Usage = addUsage(cur.Usage, data.Usage)
			cur.Messages = append(cur.Messages, callMessages(data)...)
		}
	}

	// Drop turns with nothing on either side.
	kept := turns[:0]
	for _, t := range turns {
		if strings.TrimSpace(t.Input) != "" || len(t.Messages) > 0 {
			kept = append(kept, t)
		}
	}
	return kept, nil
}

// callMessages projects a team_result's calls into display messages: one per
// call, sorted by call name (CallResults is a map, so an explicit order keeps
// the transcript stable across replays). A call that loses its identity still
// yields a message, since the reply itself is worth showing.
func callMessages(data teamTurnFields) []TurnMessage {
	if len(data.CallResults) == 0 {
		// No per-call breakdown: fall back to the team-level reply.
		if strings.TrimSpace(data.Reply) == "" {
			return nil
		}
		return []TurnMessage{{TeamID: data.Turn.TeamID, Reply: data.Reply}}
	}

	names := make([]string, 0, len(data.CallResults))
	for name := range data.CallResults {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]TurnMessage, 0, len(names))
	for _, name := range names {
		call := data.CallResults[name]
		if strings.TrimSpace(call.Reply) == "" && len(call.WorkspaceOps) == 0 {
			continue
		}
		out = append(out, TurnMessage{
			TeamID:  data.Turn.TeamID,
			CallID:  name,
			AgentID: call.AgentID,
			Reply:   call.Reply,
			Ops:     operations(call.WorkspaceOps),
		})
	}
	return out
}

// operations projects workspace operations into compact display entries.
func operations(ops []types.WorkspaceOperation) []Operation {
	if len(ops) == 0 {
		return nil
	}
	out := make([]Operation, 0, len(ops))
	for _, op := range ops {
		subject := strings.TrimSpace(op.Command)
		if subject == "" {
			subject = strings.TrimSpace(op.Path)
		}
		if subject == "" {
			subject = strings.TrimSpace(op.Summary)
		}
		out = append(out, Operation{
			Kind:     op.Kind,
			Subject:  subject,
			ExitCode: op.ExitCode,
			Failed:   op.ExitCode != 0,
		})
	}
	return out
}

// teamTurnFields decodes the container fields this package reads from a
// team_result payload. Containers use Go field names (Reply, Usage, …); Usage's
// own leaves stay snake_case — the published contract.
type teamTurnFields struct {
	Reply       string                    `json:"Reply"`
	Usage       types.TokenUsage          `json:"Usage"`
	Turn        teamTurnRef               `json:"Turn"`
	CallResults map[string]teamCallResult `json:"CallResults"`
}

// teamTurnRef carries the team identifier embedded in a team_result.Turn.
type teamTurnRef struct {
	TeamID string `json:"team_id"`
}

// teamCallResult is the subset of a CallResult this package reads.
type teamCallResult struct {
	AgentID      string                     `json:"AgentID"`
	Reply        string                     `json:"Reply"`
	WorkspaceOps []types.WorkspaceOperation `json:"WorkspaceOps"`
}

func teamTurnData(payload map[string]any) teamTurnFields {
	var wrapper struct {
		TeamResult teamTurnFields `json:"team_result"`
	}
	_ = decode(payload, &wrapper)
	return wrapper.TeamResult
}

// addUsage sums two token usages field by field.
func addUsage(a, b types.TokenUsage) types.TokenUsage {
	return types.TokenUsage{
		PromptTokens:             a.PromptTokens + b.PromptTokens,
		CompletionTokens:         a.CompletionTokens + b.CompletionTokens,
		ReasoningTokens:          a.ReasoningTokens + b.ReasoningTokens,
		TotalTokens:              a.TotalTokens + b.TotalTokens,
		PromptCacheHitTokens:     a.PromptCacheHitTokens + b.PromptCacheHitTokens,
		PromptCacheMissTokens:    a.PromptCacheMissTokens + b.PromptCacheMissTokens,
		CacheReadInputTokens:     a.CacheReadInputTokens + b.CacheReadInputTokens,
		CacheCreationInputTokens: a.CacheCreationInputTokens + b.CacheCreationInputTokens,
	}
}
