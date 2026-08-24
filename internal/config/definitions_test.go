package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestLoadDefinitions(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, ".agents", "agents")
	teamsDir := filepath.Join(root, ".agents", "teams")
	flowsDir := filepath.Join(root, ".agents", "flows")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, os.MkdirAll(flowsDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(flowsDir, "default.yml"), []byte(`
id: code-fix
entry: default
teams:
  default:
    team: default-team
    coordinator: true
    can_activate: [verify]
    inputs:
      user_message: true
  verify:
    team: verify-team
    depends_on: [default]
    inputs:
      - from: default
        record: DiagnosisReport
    on_proceed: [default]
`), 0644))

	require.NoError(t, os.WriteFile(filepath.Join(teamsDir, "default.yml"), []byte(`
id: default-team
members:
  assistant:
    type: subagent
    agent: default-assistant
    output:
      record: AssistantReply
`), 0644))

	require.NoError(t, os.WriteFile(filepath.Join(teamsDir, "verify.yml"), []byte(`
id: verify-team
members:
  unit_test:
    type: command
    command: "go test ./..."
    output:
      record: VerificationReport
  notify:
    type: webhook
    url: "https://example.com/api/comment"
    method: POST
`), 0644))

	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "default-assistant.md"), []byte(`---
name: default-assistant
persona:
  role: assistant
  goal: coordinate
model:
  provider: openai
  model: gpt-4o-mini
---
You coordinate the request.
`), 0644))

	definitions, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(flowsDir, "default.yml"),
	})
	require.NoError(t, err)
	require.NotNil(t, definitions)
	require.Equal(t, "code-fix", definitions.Flow.ID)
	require.Equal(t, "default-team", definitions.Flow.Teams["default"].TeamID)
	require.Len(t, definitions.Teams, 2)
	require.Len(t, definitions.Agents, 1)
	require.Equal(t, types.MemberCommand, definitions.Teams["verify-team"].Members["unit_test"].Type)
	require.Equal(t, types.MemberWebhook, definitions.Teams["verify-team"].Members["notify"].Type)
}

func TestLoadDefinitionsRejectsMissingSubagentDefinition(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, ".agents", "agents")
	teamsDir := filepath.Join(root, ".agents", "teams")
	flowsDir := filepath.Join(root, ".agents", "flows")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, os.MkdirAll(flowsDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(flowsDir, "default.yml"), []byte(`
id: flow
entry: default
teams:
  default:
    team: default-team
    coordinator: true
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(teamsDir, "default.yml"), []byte(`
id: default-team
members:
  assistant:
    type: subagent
    agent: missing-agent
`), 0644))

	_, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(flowsDir, "default.yml"),
	})
	require.ErrorContains(t, err, `references missing agent definition "missing-agent"`)
}
