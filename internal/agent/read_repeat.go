package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// defaultMaxRepeatedReads is how many times one read-only call may come back
// with the same arguments before the turn is treated as stuck. It only counts
// repeats with no workspace write in between, so an edit-and-verify loop is not
// affected: the write resets the count.
const defaultMaxRepeatedReads = 3

// readOnlyTools are the calls that depend on their arguments and the workspace
// alone. Repeating one with identical arguments returns the same bytes, which
// is what makes the second copy redundant rather than new information.
//
// Bash is deliberately absent: the same command string can produce different
// output, and its arguments are not a path the runtime can key on.
var readOnlyTools = map[string]bool{
	"Read":     true,
	"Grep":     true,
	"Glob":     true,
	"CodeNav":  true,
	"TodoRead": true,
}

// readCallSignature identifies a read-only call by name and arguments.
func readCallSignature(call types.ToolCall) string {
	if !readOnlyTools[call.Name] {
		return ""
	}
	keys := make([]string, 0, len(call.Arguments))
	for key := range call.Arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(call.Name)
	for _, key := range keys {
		b.WriteString("\x00")
		b.WriteString(key)
		b.WriteString("=")
		fmt.Fprint(&b, call.Arguments[key])
	}
	return b.String()
}

// repeatedCallPlaceholder replaces an earlier result whose content the latest
// copy repeats. It names what was read so the model still knows it looked, and
// points at where the current answer is.
func repeatedCallPlaceholder(call types.ToolCall) string {
	if target := readCallTarget(call); target != "" {
		return fmt.Sprintf("[%s of %s repeated later in this conversation; that later result is the current one]",
			call.Name, target)
	}
	return fmt.Sprintf("[%s repeated later in this conversation; that later result is the current one]", call.Name)
}

// readCallTarget names what a read-only call looked at, for the placeholder.
func readCallTarget(call types.ToolCall) string {
	for _, key := range []string{"file", "path", "pattern"} {
		if value, ok := call.Arguments[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// repeatedReadCap resolves the configured cap, falling back to a conservative
// built-in one so the guard is on by default rather than never firing.
func repeatedReadCap(loop types.LoopConfig) int {
	if loop.MaxRepeatedReads > 0 {
		return loop.MaxRepeatedReads
	}
	return defaultMaxRepeatedReads
}
