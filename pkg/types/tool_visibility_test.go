package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInjectedBuiltinDefaultsToBuiltin pins the compatibility rule: a definition
// that never heard of tools.direct keeps injecting everything it declares.
func TestInjectedBuiltinDefaultsToBuiltin(t *testing.T) {
	config := ToolConfig{Builtin: []string{"Bash", "Read"}}
	require.Equal(t, []string{"Bash", "Read"}, config.InjectedBuiltin())
	require.Equal(t, []string{"Bash", "Read"}, config.SandboxBuiltin())
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
