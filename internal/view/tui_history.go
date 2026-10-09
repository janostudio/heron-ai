package view

import (
	"os"
	"path/filepath"
	"strings"
)

// Input history is persisted so it survives a TUI restart. Without this, ↑/↓
// only reach the entries typed in the current process: press Ctrl+C and the
// history is gone (the "history does not work" report — the code was correct,
// the history simply was not saved).
//
// The file is per workspace (the process cwd), like a shell's history file, not
// per session: the user wants the last things they typed, regardless of which
// session those happened in.
const (
	tuiHistoryRelPath = ".agents/data/tui-history"
	tuiHistoryMax     = 500
)

// historyPath is the file backing input history.
func historyPath() string {
	if p := os.Getenv("HERON_TUI_HISTORY_FILE"); p != "" {
		return p
	}
	return tuiHistoryRelPath
}

// loadHistory reads persisted input history, oldest first. A missing file is
// not an error (first run); the result is never nil so callers can treat it as
// an empty history.
func loadHistory() []string {
	out := []string{}
	data, err := os.ReadFile(historyPath())
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// appendHistory adds one entry to the in-memory history and persists it.
//
// Newlines are flattened so one logical input stays one line (a multi-line
// entry recalled as several history entries would be wrong). A repeat of the
// most recent entry is not stored twice, matching shell behaviour.
func appendHistory(history []string, input string) []string {
	entry := strings.ReplaceAll(strings.TrimSpace(input), "\n", " ")
	if entry == "" {
		return history
	}
	if n := len(history); n > 0 && history[n-1] == entry {
		return history
	}
	history = append(history, entry)
	if len(history) > tuiHistoryMax {
		history = history[len(history)-tuiHistoryMax:]
	}
	persistHistory(history)
	return history
}

// persistHistory writes the whole history back. Best-effort: a failed write
// must not break the session.
func persistHistory(history []string) {
	path := historyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(strings.Join(history, "\n")+"\n"), 0o644)
}
