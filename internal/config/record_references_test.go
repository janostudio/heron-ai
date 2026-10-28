package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeRefFixture writes a one-Team flow whose calls are supplied by the caller
// and returns the loaded definitions (or the load error).
func loadRefFixture(t *testing.T, flowYAML, teamYAML, agentYAML string) error {
	t.Helper()
	root := t.TempDir()
	configRoot := filepath.Join(root, ".agents")
	for _, dir := range []string{"agents", "teams", "flows"} {
		require.NoError(t, os.MkdirAll(filepath.Join(configRoot, dir), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(configRoot, "settings.json"), []byte(`{"runtime":{"max_team_turns":7}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configRoot, "flows", "default.yml"), []byte(flowYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configRoot, "teams", "worker.yml"), []byte(teamYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configRoot, "agents", "worker.md"), []byte(agentYAML), 0o644))

	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

	_, err = NewConfigLoader(".").LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(".agents", "flows", "default.yml"),
	})
	return err
}

const refFlow = `
id: refs
entry: main
teams:
  main:
    team: worker-team
    inputs:
      user_message: true
`

const refAgent = `---
name: worker-agent
persona:
  role: worker
---
Work.
`

// TestRecordReferenceRejectsUnknownProducer: an input naming a record nothing
// publishes used to load fine and only surfaced at runtime as "no evidence".
func TestRecordReferenceRejectsUnknownProducer(t *testing.T) {
	err := loadRefFixture(t, refFlow, `
id: worker-team
inputs:
  flow_records: [MissingReport]
calls:
  do_work:
    type: agent
    agent: worker-agent
`, refAgent)
	require.Error(t, err)
	require.Contains(t, err.Error(), "MissingReport")
	require.Contains(t, err.Error(), "no team or call publishes it")
}

// TestRecordReferenceAcceptsDeclaredProducer keeps the happy path working.
func TestRecordReferenceAcceptsDeclaredProducer(t *testing.T) {
	err := loadRefFixture(t, refFlow, `
id: worker-team
inputs:
  flow_records: [WorkerReport]
calls:
  do_work:
    type: agent
    agent: worker-agent
    output:
      record: WorkerReport
`, refAgent)
	require.NoError(t, err)
}

// TestRecordReferenceAcceptsBuiltinProducer: TeamFailureReport is published by
// the engine, so no definition can point at it.
func TestRecordReferenceAcceptsBuiltinProducer(t *testing.T) {
	err := loadRefFixture(t, refFlow, `
id: worker-team
inputs:
  flow_records: [TeamFailureReport]
calls:
  do_work:
    type: agent
    agent: worker-agent
`, refAgent)
	require.NoError(t, err)
}

// TestCommandPlaceholderRequiresDeclaredRecord: ${records.X} without X in the
// call's inputs used to fail at runtime with a missing-field error.
func TestCommandPlaceholderRequiresDeclaredRecord(t *testing.T) {
	err := loadRefFixture(t, refFlow, `
id: worker-team
calls:
  snapshot:
    type: command
    command:
      command: "bash script.sh"
      env:
        REPORT: "${records.MissingReport.Summary}"
    output:
      record: Snapshot
`, refAgent)
	require.Error(t, err)
	require.Contains(t, err.Error(), "records.MissingReport")
	require.Contains(t, err.Error(), "do not declare")
}

// TestCommandPlaceholderAcceptsDeclaredRecord is the counterpart: a declared
// record interpolates fine.
func TestCommandPlaceholderAcceptsDeclaredRecord(t *testing.T) {
	err := loadRefFixture(t, refFlow, `
id: worker-team
inputs:
  flow_records: [WorkerReport]
calls:
  produce:
    type: agent
    agent: worker-agent
    output:
      record: WorkerReport
  snapshot:
    type: command
    command:
      command: "bash script.sh"
      env:
        REPORT: "${records.WorkerReport.Data.repo}"
    inputs:
      flow_records: [WorkerReport]
    output:
      record: Snapshot
`, refAgent)
	require.NoError(t, err)
}
