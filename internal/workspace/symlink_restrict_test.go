package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSymlinkCannotEscapeRestriction is the regression guard for the documented
// but unimplemented symlink check: resolve returned the *unresolved* relative
// path, so a link inside the workspace whose target sat in a denied directory
// passed the prefix rule under its own name while open() followed the link. The
// comment said the denial ran "after symlink resolution".
func TestSymlinkCannotEscapeRestriction(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "agents", "victim", "knowledge"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "agents", "victim", "knowledge", "private.md"), []byte("secret"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "agents", "me", "knowledge"), 0o755))

	// A link at the top level: its own name is not under the denied prefix.
	require.NoError(t, os.Symlink(filepath.Join(".agents", "agents", "victim", "knowledge"), filepath.Join(root, "link")))

	ws, err := NewLocal(root)
	require.NoError(t, err)
	restriction := Restriction{
		Denied:  []string{".agents/agents", ".agents/teams"},
		Allowed: []string{".agents/agents/me/knowledge"},
	}

	_, err = ws.Read(context.Background(), ReadRequest{Path: "link/private.md", Restrict: restriction})
	require.Error(t, err, "reading through a symlink into a denied directory must be refused")
	require.ErrorIs(t, err, ErrFileNotFound)

	// Writing through the same link must be refused too.
	_, err = ws.Write(context.Background(), WriteRequest{
		Path:     "link/planted.md",
		Content:  "x",
		Mode:     "create",
		Restrict: restriction,
	})
	require.Error(t, err, "writing through a symlink into a denied directory must be refused")

	// And the caller's own tree stays reachable through the same resolve path.
	_, err = ws.Read(context.Background(), ReadRequest{Path: ".agents/agents/me/knowledge/ok.md", Restrict: restriction})
	require.ErrorIs(t, err, ErrFileNotFound, "missing file, not a refusal: the allowed path resolves fine")
}

// TestSymlinkEscapeOutsideWorkspaceStillRefused keeps the older guarantee: a
// link pointing out of the workspace is rejected.
func TestSymlinkEscapeOutsideWorkspaceStillRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.md"), []byte("x"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))

	ws, newErr := NewLocal(root)
	require.NoError(t, newErr)
	_, err := ws.Read(context.Background(), ReadRequest{Path: "escape/secret.md"})
	require.ErrorIs(t, err, ErrPathOutsideWorkspace)
}
