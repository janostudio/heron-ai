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
calls:
  assistant:
    type: agent
    agent: default-assistant
    output:
      record: AssistantReply
`), 0644))

	require.NoError(t, os.WriteFile(filepath.Join(teamsDir, "verify.yml"), []byte(`
id: verify-team
calls:
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
	require.Equal(t, types.CallCommand, definitions.Teams["verify-team"].Calls["unit_test"].Type)
	require.Equal(t, types.CallWebhook, definitions.Teams["verify-team"].Calls["notify"].Type)
}

func TestLoadDefinitionsRejectsLifecycleOnProceedActions(t *testing.T) {
	for _, action := range []string{"complete", "wait_input"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			teamsDir := filepath.Join(root, ".agents", "teams")
			flowsDir := filepath.Join(root, ".agents", "flows")
			require.NoError(t, os.MkdirAll(teamsDir, 0755))
			require.NoError(t, os.MkdirAll(flowsDir, 0755))

			require.NoError(t, os.WriteFile(filepath.Join(flowsDir, "default.yml"), []byte(`
id: flow
entry: default
teams:
  default:
    team: default-team
    coordinator: true
    on_proceed:
      action: `+action+`
`), 0644))
			require.NoError(t, os.WriteFile(filepath.Join(teamsDir, "default.yml"), []byte(`
id: default-team
calls: {}
`), 0644))

			_, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
				FlowPath: filepath.Join(flowsDir, "default.yml"),
			})
			require.ErrorContains(t, err, "会话生命周期不再可配置")
			require.ErrorContains(t, err, action)
		})
	}
}

func TestLoadDefinitionsRejectsUnknownOnProceedAction(t *testing.T) {
	root := t.TempDir()
	teamsDir := filepath.Join(root, ".agents", "teams")
	flowsDir := filepath.Join(root, ".agents", "flows")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, os.MkdirAll(flowsDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(flowsDir, "default.yml"), []byte(`
id: flow
entry: default
teams:
  default:
    team: default-team
    coordinator: true
    on_proceed:
      action: wait_tool
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(teamsDir, "default.yml"), []byte(`
id: default-team
calls: {}
`), 0644))

	_, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(flowsDir, "default.yml"),
	})
	require.ErrorContains(t, err, "not an orchestration action")
}

func TestLoadDefinitionsAcceptsOrchestrationOnProceedActions(t *testing.T) {
	for _, action := range []string{"proceed", "return", "coordinate", "activate", "fail"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			teamsDir := filepath.Join(root, ".agents", "teams")
			flowsDir := filepath.Join(root, ".agents", "flows")
			require.NoError(t, os.MkdirAll(teamsDir, 0755))
			require.NoError(t, os.MkdirAll(flowsDir, 0755))

			require.NoError(t, os.WriteFile(filepath.Join(flowsDir, "default.yml"), []byte(`
id: flow
entry: default
teams:
  default:
    team: default-team
    coordinator: true
    on_proceed:
      action: `+action+`
`), 0644))
			require.NoError(t, os.WriteFile(filepath.Join(teamsDir, "default.yml"), []byte(`
id: default-team
calls: {}
`), 0644))

			_, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
				FlowPath: filepath.Join(flowsDir, "default.yml"),
			})
			require.NoError(t, err)
		})
	}
}

func TestLoadDefinitionsRejectsMissingAgentDefinition(t *testing.T) {	root := t.TempDir()
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
calls:
  assistant:
    type: agent
    agent: missing-agent
`), 0644))

	_, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(flowsDir, "default.yml"),
	})
	require.ErrorContains(t, err, `references missing agent definition "missing-agent"`)
}

func TestLoadDefinitionsLoadsDirectoryAgentWithPrivateKnowledgeAndRules(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, ".agents", "agents")
	teamsDir := filepath.Join(root, ".agents", "teams")
	flowsDir := filepath.Join(root, ".agents", "flows")
	require.NoError(t, os.MkdirAll(filepath.Join(agentsDir, "assistant", "knowledge"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(agentsDir, "assistant", "rules"), 0755))
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
calls:
  assistant:
    type: agent
    agent: assistant
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "assistant", "AGENT.md"), []byte(`---
name: assistant
persona:
  role: assistant
  goal: answer
---
Answer using the private knowledge directory.
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "assistant", "knowledge", "index.md"), []byte("# Assistant Knowledge Index\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "assistant", "knowledge", "domain.md"), []byte(`---
id: domain
title: Domain
keys: [domain]
scope:
  type: agents
  agents: [assistant]
---
Private domain knowledge.
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "assistant", "rules", "private.md"), []byte(`---
id: private
type: hard
scope:
  type: agents
  agents: [assistant]
priority: 10
---
Keep the answer concise.
`), 0644))

	definitions, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(flowsDir, "default.yml"),
	})
	require.NoError(t, err)
	require.Contains(t, definitions.Agents, "assistant")
	require.Equal(t, "assistant", definitions.Agents["assistant"].Name)
	require.Contains(t, definitions.Rules, "private")
}

func TestLoadDefinitionsValidatesSkillPackageScripts(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, ".agents", "agents")
	teamsDir := filepath.Join(root, ".agents", "teams")
	flowsDir := filepath.Join(root, ".agents", "flows")
	skillDir := filepath.Join(root, ".agents", "skills", "checks")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, os.MkdirAll(flowsDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "scripts"), 0755))

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
calls: {}
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: checks
description: deterministic checks
scripts:
  - scripts/check.sh
---
Run the packaged check.
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "scripts", "check.sh"), []byte("#!/bin/sh\n"), 0755))

	definitions, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(flowsDir, "default.yml"),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"scripts/check.sh"}, definitions.Skills["checks"].Scripts)

	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: checks
description: deterministic checks
scripts:
  - scripts/missing.sh
---
`), 0644))
	_, err = NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(flowsDir, "default.yml"),
	})
	require.ErrorContains(t, err, `skill script "scripts/missing.sh" does not exist`)
}

func TestLoadKnowledgeSettings_ReadsCuratorModel(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"), []byte(`{
  "knowledge": {
    "curator_model": "gpt-x"
  }
}`), 0644))

	cfg := NewConfigLoader(root).LoadKnowledgeSettings()
	require.Equal(t, "gpt-x", cfg.CuratorModel)
}

func TestLoadKnowledgeSettings_MissingFileReturnsZero(t *testing.T) {
	root := t.TempDir()

	cfg := NewConfigLoader(root).LoadKnowledgeSettings()
	require.Equal(t, "", cfg.CuratorModel)
}

func TestLoadDefinitionsLoadsRuntimeLimits(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "agents"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "teams"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "flows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"), []byte(`{
  "runtime": {
    "max_team_turns": 7,
    "max_calls_per_team_turn": 8,
    "max_agent_rounds": 99,
    "max_parallel_teams": 3,
    "max_parallel_calls": 4,
    "max_parallel_tools": 4
  }
}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "flows", "default.yml"), []byte(`
id: flow
entry: default
teams:
  default:
    team: default-team
    coordinator: true
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "teams", "default.yml"), []byte(`
id: default-team
calls: {}
`), 0644))

	definitions, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(root, ".agents", "flows", "default.yml"),
	})
	require.NoError(t, err)
	require.Equal(t, 7, definitions.Limits.MaxTeamTurns)
	require.Equal(t, 8, definitions.Limits.MaxCallsPerTeamTurn)
	require.Equal(t, 99, definitions.Limits.MaxAgentRounds)
	require.Equal(t, 3, definitions.Limits.MaxParallelTeams)
	require.Equal(t, 4, definitions.Limits.MaxParallelCalls)
	require.Equal(t, 4, definitions.Limits.MaxParallelTools)
}
