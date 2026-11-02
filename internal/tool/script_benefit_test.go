package tool_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The claim this feature makes is measurable without a model: for the same
// task, how many bytes of tool output would have to enter the conversation.
// This measures exactly that over the real tool stack, so a regression in the
// design shows up as a number rather than as an opinion.

// TestBenefitContextFootprint measures the task "scan N files, report which
// contain TODO" both ways.
func TestBenefitContextFootprint(t *testing.T) {
	const files = 20
	scriptTool, executor, root := newScriptFixture(t)

	var names []string
	for i := 0; i < files; i++ {
		body := strings.Repeat("filler line without the marker\n", 30) + "TODO: fix this\n"
		name := fmt.Sprintf("f%d.txt", i)
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o644))
		names = append(names, name)
	}

	// Standard mode: one Read per file, and every result enters the conversation.
	var direct strings.Builder
	for _, name := range names {
		result, err := executor.Execute(context.Background(), "Read", map[string]any{"file": name})
		require.NoError(t, err)
		require.True(t, result.Success, "read failed: %s", result.Error)
		direct.WriteString(result.Content)
	}

	// Script mode: one call, one conclusion. The bodies stay in the sandbox.
	var code strings.Builder
	code.WriteString("const names = [")
	for i, name := range names {
		if i > 0 {
			code.WriteString(",")
		}
		code.WriteString(fmt.Sprintf("%q", name))
	}
	code.WriteString(`];
		const hits = [];
		for (const n of names) {
			const r = tools.Read({file: n});
			if (r.content.indexOf("TODO") >= 0) hits.push(n);
		}
		return hits.join(",");`)

	result, err := scriptTool.Execute(context.Background(), map[string]any{"code": code.String()})
	require.NoError(t, err)
	require.True(t, result.Success, "script failed: %s", result.Error)

	directBytes := direct.Len()
	scriptBytes := len(result.Content)
	ratio := float64(directBytes) / float64(scriptBytes)

	t.Logf("task: scan %d files for TODO", files)
	t.Logf("standard mode tool output: %d bytes (one Read per file)", directBytes)
	t.Logf("script mode tool output:   %d bytes (one conclusion)", scriptBytes)
	t.Logf("reduction: %.1fx", ratio)

	assert.Greater(t, ratio, 10.0, "the script path must cut the footprint by an order of magnitude")
	// Fewer bytes only matter if the answer survives: the script must have found
	// every file.
	found := strings.Split(result.Content, ",")
	assert.Len(t, found, files, "the script must report every matching file")
}
