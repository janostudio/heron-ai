package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func loadPermissionsFrom(t *testing.T, contents string) types.PermissionsConfig {
	t.Helper()
	InvalidateSettingsCache()
	t.Cleanup(InvalidateSettingsCache)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agents", "settings.json"), []byte(contents), 0o644))
	loader := &ConfigLoader{baseDir: dir, fileStore: storage.NewFileStore(dir)}
	return loader.LoadPermissions()
}

func TestLoadPermissionsReadsThePosture(t *testing.T) {
	cfg := loadPermissionsFrom(t, `{
  "permissions": {"bash": "ask", "write": "deny", "websearch": "allow", "webfetch": "ask"}
}`)

	require.Empty(t, cfg.Validate())
	require.Equal(t, types.PermissionAsk, cfg.DecisionFor("Bash"))
	require.Equal(t, types.PermissionDeny, cfg.DecisionFor("Write"))
	require.Equal(t, types.PermissionAllow, cfg.DecisionFor("WebSearch"))
	require.Equal(t, types.PermissionAsk, cfg.DecisionFor("WebFetch"))
	// A tool outside the three classes is not governed by the posture.
	require.Equal(t, types.PermissionAllow, cfg.DecisionFor("Read"))
}

// No file, no section: the zero value allows everything, which is what every
// installation did before the section existed.
func TestLoadPermissionsAbsentIsAllowAll(t *testing.T) {
	InvalidateSettingsCache()
	t.Cleanup(InvalidateSettingsCache)
	dir := t.TempDir()
	loader := &ConfigLoader{baseDir: dir, fileStore: storage.NewFileStore(dir)}

	cfg := loader.LoadPermissions()
	require.Empty(t, cfg.Validate())
	require.Equal(t, types.PermissionAllow, cfg.DecisionFor("Bash"))
	require.Equal(t, types.PermissionAllow, cfg.DecisionFor("Write"))
}

// A typo is reported, never reinterpreted: silently turning "block" into allow
// would look like a restriction that is not there.
func TestLoadPermissionsReportsUnknownDecisions(t *testing.T) {
	cfg := loadPermissionsFrom(t, `{"permissions": {"bash": "block", "write": "ask"}}`)

	problems := cfg.Validate()
	require.Len(t, problems, 1)
	require.Contains(t, problems[0], "permissions.bash")
	require.Contains(t, problems[0], "block")

	// The valid entry still applies, and the invalid one resolves to allow so the
	// engine starts: a typo must not become an outage.
	require.Equal(t, types.PermissionAsk, cfg.DecisionFor("Write"))
	require.Equal(t, types.PermissionAllow, cfg.DecisionFor("Bash"))
}

// The unknown-key report has one schema for the whole file. A new section that is
// missing there is reported on every start — a warning that is wrong is worse than
// no warning, because it teaches people to ignore the report.
func TestSettingsSchemaKnowsThePermissionSection(t *testing.T) {
	known := []byte(`{"permissions": {"bash": "ask", "write": "allow", "websearch": "allow", "webfetch": "deny"}}`)
	require.Empty(t, CheckSettingsFields(known), "the four fields are read by LoadPermissions")

	// A misspelling inside the section is still a misspelling, and the report
	// suggests the key that was meant. `shell` is reported without a suggestion on
	// purpose: "shell" and "bash" are not textually close, and guessing would be
	// worse than saying nothing.
	fields := CheckSettingsFields([]byte(`{"permissions": {"bassh": "ask", "shell": "deny"}}`))
	require.Len(t, fields, 2)
	require.Equal(t, "permissions.bassh", fields[0].Path)
	require.Equal(t, "bash", fields[0].Suggestion)
	require.Equal(t, "permissions.shell", fields[1].Path)
	require.Empty(t, fields[1].Suggestion)
}
