//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// event is one line of a session JSONL file, kept as raw bytes plus the few
// fields a case judges. Parsing into a struct and re-printing loses fields —
// two earlier investigations produced false negatives from exactly that, so
// Raw is preserved.
type event struct {
	File string
	Line int
	Seq  int64
	Type string
	Raw  map[string]any
	Text string
}

// sessionID is the FlowSession id the event belongs to ("" for global events).
func (e event) sessionID() string {
	value, _ := e.Raw["flow_session_id"].(string)
	return value
}

// readSession returns the events of one session, ordered by seq.
func readSession(t *testing.T, root, sessionID string) []event {
	t.Helper()
	dir := filepath.Join(root, ".agents", "data", "sessions", sessionID)
	events := readEventsIn(t, dir)
	if len(events) == 0 {
		t.Fatalf("no events for session %s (dir=%s)", sessionID, dir)
	}
	return events
}

// readEventsIn reads every *.jsonl in a session directory.
func readEventsIn(t *testing.T, dir string) []event {
	t.Helper()
	var events []event
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		file, openErr := os.Open(filepath.Join(dir, entry.Name()))
		if openErr != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		line := 0
		for scanner.Scan() {
			line++
			text := scanner.Text()
			if strings.TrimSpace(text) == "" {
				continue
			}
			var raw map[string]any
			if json.Unmarshal([]byte(text), &raw) != nil {
				continue
			}
			ev := event{File: entry.Name(), Line: line, Raw: raw, Text: text}
			if value, ok := raw["type"].(string); ok {
				ev.Type = value
			}
			switch seq := raw["seq"].(type) {
			case float64:
				ev.Seq = int64(seq)
			case string:
				ev.Seq, _ = strconv.ParseInt(seq, 10, 64)
			}
			events = append(events, ev)
		}
		file.Close()
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	return events
}

// typesOf lists the event types in order; used by assertions that care about
// relative order rather than exact adjacency.
func typesOf(events []event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Type)
	}
	return out
}

func hasEvent(events []event, eventType string) bool {
	return countEvents(events, eventType) > 0
}

func countEvents(events []event, eventType string) int {
	count := 0
	for _, ev := range events {
		if ev.Type == eventType {
			count++
		}
	}
	return count
}

// firstIndex returns the position of the first event of a type, or -1.
func firstIndex(events []event, eventType string) int {
	for index, ev := range events {
		if ev.Type == eventType {
			return index
		}
	}
	return -1
}

// waitForEvent polls the session log until predicate holds. Async behaviour is
// judged by events, never by sleeping a fixed duration: a sleep either wastes
// time or makes the assertion flaky, and both hide the real defect.
func waitForEvent(t *testing.T, root, sessionID string, timeout time.Duration, predicate func([]event) bool) []event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []event
	for time.Now().Before(deadline) {
		last = readEventsIn(t, filepath.Join(root, ".agents", "data", "sessions", sessionID))
		if len(last) > 0 && predicate(last) {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for events in %s; last types: %v", timeout, sessionID, tail(typesOf(last), 12))
	return last
}

func tail(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[len(values)-limit:]
}

// turnTerminated reports whether the turn reached a state a client can act on.
func turnTerminated(events []event) bool {
	for _, ev := range events {
		switch ev.Type {
		case "session.turn.completed", "session.turn.idle",
			"session.turn.waiting_tool", "session.turn.waiting_approval":
			return true
		}
	}
	return false
}

// finalTurnStatus returns the terminal status seen for the turn, if any.
func finalTurnStatus(events []event) string {
	for index := len(events) - 1; index >= 0; index-- {
		ev := events[index]
		switch ev.Type {
		case "session.turn.completed", "session.turn.idle",
			"session.turn.waiting_tool", "session.turn.waiting_approval":
			if payload, ok := ev.Raw["payload"].(map[string]any); ok {
				if turn, ok := payload["turn"].(map[string]any); ok {
					if status, ok := turn["status"].(string); ok && status != "" {
						return status
					}
				}
			}
			return ev.Type
		}
	}
	return ""
}
