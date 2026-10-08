package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestResolveModelProfile(t *testing.T) {
	models := []types.ModelProfile{
		{Name: "gpt-4o", Provider: "openai"},
		{Name: "claude-sonnet", Provider: "anthropic"},
	}

	t.Run("selected matches", func(t *testing.T) {
		cfg := &ModelsConfig{Model: "claude-sonnet", Models: models}
		got, err := resolveModelProfile(cfg)
		require.NoError(t, err)
		require.Equal(t, "claude-sonnet", got.Name)
	})

	t.Run("selected not found returns error", func(t *testing.T) {
		cfg := &ModelsConfig{Model: "claude-sonnet-typo", Models: models}
		_, err := resolveModelProfile(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "claude-sonnet-typo")
		require.Contains(t, err.Error(), "not found")
	})

	t.Run("selected empty falls back to first model", func(t *testing.T) {
		cfg := &ModelsConfig{Model: "", Models: models}
		got, err := resolveModelProfile(cfg)
		require.NoError(t, err)
		require.Equal(t, "gpt-4o", got.Name)
	})

	t.Run("config nil returns error", func(t *testing.T) {
		_, err := resolveModelProfile(nil)
		require.Error(t, err)
	})

	t.Run("empty models returns error", func(t *testing.T) {
		_, err := resolveModelProfile(&ModelsConfig{Model: "", Models: nil})
		require.Error(t, err)
	})
}

func TestResolveFlowPath(t *testing.T) {
	t.Run("explicit path wins", func(t *testing.T) {
		got := resolveFlowPath("/tmp/custom.yml")
		require.Equal(t, "/tmp/custom.yml", got)
	})

	t.Run("default yml found", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".agents", "flows"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".agents", "flows", "default.yml"), []byte("flow: x"), 0o644))
		withChdir(t, dir)
		got := resolveFlowPath("")
		require.Equal(t, ".agents/flows/default.yml", got)
	})

	t.Run("default yaml fallback", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".agents", "flows"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".agents", "flows", "default.yaml"), []byte("flow: x"), 0o644))
		withChdir(t, dir)
		got := resolveFlowPath("")
		require.Equal(t, ".agents/flows/default.yaml", got)
	})

	t.Run("no default returns empty", func(t *testing.T) {
		withChdir(t, t.TempDir())
		require.Equal(t, "", resolveFlowPath(""))
	})
}

func TestLoadModelsConfig(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".agents"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".agents", "models.json"),
			[]byte(`{"model":"gpt-4o","models":[{"name":"gpt-4o","provider":"openai"}]}`), 0o644))
		withChdir(t, dir)

		cfg, err := loadModelsConfig()
		require.NoError(t, err)
		require.Equal(t, "gpt-4o", cfg.Model)
		require.Len(t, cfg.Models, 1)
		require.Equal(t, "gpt-4o", cfg.Models[0].Name)
	})

	t.Run("file missing", func(t *testing.T) {
		withChdir(t, t.TempDir())
		_, err := loadModelsConfig()
		require.Error(t, err)
	})

	t.Run("invalid json", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".agents"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".agents", "models.json"),
			[]byte(`{not valid json`), 0o644))
		withChdir(t, dir)

		_, err := loadModelsConfig()
		require.Error(t, err)
	})
}

func TestAPIKeyFallbackFor(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic")

	t.Run("openai protocol", func(t *testing.T) {
		require.Equal(t, "sk-openai", apiKeyFallbackFor(types.ModelProfile{Protocol: "openai"}))
	})
	t.Run("anthropic protocol", func(t *testing.T) {
		require.Equal(t, "sk-anthropic", apiKeyFallbackFor(types.ModelProfile{Protocol: "anthropic"}))
	})
	t.Run("anthropic provider without protocol", func(t *testing.T) {
		require.Equal(t, "sk-anthropic", apiKeyFallbackFor(types.ModelProfile{Provider: "anthropic"}))
	})
	t.Run("anthropic_messages alias", func(t *testing.T) {
		require.Equal(t, "sk-anthropic", apiKeyFallbackFor(types.ModelProfile{Protocol: "anthropic_messages"}))
	})
	t.Run("empty protocol defaults to openai", func(t *testing.T) {
		require.Equal(t, "sk-openai", apiKeyFallbackFor(types.ModelProfile{}))
	})
}

// sessionFlowRunner stub FlowRuntime for exercising the Run method branches.
type runnerFlowRuntimeStub struct {
	startCalls   int
	handleCalls  int
	startResult  types.FlowTurnResult
	handleResult types.FlowTurnResult
	err          error
}

func (s *runnerFlowRuntimeStub) Start(context.Context, types.StartFlowRequest) (types.FlowTurnResult, error) {
	s.startCalls++
	return s.startResult, s.err
}

func (s *runnerFlowRuntimeStub) HandleInput(context.Context, string, string) (types.FlowTurnResult, error) {
	s.handleCalls++
	return s.handleResult, s.err
}

func (s *runnerFlowRuntimeStub) Resume(context.Context, string, string) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, nil
}

func (s *runnerFlowRuntimeStub) Cancel(context.Context, string) error { return nil }

func (s *runnerFlowRuntimeStub) Status(context.Context, string) (types.FlowSession, error) {
	return types.FlowSession{}, nil
}

func TestSessionFlowRunnerRun(t *testing.T) {
	newTurnResult := func(sessionID string, status types.SessionStatus) types.FlowTurnResult {
		return types.FlowTurnResult{
			Session: types.FlowSession{ID: sessionID, Status: status},
			TeamResults: []types.TeamTurnResult{
				{Turn: types.TeamTurn{TeamID: "team-1"}, Reply: "hello", Usage: types.TokenUsage{TotalTokens: 30}},
			},
		}
	}

	t.Run("start branch when no session", func(t *testing.T) {
		stub := &runnerFlowRuntimeStub{
			startResult: newTurnResult("fs-1", types.SessionWaitingInput),
		}
		runner := &sessionFlowRunner{runtime: stub}
		res, err := runner.Run(context.Background(), "hello")
		require.NoError(t, err)
		require.Equal(t, 1, stub.startCalls)
		require.Equal(t, 0, stub.handleCalls)
		require.Equal(t, "fs-1", runner.sessionID)
		require.Equal(t, types.SessionWaitingInput, res.Status)
		require.Len(t, res.Teams, 1)
		require.Equal(t, "team-1", res.Teams[0].TeamID)
		require.Equal(t, 30, res.Usage.TotalTokens)
	})

	t.Run("handle branch when session exists", func(t *testing.T) {
		stub := &runnerFlowRuntimeStub{
			handleResult: newTurnResult("fs-1", types.SessionWaitingInput),
		}
		runner := &sessionFlowRunner{runtime: stub, sessionID: "fs-1"}
		res, err := runner.Run(context.Background(), "continue")
		require.NoError(t, err)
		require.Equal(t, 0, stub.startCalls)
		require.Equal(t, 1, stub.handleCalls)
		require.Equal(t, "fs-1", runner.sessionID)
		require.Equal(t, types.SessionWaitingInput, res.Status)
	})

	t.Run("session id updates after start", func(t *testing.T) {
		stub := &runnerFlowRuntimeStub{
			startResult: newTurnResult("fs-2", types.SessionRunning),
		}
		runner := &sessionFlowRunner{runtime: stub}
		_, err := runner.Run(context.Background(), "hello")
		require.NoError(t, err)
		require.Equal(t, "fs-2", runner.sessionID)

		// Second call should now use the handle branch.
		_, err = runner.Run(context.Background(), "again")
		require.NoError(t, err)
		require.Equal(t, 1, stub.startCalls)
		require.Equal(t, 1, stub.handleCalls)
	})
}

func TestWritePromptResult(t *testing.T) {
	newResult := func(usages ...types.TokenUsage) types.FlowTurnResult {
		teamResults := make([]types.TeamTurnResult, 0, len(usages))
		for _, usage := range usages {
			teamResults = append(teamResults, types.TeamTurnResult{Usage: usage})
		}
		return types.FlowTurnResult{
			Session:     types.FlowSession{ID: "fs-1", Status: types.SessionWaitingInput},
			Reply:       "done",
			Records:     []types.SharedRecord{{Name: "DiagnosisReport", Summary: "found one issue"}},
			TeamResults: teamResults,
		}
	}

	t.Run("reports aggregated token usage", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, writePromptResult(&buf, "test-flow", "hy3", newResult(
			types.TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
			types.TokenUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		)))

		out := buf.String()
		require.Contains(t, out, "Flow: test-flow\n")
		require.Contains(t, out, "Model: hy3\n")
		require.Contains(t, out, "FlowSession: fs-1\n")
		require.Contains(t, out, "Status: waiting_input\n")
		// Same aggregation the JSON-RPC result uses: 30 + 3.
		require.Contains(t, out, "Tokens: 33 (prompt 11, completion 22)\n")
		require.Contains(t, out, "\ndone\n")
		require.Contains(t, out, "[DiagnosisReport] found one issue")
	})

	t.Run("omits token line when no usage recorded", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, writePromptResult(&buf, "test-flow", "hy3", newResult()))
		require.NotContains(t, buf.String(), "Tokens:")
	})
}

// withChdir runs fn in dir, restoring the original working directory.
func withChdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(old))
	})
}

// setupMinimalProject creates a temp dir with a minimal flow/team/agent config
// plus models.json, so buildProvider can run without a real project.
func setupMinimalProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "flows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "teams"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "agents"), 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "flows", "default.yml"), []byte(`
id: test-flow
entry: default
teams:
  default:
    team: default-team
    coordinator: true
    inputs:
      user_message: true
`), 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "teams", "default.yml"), []byte(`
id: default-team
calls:
  assistant:
    type: agent
    agent: default-assistant
    output:
      record: Reply
`), 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "agents", "default-assistant.md"), []byte(`---
name: default-assistant
persona:
  role: assistant
  goal: answer
model:
  provider: openai
  model: gpt-4o-mini
---
You answer.
`), 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "models.json"), []byte(`{
  "model": "gpt-4o",
  "models": [
    {"name": "gpt-4o", "provider": "openai", "api_key": "sk-default", "base_url": "https://api.openai.com/v1"},
    {"name": "claude-sonnet", "provider": "anthropic", "api_key": "sk-claude", "base_url": "https://api.anthropic.com"}
  ]
}`), 0o644))

	return root
}

// TestBuildProviderModelOverride verifies --model override wins over the
// models.json "model" field.
func TestBuildProviderModelOverride(t *testing.T) {
	root := setupMinimalProject(t)
	withChdir(t, root)

	_, provider, err := buildProvider(context.Background(), ".agents/flows/default.yml", "claude-sonnet")
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, "claude-sonnet", provider.DefaultModel())
}

// TestBuildProviderNoModelOverrideUsesConfig verifies that without --model the
// models.json "model" field is used.
func TestBuildProviderNoModelOverrideUsesConfig(t *testing.T) {
	root := setupMinimalProject(t)
	withChdir(t, root)

	_, provider, err := buildProvider(context.Background(), ".agents/flows/default.yml", "")
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, "gpt-4o", provider.DefaultModel())
}

func TestApplyMaxRounds(t *testing.T) {
	t.Run("positive overrides", func(t *testing.T) {
		limits := types.RuntimeLimits{MaxAgentRounds: 200}
		applyMaxRounds(&limits, 5)
		require.Equal(t, 5, limits.MaxAgentRounds)
	})

	t.Run("zero keeps config", func(t *testing.T) {
		limits := types.RuntimeLimits{MaxAgentRounds: 200}
		applyMaxRounds(&limits, 0)
		require.Equal(t, 200, limits.MaxAgentRounds)
	})

	t.Run("negative keeps config", func(t *testing.T) {
		limits := types.RuntimeLimits{MaxAgentRounds: 200}
		applyMaxRounds(&limits, -1)
		require.Equal(t, 200, limits.MaxAgentRounds)
	})

	t.Run("nil limits is noop", func(t *testing.T) {
		applyMaxRounds(nil, 5)
	})
}
