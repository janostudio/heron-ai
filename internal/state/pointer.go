package state

import (
	"path"
	"strings"
)

// PointerText renders the fixed pointer block that replaces snapshot injection.
//
// The state is a memory, and a memory replayed into every prompt is read as the
// current task's finding. That is not hypothetical: with the snapshot
// injected, a previous turn's verdict came back verbatim as the answer to a
// different question (s13, genie-agent→cls), and both teams that carry
// cross-turn work had to switch State off to stop it.
//
// The memory-tool pattern fixes the cause instead of the symptom: keep only a
// stable pointer in the prompt, and let the agent read the store on demand.
// The pointer is byte-stable for a given agent, so callers can mark it stable
// and keep it in the cached prefix. It names the directory and the rules for
// reading it; it never carries an entry.
func PointerText(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return ""
	}
	return `## Persistent State
Your cross-session state is a directory, not part of this prompt: ` +
		path.Join(".agents", "data", "agents", agentID, "state") + `/
Call the State tool with action="list" (no field) to read the index: which
blocks exist, how many entries each has, whether anything is in_progress, and
how old each block is. Load one block (field=<name>) only when you need its
entries.

- State is a memory, not the result of the current task. Verify before reusing it.
- Judge a block by its updated_at and by the goal: a block written for another
  task is stale, and must not be quoted as this task's outcome.
- When the task changes, overwrite the goal (action="add", field="goal") and
  clear the blocks that no longer apply, instead of layering a new task on the
  old one.
- next_steps carries the todo lifecycle: pending -> in_progress -> completed.
  Only one entry may be in_progress at a time. Remove entries you no longer need.
- Never write a turn's final answer here: the session already carries it.`
}
