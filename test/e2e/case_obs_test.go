//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// logLine is one process-log record, kept raw alongside the decoded fields a
// case judges.
type logLine struct {
	Level         string
	Msg           string
	FlowSessionID string
	CallID        string
	TurnID        string
	TaskID        string
	Raw           string
}

// readProcessLog returns every JSON line the engine wrote for this run.
func readProcessLog(t *testing.T, root string) []logLine {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".agents", "data", "logs"))
	if err != nil {
		return nil
	}
	var lines []logLine
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".log" {
			continue
		}
		file, openErr := os.Open(filepath.Join(root, ".agents", "data", "logs", entry.Name()))
		if openErr != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			text := scanner.Text()
			if len(text) == 0 || text[0] != '{' {
				continue
			}
			var raw map[string]any
			if json.Unmarshal([]byte(text), &raw) != nil {
				continue
			}
			line := logLine{Raw: text}
			line.Level, _ = raw["level"].(string)
			line.Msg, _ = raw["msg"].(string)
			line.FlowSessionID, _ = raw["flow_session_id"].(string)
			if line.FlowSessionID == "" {
				line.FlowSessionID, _ = raw["session_id"].(string)
			}
			line.CallID, _ = raw["call_id"].(string)
			line.TaskID, _ = raw["task_id"].(string)
			if turn, ok := raw["flow_turn_id"].(string); ok {
				line.TurnID = turn
			}
			lines = append(lines, line)
		}
		file.Close()
	}
	return lines
}

// locatable counts the log lines a reader could use to jump to one session.
func locatable(lines []logLine) int {
	count := 0
	for _, line := range lines {
		if line.FlowSessionID != "" {
			count++
		}
	}
	return count
}

// TestObs1ExecutionLogsCarrySessionIdentity covers I-OBS-1 at debug level: the
// engine can write execution logs that name the session they belong to. This is
// the floor — a log line without an identity cannot be joined to the session
// data, so a reader falls back to matching timestamps by eye.
func TestObs1ExecutionLogsCarrySessionIdentity(t *testing.T) {
	judge(t, "I-OBS-1", "observability")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-OBS-1", logLevel: "debug"})
	e.Mock.script(textStep("答案"))

	result := e.runTurn(map[string]any{"input": "问题"})
	lines := readProcessLog(t, e.Root)

	if len(lines) == 0 {
		t.Fatalf("debug level wrote no process log for session %s", result.Session.ID)
	}
	if n := locatable(lines); n == 0 {
		t.Fatalf("debug level wrote %d log lines, none carrying an identity (msgs=%v)", len(lines), messagesOf(lines))
	}

	writeTrace(t, e, httpTrace{Method: "POST", Path: "/api/run", Status: 200}, result.Session.ID, map[string]any{
		"log_lines": len(lines),
		"locatable": locatable(lines),
		"messages":  messagesOf(lines),
	})
}

// TestObs2LocatingDoesNotRequireDebug covers I-OBS-2: a deployment runs at info.
// If the lines that identify a session only exist at debug level, then the
// engine is diagnosable in development and blind in production — and every
// "why did this turn hang" question goes back to guessing.
func TestObs2LocatingDoesNotRequireDebug(t *testing.T) {
	judge(t, "I-OBS-2", "observability")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-OBS-2"})
	e.Mock.script(textStep("答案"))

	result := e.runTurn(map[string]any{"input": "问题"})
	lines := readProcessLog(t, e.Root)

	writeTrace(t, e, httpTrace{Method: "POST", Path: "/api/run", Status: 200}, result.Session.ID, map[string]any{
		"log_lines": len(lines),
		"locatable": locatable(lines),
		"messages":  messagesOf(lines),
	})

	if len(lines) == 0 {
		t.Fatalf("a completed turn wrote no process log at all; session %s is only findable by reading its jsonl", result.Session.ID)
	}
	if n := locatable(lines); n == 0 {
		t.Fatalf("default level wrote %d log lines and none names the session (msgs=%v); locating it requires debug", len(lines), messagesOf(lines))
	}
}

func messagesOf(lines []logLine) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.Msg)
	}
	return out
}
