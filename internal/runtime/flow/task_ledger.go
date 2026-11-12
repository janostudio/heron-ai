package flow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// taskLedgerLimit caps the rows one ledger shows. Beyond this the caller is told
// how many were left out rather than spending context on all of them.
const taskLedgerLimit = 12

// taskLedgerGoalRunes is how much of the dispatch text a row shows. Enough to
// recognise the task, short enough that a dozen rows cost little.
const taskLedgerGoalRunes = 80

// taskLedgerConclusionRunes is how much of a finished task's answer a row
// shows. It exists so the caller can report "what came back" without opening
// the task record; the full text stays available through Collect.
const taskLedgerConclusionRunes = 80

// TaskLedger renders the tasks one caller instance still has to care about.
//
// The problem it solves is not "too many results in context". It is that
// compaction throws away the conversation that dispatched a task, so when the
// task reports back the caller no longer knows what it was, how many siblings are
// still running, or whether it already summarised this one. The task records hold
// all of it; this puts them where the model can see them.
//
// Only open work appears: still running, or finished but not yet summarised. A
// summarised task drops out, so the ledger's length tracks outstanding work
// rather than history.
//
// It reads the durable task store alone. A Team instance can serve several
// dispatches over its life, so "what did I dispatch and what do I still owe" is a
// property of the tasks, not of the instance.
func (r *Runtime) TaskLedger(ctx context.Context, sessionID, callerSessionID string) string {
	if r == nil || r.tasks == nil {
		return ""
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(callerSessionID) == "" {
		return ""
	}
	tasks, err := r.tasks.List(ctx)
	if err != nil {
		return ""
	}

	type row struct {
		team       string
		state      string
		age        string
		goal       string
		conclusion string
		handle     string
		pending    bool
	}
	rows := make([]row, 0, len(tasks))
	for _, task := range tasks {
		if task.ToolName != types.ToolNameTeamRun {
			continue
		}
		if task.FlowSessionID != "" && task.FlowSessionID != sessionID {
			continue
		}
		if task.Acknowledged {
			continue
		}
		if !dispatchedBy(task, callerSessionID) {
			continue
		}
		team, _ := task.Arguments["team_id"].(string)
		goal, _ := task.Arguments["item"].(string)
		if strings.TrimSpace(goal) == "" {
			goal, _ = task.Arguments["task_key"].(string)
		}
		// The answer travels with the row. Without it "how did that task go?"
		// is only answerable by reading the task files, which the model then
		// does — one round trip per file, per task.
		conclusion := ""
		if task.Result != nil {
			conclusion = truncateLedgerText(firstLine(task.Result.Content), taskLedgerConclusionRunes)
		}
		rows = append(rows, row{
			team:       team,
			state:      ledgerStateOf(task.Status),
			age:        ledgerAgeOf(task.CreatedAt),
			goal:       truncateLedgerText(firstLine(goal), taskLedgerGoalRunes),
			conclusion: conclusion,
			handle:     task.ID,
			pending:    task.Status != types.ToolTaskQueued && task.Status != types.ToolTaskRunning,
		})
	}
	if len(rows) == 0 {
		return ""
	}
	// Finished work first: it is the only part that needs the caller to act.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].pending != rows[j].pending {
			return rows[i].pending
		}
		return rows[i].handle < rows[j].handle
	})

	var b strings.Builder
	b.WriteString("[任务清单] 你派出、还没收尾的任务（已完成待总结的排在前面）")
	for index, item := range rows {
		if index >= taskLedgerLimit {
			fmt.Fprintf(&b, "\n- 另有 %d 条未列出", len(rows)-taskLedgerLimit)
			break
		}
		fmt.Fprintf(&b, "\n- %s  %s  %s  「%s」",
			item.team, item.state, item.age, item.goal)
		if item.conclusion != "" {
			fmt.Fprintf(&b, "  结果：%s", item.conclusion)
		}
		fmt.Fprintf(&b, "  handle=%s", item.handle)
	}
	b.WriteString("\n用 Collect(handles=[handle]) 读取完整结果；总结过的任务会自动移出本表。")
	return b.String()
}

// dispatchedBy reports whether one caller instance owns this task. The dispatch
// records it under dispatched_by.team_session_id; a task without it was not a
// Team dispatch and does not belong in a caller's ledger.
func dispatchedBy(task types.ToolTask, callerSessionID string) bool {
	ref, ok := task.Arguments["dispatched_by"].(map[string]any)
	if !ok {
		return false
	}
	owner, _ := ref["team_session_id"].(string)
	return strings.TrimSpace(owner) == strings.TrimSpace(callerSessionID)
}

// acknowledgeTask marks one finished dispatch as summarised, so it leaves the
// caller's ledger. The runtime calls this after the summary turn succeeds, not
// the model: the model cannot be relied on to close its own loop, and a task that
// stays listed is a task that gets summarised twice.
func (r *Runtime) acknowledgeTask(ctx context.Context, taskID string) error {
	if r == nil || r.tasks == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	task, err := r.tasks.Load(ctx, taskID)
	if err != nil || task == nil {
		return nil
	}
	if task.Acknowledged {
		return nil
	}
	task.Acknowledged = true
	task.UpdatedAt = time.Now().UTC()
	return r.tasks.Save(ctx, *task)
}

// ledgerStateOf names the state in the caller's terms: whether it is still
// waiting on this task, or owes a summary for it.
func ledgerStateOf(status types.ToolTaskStatus) string {
	switch status {
	case types.ToolTaskQueued:
		return "queued   "
	case types.ToolTaskRunning:
		return "running  "
	case types.ToolTaskFailed:
		return "failed   "
	case types.ToolTaskCancelled:
		return "cancelled"
	default:
		return "done     "
	}
}

func ledgerAgeOf(started time.Time) string {
	if started.IsZero() {
		return "-"
	}
	age := time.Since(started)
	switch {
	case age < time.Minute:
		return "<1m"
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(age.Hours()))
	}
}

// firstLine is the opening line of a dispatch text: what the task is about,
// without the whole brief.
func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	return strings.TrimSpace(text)
}

func truncateLedgerText(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
