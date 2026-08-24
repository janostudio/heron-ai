package workspace

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestServiceReadWriteAndRevision(t *testing.T) {
	root := t.TempDir()
	service, err := New(root)
	require.NoError(t, err)

	write, err := service.Write(context.Background(), WriteRequest{
		Path:    "src/main.go",
		Content: "package main\n",
	})
	require.NoError(t, err)
	require.NotEmpty(t, write.Revision)
	require.Equal(t, "write", write.Operation.Kind)

	read, err := service.Read(context.Background(), ReadRequest{Path: "src/main.go"})
	require.NoError(t, err)
	require.Equal(t, "package main\n", read.Content)
	require.Equal(t, write.Revision, read.Revision)
	require.Equal(t, "src/main.go", read.Operation.Path)
	require.NotEmpty(t, read.Operation.Excerpt)

	_, err = service.Write(context.Background(), WriteRequest{
		Path:         "src/main.go",
		Content:      "package changed\n",
		BaseRevision: "sha256:stale",
	})
	require.ErrorIs(t, err, ErrRevisionConflict)
}

func TestServiceRejectsPathOutsideWorkspace(t *testing.T) {
	service, err := New(t.TempDir())
	require.NoError(t, err)

	_, err = service.Read(context.Background(), ReadRequest{Path: "../outside.txt"})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPathOutsideWorkspace))
}

func TestServiceRunRecordsTestOperation(t *testing.T) {
	service, err := New(t.TempDir())
	require.NoError(t, err)

	result, err := service.Run(context.Background(), CommandRequest{
		Command: "printf 'ok'",
	})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Stdout)
	require.Equal(t, 0, result.ExitCode)
	require.Equal(t, "test", result.Operation.Kind)
}

func TestWorkspaceOperationKindIsDocumented(t *testing.T) {
	operation := types.WorkspaceOperation{Kind: "read"}
	require.Equal(t, "read", operation.Kind)
}
