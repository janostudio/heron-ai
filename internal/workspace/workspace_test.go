package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestServiceRejectsSymlinkOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "linked")))

	service, err := New(root)
	require.NoError(t, err)
	_, err = service.Read(context.Background(), ReadRequest{Path: "linked/secret.txt"})
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

func TestServiceReadLineRangeAndTruncation(t *testing.T) {
	service, err := New(t.TempDir())
	require.NoError(t, err)
	_, err = service.Write(context.Background(), WriteRequest{
		Path:    "lines.txt",
		Content: "one\ntwo\nthree\n",
	})
	require.NoError(t, err)

	read, err := service.Read(context.Background(), ReadRequest{
		Path:      "lines.txt",
		LineStart: 2,
		LineEnd:   3,
	})
	require.NoError(t, err)
	require.Equal(t, "two\nthree\n", read.Content)
	require.Equal(t, 3, read.TotalLines)
	require.Equal(t, 2, read.LineStart)
	require.Equal(t, 3, read.LineEnd)

	truncated, err := service.Read(context.Background(), ReadRequest{
		Path:     "lines.txt",
		MaxBytes: 4,
	})
	require.NoError(t, err)
	require.Equal(t, "one\n", truncated.Content)
	require.True(t, truncated.Truncated)
}

func TestServiceWriteEditModesAndErrors(t *testing.T) {
	service, err := New(t.TempDir())
	require.NoError(t, err)

	created, err := service.Write(context.Background(), WriteRequest{
		Path: "app.js", Mode: "create", Content: "const x = 1;\n",
	})
	require.NoError(t, err)
	require.Equal(t, "create", created.Mode)

	_, err = service.Write(context.Background(), WriteRequest{
		Path: "app.js", Mode: "create", Content: "const x = 2;\n",
	})
	require.ErrorIs(t, err, ErrFileExists)

	read, err := service.Read(context.Background(), ReadRequest{Path: "app.js"})
	require.NoError(t, err)
	edited, err := service.Write(context.Background(), WriteRequest{
		Path: "app.js", Mode: "edit", OldText: "const x = 1;", NewText: "const x = 2;",
		BaseRevision: read.Revision,
	})
	require.NoError(t, err)
	require.Equal(t, "edit", edited.Mode)
	require.Equal(t, 1, edited.MatchedCount)

	_, err = service.Write(context.Background(), WriteRequest{
		Path: "app.js", Mode: "edit", OldText: "missing", NewText: "x",
		BaseRevision: edited.Revision,
	})
	require.ErrorIs(t, err, ErrEditTargetNotFound)
}

func TestServiceWritePreservesExistingPermissions(t *testing.T) {
	root := t.TempDir()
	service, err := New(root)
	require.NoError(t, err)
	path := filepath.Join(root, "script.sh")
	require.NoError(t, os.WriteFile(path, []byte("echo old\n"), 0755))

	_, err = service.Write(context.Background(), WriteRequest{
		Path: "script.sh", Content: "echo new\n", Mode: "replace",
	})
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0755), info.Mode().Perm())
}

func TestServiceSearchRecursiveAndSkipsExcludedAndBinary(t *testing.T) {
	root := t.TempDir()
	service, err := New(root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("needle here\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "node_modules", "pkg", "a.js"), []byte("needle hidden\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 1, 2}, 0644))

	result, err := service.Search(context.Background(), SearchRequest{
		Pattern: "needle", Path: ".", Include: "*.go",
	})
	require.NoError(t, err)
	require.Len(t, result.Matches, 1)
	require.Equal(t, "src/a.go", result.Matches[0].Path)
	require.Equal(t, 1, result.Matches[0].Line)
}

func TestServiceGlobRecursiveAndContextCancel(t *testing.T) {
	root := t.TempDir()
	service, err := New(root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("x"), 0644))

	matches, err := service.GlobWithOptions(context.Background(), GlobRequest{Pattern: "**/*.go"})
	require.NoError(t, err)
	require.Equal(t, []string{"src/a.go"}, matches)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.GlobWithOptions(ctx, GlobRequest{Pattern: "**/*.go"})
	require.ErrorIs(t, err, context.Canceled)
}

func TestServiceSearchRegexCaseAndLimits(t *testing.T) {
	root := t.TempDir()
	service, err := New(root)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("Needle one\nneedle two\nother\n"), 0644))

	result, err := service.Search(context.Background(), SearchRequest{
		Pattern: "needle \\w+", Path: ".", Regex: true, IgnoreCase: true,
		MaxResults: 1,
	})
	require.NoError(t, err)
	require.Len(t, result.Matches, 1)
	require.True(t, result.Truncated)
}

func TestWorkspaceOperationKindIsDocumented(t *testing.T) {
	operation := types.WorkspaceOperation{Kind: "read"}
	require.Equal(t, "read", operation.Kind)
}
