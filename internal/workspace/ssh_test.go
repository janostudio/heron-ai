package workspace

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSSHResolvePath(t *testing.T) {
	w := &sshWorkspace{root: "/root/workspace"}

	tests := []struct {
		name       string
		input      string
		wantAbs    string
		wantRel    string
		wantErr    bool
	}{
		{"relative simple", "src/main.go", "/root/workspace/src/main.go", "src/main.go", false},
		{"relative nested", "a/b/c.txt", "/root/workspace/a/b/c.txt", "a/b/c.txt", false},
		{"dot", ".", "/root/workspace", ".", false},
		{"dot slash", "./x", "/root/workspace/x", "x", false},
		{"cleanup", "a/../b", "/root/workspace/b", "b", false},
		{"abs inside", "/root/workspace/foo", "/root/workspace/foo", "foo", false},
		{"traversal parent", "../etc/passwd", "", "", true},
		{"traversal deep", "a/../../b", "", "", true},
		{"abs outside", "/etc/passwd", "", "", true},
		{"empty", "", "", "", true},
		{"empty spaces", "   ", "", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			abs, rel, err := w.resolve(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantAbs, abs)
			require.Equal(t, tc.wantRel, rel)
		})
	}
}

func TestSSHResolveTraversalWithinName(t *testing.T) {
	// A filename that merely contains ".." but is not a traversal must resolve.
	w := &sshWorkspace{root: "/root/workspace"}
	abs, rel, err := w.resolve("foo..bar.txt")
	require.NoError(t, err)
	require.Equal(t, "/root/workspace/foo..bar.txt", abs)
	require.Equal(t, "foo..bar.txt", rel)
}

func TestRelPath(t *testing.T) {
	tests := []struct {
		base, target string
		want         string
		wantErr      bool
	}{
		{"/root/workspace", "/root/workspace", ".", false},
		{"/root/workspace", "/root/workspace/a", "a", false},
		{"/root/workspace", "/root/workspace/a/b", "a/b", false},
		{"/", "/a", "a", false},
		{"/root/workspace", "/other", "", true},
		{"/root/workspace", "/root/workspace2/x", "", true},
	}
	for _, tc := range tests {
		got, err := relPath(tc.base, tc.target)
		if tc.wantErr {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
}

func TestParseGrepLine(t *testing.T) {
	tests := []struct {
		line        string
		wantRel     string
		wantLineNo  int
		wantContent string
		wantOK      bool
	}{
		{"src/main.go:42:fmt.Println", "src/main.go", 42, "fmt.Println", true},
		{"a/b.txt:1:", "a/b.txt", 1, "", true},
		{"foo:bar:baz:qux", "foo", 0, "", false}, // non-numeric line
		{"no-colon", "", 0, "", false},
		{":42:content", "", 0, "", false}, // blank path
	}
	for _, tc := range tests {
		rel, lineNo, content, ok := parseGrepLine(tc.line)
		require.Equal(t, tc.wantOK, ok, tc.line)
		if tc.wantOK {
			require.Equal(t, tc.wantRel, rel)
			require.Equal(t, tc.wantLineNo, lineNo)
			require.Equal(t, tc.wantContent, content)
		}
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "''"},
		{"hello", "'hello'"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
		{"/root/workspace", "'/root/workspace'"},
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, shellQuote(tc.in))
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	require.Equal(t, home, expandHome("~"))
	require.Equal(t, home+"/.ssh/id_ed25519", expandHome("~/.ssh/id_ed25519"))
	require.Equal(t, "/abs/path", expandHome("/abs/path"))
	require.Equal(t, "rel/path", expandHome("rel/path"))
}
