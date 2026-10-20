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

func TestLoadDefinitionsRejectsMissingAgentDefinition(t *testing.T) {
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

func TestLoadKnowledgeSettings_ReadsSummaryModel(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"), []byte(`{
  "knowledge": {
    "summary_model": "gpt-x"
  }
}`), 0644))

	cfg := NewConfigLoader(root).LoadKnowledgeSettings()
	require.Equal(t, "gpt-x", cfg.SummaryModel)
}

func TestLoadKnowledgeSettings_MissingFileReturnsZero(t *testing.T) {
	root := t.TempDir()

	cfg := NewConfigLoader(root).LoadKnowledgeSettings()
	require.Equal(t, "", cfg.SummaryModel)
}

func TestLoadWebSettings_DefaultsToDuckDuckGo(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"), []byte(`{
  "web": {
    "api_key": "k"
  }
}`), 0644))

	cfg := NewConfigLoader(root).LoadWebSettings()
	require.Equal(t, types.WebProviderDuckDuckGo, cfg.Provider)
	require.Equal(t, "k", cfg.APIKey)
	require.Equal(t, types.DefaultWebSearchURL(types.WebProviderDuckDuckGo), cfg.Search.URL)
	require.Equal(t, types.DefaultWebFetchURL(types.WebProviderDuckDuckGo), cfg.Fetch.URL)
	require.Equal(t, 10, cfg.Search.MaxResults)
	require.Equal(t, "markdown", cfg.Fetch.Format)
}

func TestLoadWebSettings_ExplicitProviderAndURLWin(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"), []byte(`{
  "web": {
    "provider": "brave",
    "search": { "url": "https://custom/search" }
  }
}`), 0644))

	cfg := NewConfigLoader(root).LoadWebSettings()
	require.Equal(t, types.WebProviderBrave, cfg.Provider)
	require.Equal(t, "https://custom/search", cfg.Search.URL)
}

func TestLoadWebSettings_MissingSectionUsesDefaults(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"), []byte(`{}`), 0644))

	cfg := NewConfigLoader(root).LoadWebSettings()
	require.Equal(t, types.WebProviderDuckDuckGo, cfg.Provider)
	require.Empty(t, cfg.APIKey)
	require.Equal(t, types.DefaultWebSearchURL(types.WebProviderDuckDuckGo), cfg.Search.URL)
	require.Equal(t, types.DefaultWebFetchURL(types.WebProviderDuckDuckGo), cfg.Fetch.URL)
}

func TestLoadWebSettings_MissingFileUsesDefaults(t *testing.T) {
	root := t.TempDir()

	cfg := NewConfigLoader(root).LoadWebSettings()
	require.Equal(t, types.WebProviderDuckDuckGo, cfg.Provider)
	require.Empty(t, cfg.APIKey)
	require.Equal(t, types.DefaultWebSearchURL(types.WebProviderDuckDuckGo), cfg.Search.URL)
}

// 零配置可用：没有 web 段（或 web 段没写 provider）时必须落到 duckduckgo 且
// 无 key。这是「默认 duckduckgo」这条需求的锁死用例——配置层一旦把默认改回
// 要 key 的 provider，端到端就会 fail-closed，开箱即用失效。
func TestLoadWebSettings_ZeroConfigIsKeyless(t *testing.T) {
	for name, body := range map[string]string{
		"missing section": `{}`,
		"no provider":     `{"web": {"fetch": {"format": "markdown"}}}`,
		"empty provider":  `{"web": {"provider": ""}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0755))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"), []byte(body), 0644))

			cfg := NewConfigLoader(root).LoadWebSettings()
			require.Equal(t, types.WebProviderDuckDuckGo, cfg.Provider)
			require.Empty(t, cfg.APIKey)
			require.Equal(t, "https://api.duckduckgo.com/", cfg.Search.URL)
			// duckduckgo 没有 fetch API：endpoint 为空，tool 层按 direct 本地直连。
			require.Empty(t, cfg.Fetch.URL)
		})
	}
}

// web.api_key 为空时回退环境变量 WEB_SEARCH_API_KEY（与具体厂商无关：
// provider 与 key 是配套整段换的）。
func TestLoadWebSettings_FallsBackToEnvAPIKey(t *testing.T) {
	t.Setenv("WEB_SEARCH_API_KEY", "env-key")

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"), []byte(`{
  "web": { "provider": "brave" }
}`), 0644))

	cfg := NewConfigLoader(root).LoadWebSettings()
	require.Equal(t, "env-key", cfg.APIKey)
}

// 明文配置的优先级高于环境变量。
func TestLoadWebSettings_ExplicitAPIKeyBeatsEnv(t *testing.T) {
	t.Setenv("WEB_SEARCH_API_KEY", "env-key")

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"), []byte(`{
  "web": { "api_key": "file-key" }
}`), 0644))

	cfg := NewConfigLoader(root).LoadWebSettings()
	require.Equal(t, "file-key", cfg.APIKey)
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

func TestLoadLoggingSettings_ReadsConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"), []byte(`{
  "logging": {
    "level": "debug",
    "dir": ".agents/data/logs",
    "max_file_size": "10MB",
    "max_backups": 3,
    "retention_days": 14
  }
}`), 0644))

	cfg := NewConfigLoader(root).LoadLoggingSettings()
	require.Equal(t, "debug", cfg.Level)
	require.Equal(t, ".agents/data/logs", cfg.Dir)
	require.Equal(t, "10MB", cfg.MaxFileSize)
	require.Equal(t, 3, cfg.MaxBackups)
	require.Equal(t, 14, cfg.RetentionDays)
}

func TestLoadLoggingSettings_MissingFileReturnsZero(t *testing.T) {
	root := t.TempDir()

	cfg := NewConfigLoader(root).LoadLoggingSettings()
	require.Equal(t, "", cfg.Level)
	require.Equal(t, "", cfg.Dir)
	require.Equal(t, 0, cfg.RetentionDays)
}

func TestResolveWorkspace(t *testing.T) {
	ssh := func(tag string) *types.WorkspaceConfig {
		return &types.WorkspaceConfig{Type: "ssh", SSH: &types.SSHConfig{Host: tag}}
	}
	agent := func(ws *types.WorkspaceConfig) types.AgentConfig {
		return types.AgentConfig{Name: "a", Workspace: ws}
	}
	team := func(ws *types.WorkspaceConfig) types.Team {
		return types.Team{ID: "t", Workspace: ws}
	}

	t.Run("agent wins over team and flow", func(t *testing.T) {
		got := resolveWorkspace(
			types.Flow{Workspace: ssh("flow")},
			map[string]types.Team{"t": team(ssh("team"))},
			map[string]types.AgentConfig{"a": agent(ssh("agent"))},
		)
		require.NotNil(t, got)
		require.Equal(t, "agent", got.SSH.Host)
	})

	t.Run("team wins over flow", func(t *testing.T) {
		got := resolveWorkspace(
			types.Flow{Workspace: ssh("flow")},
			map[string]types.Team{"t": team(ssh("team"))},
			map[string]types.AgentConfig{"a": agent(nil)},
		)
		require.NotNil(t, got)
		require.Equal(t, "team", got.SSH.Host)
	})

	t.Run("flow used when lower levels empty", func(t *testing.T) {
		got := resolveWorkspace(
			types.Flow{Workspace: ssh("flow")},
			map[string]types.Team{"t": team(nil)},
			map[string]types.AgentConfig{"a": agent(nil)},
		)
		require.NotNil(t, got)
		require.Equal(t, "flow", got.SSH.Host)
	})

	t.Run("nil when nothing configured", func(t *testing.T) {
		got := resolveWorkspace(
			types.Flow{},
			map[string]types.Team{"t": team(nil)},
			map[string]types.AgentConfig{"a": agent(nil)},
		)
		require.Nil(t, got)
	})

	t.Run("empty type treated as unset", func(t *testing.T) {
		got := resolveWorkspace(
			types.Flow{},
			map[string]types.Team{"t": team(&types.WorkspaceConfig{})},
			map[string]types.AgentConfig{"a": agent(nil)},
		)
		require.Nil(t, got)
	})
}
