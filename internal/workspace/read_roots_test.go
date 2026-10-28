package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReadRootAllowsReadingOutsideWorkspace is the P3 regression: the engine
// runs with its cwd set to a subdirectory while the repositories it works on
// live one level up. Without a read root every such read is rejected as
// "path escapes workspace", and the model falls back to Bash — which does not
// enforce the same boundary.
func TestReadRootAllowsReadingOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	target := filepath.Join(outsideRoot, "repo", "main.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("package main\n"), 0o644))

	service, err := NewLocalWithReadRoots(root, []string{outsideRoot})
	require.NoError(t, err)

	result, err := service.Read(context.Background(), ReadRequest{Path: target})
	require.NoError(t, err)
	require.Contains(t, result.Content, "package main")

	// The same read must still fail without the grant.
	strict, err := NewLocal(root)
	require.NoError(t, err)
	_, err = strict.Read(context.Background(), ReadRequest{Path: target})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPathOutsideWorkspace))
}

// TestReadRootRejectsWrites keeps the guarantee asymmetric: a read root is a
// wider view, not a second workspace.
func TestReadRootRejectsWrites(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	target := filepath.Join(outsideRoot, "notes.md")

	service, err := NewLocalWithReadRoots(root, []string{outsideRoot})
	require.NoError(t, err)

	_, err = service.Write(context.Background(), WriteRequest{Path: target, Content: "x"})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPathOutsideWorkspace),
		"write into a read-only root must be refused, got %v", err)

	_, statErr := os.Stat(target)
	require.True(t, os.IsNotExist(statErr), "nothing may be created in a read root")
}

// TestReadRootIsNotAncestorGrant makes sure the grant is the directory and not
// everything beside it: a sibling of the read root stays unreachable.
func TestReadRootIsNotAncestorGrant(t *testing.T) {
	root := t.TempDir()
	parent := t.TempDir()
	allowed := filepath.Join(parent, "allowed")
	sibling := filepath.Join(parent, "sibling")
	require.NoError(t, os.MkdirAll(allowed, 0o755))
	require.NoError(t, os.MkdirAll(sibling, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sibling, "secret.txt"), []byte("s"), 0o644))

	service, err := NewLocalWithReadRoots(root, []string{allowed})
	require.NoError(t, err)

	_, err = service.Read(context.Background(), ReadRequest{Path: filepath.Join(sibling, "secret.txt")})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPathOutsideWorkspace))
}

// TestReadRootDoesNotFollowEscapingSymlink: a symlink inside the granted root
// that points outside it must not widen the grant.
func TestReadRootDoesNotFollowEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	allowed := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(allowed, "escape")))

	service, err := NewLocalWithReadRoots(root, []string{allowed})
	require.NoError(t, err)

	_, err = service.Read(context.Background(), ReadRequest{Path: filepath.Join(allowed, "escape", "secret.txt")})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPathOutsideWorkspace),
		"a link out of a read root must not be followed, got %v", err)
}

// TestReadRootMissingIsSkipped: a stale read root must not stop the engine from
// starting. Relative entries resolve against the process cwd, so a config that
// is correct in one deployment can be wrong in another — that is not a reason
// to refuse to run.
func TestReadRootMissingIsSkipped(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "does-not-exist")

	service, err := NewLocalWithReadRoots(root, []string{missing})
	require.NoError(t, err, "a missing read root must not fail construction")

	_, err = service.Read(context.Background(), ReadRequest{Path: filepath.Join(missing, "x.txt")})
	require.Error(t, err)
}
