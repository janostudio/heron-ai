package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// printUsage emits GNU-style double dashes. Go's flag package would print a
// single dash, and the stored name carries no dash at all, so the prefix is
// added by the custom Usage. Input parsing is unchanged (flag accepts both
// -name and --name), so this is display-only.
func TestPrintUsageUsesDoubleDash(t *testing.T) {
	fs := flag.NewFlagSet("heron", flag.ContinueOnError)
	fs.String("prompt", "", "Run one FlowTurn and exit")
	fs.String("session", "", "Resume an existing FlowSession")
	fs.Bool("list", false, "List sessions")
	fs.Int("max-rounds", 0, "Override max agent rounds")

	var buf bytes.Buffer
	fs.SetOutput(&buf)
	printUsage(fs)

	out := buf.String()
	require.Contains(t, out, "--prompt string")
	require.Contains(t, out, "--session string")
	require.Contains(t, out, "--list\n")
	require.Contains(t, out, "--max-rounds int")
	require.NotContains(t, out, "  -prompt", "help must not show single-dash flags")
	// A boolean flag takes no placeholder.
	assert.False(t, strings.Contains(out, "--list value"))
}
