package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInjectedBuiltinDefaultsToBuiltin pins the compatibility rule: a definition
// without REPL injects everything it declares and has no sandbox at all.
func TestInjectedBuiltinDefaultsToBuiltin(t *testing.T) {
	config := ToolConfig{Builtin: []string{"Bash", "Read"}}
	require.Equal(t, []string{"Bash", "Read"}, config.InjectedBuiltin())
	require.Empty(t, config.SandboxBuiltin(), "no REPL, no sandbox")
	require.Equal(t, []string{"Bash", "Read"}, config.CallableBuiltin())
}

// TestDeclaringREPLSplitTheSurface is the one-line definition: list REPL and the
// non-code tools, and the code tools land in the sandbox on their own.
func TestDeclaringREPLSplitTheSurface(t *testing.T) {
	config := ToolConfig{Builtin: []string{"REPL", "Team", "TodoWrite"}}
	require.True(t, config.DeclaresREPL())
	require.Equal(t, []string{"REPL", "Team", "TodoWrite"}, config.InjectedBuiltin())
	require.Equal(t, []string{"Bash", "Read", "Write", "Grep", "Glob"}, config.SandboxBuiltin())
	require.Equal(t,
		[]string{"REPL", "Team", "TodoWrite", "Bash", "Read", "Write", "Grep", "Glob"},
		config.CallableBuiltin())
}

// TestDeclaringREPLDropsListedCodeTools keeps the two surfaces disjoint even when
// the author repeats a code tool next to REPL: it is implied, and the injected list
// must not grow it back.
func TestDeclaringREPLDropsListedCodeTools(t *testing.T) {
	config := ToolConfig{Builtin: []string{"Bash", "REPL", "Team"}}
	require.Equal(t, []string{"REPL", "Team"}, config.InjectedBuiltin())
	require.Equal(t, []string{"Bash", "Read", "Write", "Grep", "Glob"}, config.SandboxBuiltin())
	require.Equal(t,
		[]string{"Bash", "REPL", "Team", "Read", "Write", "Grep", "Glob"},
		config.CallableBuiltin(), "no name appears twice")
}

// TestDirectMovesToolsIntoTheSandbox is the whole point of the field: the injected
// list shrinks, the sandbox list grows by exactly the tools that left.
func TestDirectMovesToolsIntoTheSandbox(t *testing.T) {
	config := ToolConfig{
		Builtin: []string{"Bash", "Read", "Grep", "REPL", "Team"},
		Direct:  []string{"REPL", "Team"},
	}
	require.Equal(t, []string{"REPL", "Team"}, config.InjectedBuiltin())
	require.Equal(t, []string{"Bash", "Read", "Grep"}, config.SandboxBuiltin())
}

// TestSandboxKeepsDeclarationOrder guards the order of the catalog: the model reads
// it, and a stable order is what makes two runs comparable.
func TestSandboxKeepsDeclarationOrder(t *testing.T) {
	config := ToolConfig{
		Builtin: []string{"Read", "Bash", "REPL", "Grep"},
		Direct:  []string{"REPL"},
	}
	require.Equal(t, []string{"Read", "Bash", "Grep"}, config.SandboxBuiltin())
}
