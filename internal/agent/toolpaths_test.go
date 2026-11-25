package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestCallPathTargetsFromArguments covers the half that has no other source: a
// write that fails reports no operation, and a shell command never reports one.
// If the arguments are not read, those writes are invisible to directory
// contracts — which is exactly the case that matters for "do not write here".
func TestCallPathTargetsFromArguments(t *testing.T) {
	cases := []struct {
		name  string
		call  types.ToolCall
		paths []string
		kind  string
	}{
		{
			name:  "write names its target and its directory",
			call:  types.ToolCall{Name: "Write", Arguments: map[string]any{"file": "docs/self/x.md"}},
			paths: []string{"docs/self/x.md", "docs/self"},
			kind:  "write",
		},
		{
			name:  "edit reads the same keys as write",
			call:  types.ToolCall{Name: "Edit", Arguments: map[string]any{"file_path": "knowledge/patterns.md"}},
			paths: []string{"knowledge/patterns.md", "knowledge"},
			kind:  "write",
		},
		{
			name:  "read",
			call:  types.ToolCall{Name: "Read", Arguments: map[string]any{"file": "repos/example/main.go"}},
			paths: []string{"repos/example/main.go"},
			kind:  "read",
		},
		{
			name:  "grep with a path",
			call:  types.ToolCall{Name: "Grep", Arguments: map[string]any{"path": "repos/example"}},
			paths: []string{"repos/example"},
			kind:  "search",
		},
		{
			name:  "grep without a path searches everywhere",
			call:  types.ToolCall{Name: "Grep", Arguments: map[string]any{"pattern": "TODO"}},
			paths: []string{"."},
			kind:  "search",
		},
		{
			name:  "unknown tool contributes nothing",
			call:  types.ToolCall{Name: "Team", Arguments: map[string]any{"prompt": "x"}},
			paths: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targets := callPathTargets(tc.call)
			var got []string
			for _, target := range targets {
				got = append(got, target.Path)
				if tc.kind != "" {
					assert.Equal(t, tc.kind, target.Kind)
				}
			}
			if tc.paths == nil {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tc.paths, got)
		})
	}
}

// TestBashTargetsReadsOnlyWhatItCanKnow is the parser's contract: three shapes
// are understood, everything dynamic is dropped rather than guessed. A contract
// injected for a path the command may never touch teaches the model that rule
// notes are noise.
func TestBashTargetsReadsOnlyWhatItCanKnow(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"heredoc redirect", "cat > ../docs/self/x.md <<'EOF'\nbody\nEOF", []string{"../docs/self/x.md", "../docs/self"}},
		{"append redirect", "echo hi >> docs/INDEX.md", []string{"docs/INDEX.md", "docs"}},
		{"redirect without space", "echo hi >docs/x.md", []string{"docs/x.md", "docs"}},
		{"tee", "printf x | tee -a knowledge/log.md", []string{"knowledge/log.md", "knowledge"}},
		{"workdir flag", "git -C ../repos/example status", []string{"../repos/example"}},
		{"cd", "cd ../repos/example && go test ./...", []string{"../repos/example"}},
		{"variable target is not guessed", "echo hi > $OUT/x.md", nil},
		{"glob target is not guessed", "echo hi > docs/*.md", nil},
		{"plain command", "go test ./...", nil},
		{"empty command", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targets := bashTargets(tc.command)
			var got []string
			for _, target := range targets {
				got = append(got, target.Path)
			}
			if tc.want == nil {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestOpPathTargetsUsesTheResolvedPath: the workspace layer's report is the half
// that is already resolved and canonical, so it is kept even when the arguments
// used a different spelling.
func TestOpPathTargetsUsesTheResolvedPath(t *testing.T) {
	targets := opPathTargets([]types.WorkspaceOperation{
		{Kind: "write", Path: "docs/self/x.md"},
		{Kind: "read", Path: "repos/example/main.go"},
		{Kind: "search", Path: "knowledge"},
		{Kind: "bash"},
	})
	require.Len(t, targets, 4)
	assert.Equal(t, pathTarget{Path: "docs/self/x.md", Kind: "write"}, targets[0])
	assert.Equal(t, pathTarget{Path: "docs/self", Kind: "write"}, targets[1])
	assert.Equal(t, pathTarget{Path: "repos/example/main.go", Kind: "read"}, targets[2])
	assert.Equal(t, pathTarget{Path: "knowledge", Kind: "search"}, targets[3])
}
