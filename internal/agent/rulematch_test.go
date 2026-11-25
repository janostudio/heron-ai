package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRulePathMatches pins the three pattern shapes. The failing case that
// matters is the nested one: Go's path.Match cannot express "everything under
// docs/", so a "'**' handled as a glob" implementation would look correct on
// "docs/a.md" and silently never fire on a real path.
func TestRulePathMatches(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		target  string
		want    bool
	}{
		{"prefix matches a direct child", "docs/**", "docs/a.md", true},
		{"prefix matches a nested path", "docs/**", "docs/self/design/x.md", true},
		{"prefix matches the directory itself", "docs/**", "docs/self", true},
		{"prefix does not match a sibling with the same stem", "docs/**", "docsx/a.md", false},
		{"single star stays one level", "docs/*", "docs/a.md", true},
		{"single star does not cross a directory", "docs/*", "docs/a/b.md", false},
		{"exact file", "docs/INDEX.md", "docs/INDEX.md", true},
		{"exact file does not match another", "docs/INDEX.md", "docs/other.md", false},
		{"leading ./ is the same place", "docs/**", "./docs/a.md", true},
		{"leading / is the same place", "docs/**", "/docs/a.md", true},
		{"backslashes are separators", "docs/**", `docs\self\x.md`, true},
		{"parent-relative pattern is allowed", "../docs/**", "../docs/self/x.md", true},
		{"parent-relative pattern stays distinct", "../docs/**", "docs/self/x.md", false},
		{"empty pattern matches nothing", "", "docs/a.md", false},
		{"empty target matches nothing", "docs/**", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, rulePathMatches(tc.pattern, tc.target))
		})
	}
}

// TestFirstMatchPathReportsWhatTriggered: the note tells the model which path
// made the rule apply, so the match has to come back, not just the boolean.
func TestFirstMatchPathReportsWhatTriggered(t *testing.T) {
	require.Equal(t,
		"docs/self/x.md",
		firstMatchPath([]string{"repos/**", "docs/**"}, []string{"src/a.go", "docs/self/x.md"}))
	require.Equal(t, "", firstMatchPath([]string{"repos/**"}, []string{"docs/x.md"}))
}
