package view

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TUI debug logging is off by default and enabled with HERON_TUI_DEBUG=1.
//
// The TUI renders on the alternate screen, so anything printed there is lost on
// exit; when a key or a piece of state misbehaves there is otherwise no way to
// see what the program actually received (this is what stalled
// ISSUE-tui-team-duplicate-and-history-no-log: the history code looked correct
// but did not fire in a real terminal). Logs go to a file, one line per event,
// so the AltScreen is never disturbed.
//
// Path: $HERON_TUI_DEBUG_FILE, else .agents/data/logs/tui-debug.log.
var (
	tuiDebugOnce sync.Once
	tuiDebugFile *os.File
	tuiDebugOn   bool
)

func tuiDebugEnabled() bool {
	tuiDebugOnce.Do(func() {
		if os.Getenv("HERON_TUI_DEBUG") == "" {
			return
		}
		path := os.Getenv("HERON_TUI_DEBUG_FILE")
		if path == "" {
			path = filepath.Join(".agents", "data", "logs", "tui-debug.log")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		tuiDebugFile = f
		tuiDebugOn = true
	})
	return tuiDebugOn
}

// tuiLogf appends one timestamped line to the debug log when enabled. It is a
// no-op otherwise, so callers can leave calls in hot paths.
func tuiLogf(format string, args ...any) {
	if !tuiDebugEnabled() {
		return
	}
	line := fmt.Sprintf("%s [tui] %s\n",
		time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
	_, _ = tuiDebugFile.WriteString(line)
}

// resetTUIDebug clears the once-guard and open file. Test-only helper so a test
// can point the logger at its own path regardless of test order.
func resetTUIDebug() {
	if tuiDebugFile != nil {
		_ = tuiDebugFile.Close()
	}
	tuiDebugOnce = sync.Once{}
	tuiDebugFile = nil
	tuiDebugOn = false
}
