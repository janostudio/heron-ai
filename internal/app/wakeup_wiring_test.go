package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// The wake-up tools are visible only where they can actually work: a process
// that outlives its turns and whose event stream a client can follow. Anywhere
// else the model must not even see them — offering the tool and refusing it
// would still put the idea in its head.
func TestWakeupToolsAreOnlyRegisteredInServeMode(t *testing.T) {
	_, flowPath, definitions := fixtureConfigRoot(t, t.TempDir())
	workspaceRoot := t.TempDir()
	store := types.NewDefinitionStore(definitions, filepath.Dir(flowPath), flowPath)

	plain, err := BuildRuntime(context.Background(), store, &inertModel{}, workspaceRoot, "error")
	require.NoError(t, err)
	_, visible := plain.ToolExecutor.ToolSchema("ScheduleWakeup")
	require.False(t, visible, "a non-serve runtime must not expose the wake-up tool")
	_, visible = plain.ToolExecutor.ToolSchema("CancelWakeup")
	require.False(t, visible)

	serving, err := BuildRuntime(context.Background(), store, &inertModel{}, workspaceRoot, "error", WithWakeupTools(true))
	require.NoError(t, err)
	_, visible = serving.ToolExecutor.ToolSchema("ScheduleWakeup")
	require.True(t, visible, "serve mode is where a wake-up can reach someone")
	_, visible = serving.ToolExecutor.ToolSchema("CancelWakeup")
	require.True(t, visible, "the pair is registered together or not at all")
}
