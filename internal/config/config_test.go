package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// writeFile creates a file with the given content in a directory
func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
}

func TestLoadCompleteConfig(t *testing.T) {
	dir := t.TempDir()

	// Create flow.yml
	require.NoError(t, writeFile(dir, "flow.yml", `
name: test-flow
loop_max_rounds: 3
stages:
  - name: analyze
    team: analysis-team
`))

	// Create teams directory and team config
	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "analysis.yml", `
name: analysis-team
stages:
  - process: sequential
    tasks:
      - name: analyze-task
        agent: analyzer
`))

	// Create agents directory and agent config
	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, writeFile(agentsDir, "analyzer.md", `---
name: analyzer
persona:
  role: code reviewer
  goal: find bugs
  backstory: experienced dev
model:
  provider: openai
  model: gpt-4
  temperature: 0.7
tools:
  builtin: []
loop:
  max_rounds: 5
  tool_mode: sequential
---
You are a code reviewer. Analyze the code carefully.
`))

	// Create rules directory
	rulesDir := filepath.Join(dir, "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0755))
	require.NoError(t, writeFile(rulesDir, "safety.md", `---
id: safety-rule
type: hard
scope:
  type: all
priority: 10
---
Never expose API keys in responses.
`))

	// Create knowledge directory
	knowledgeDir := filepath.Join(dir, "knowledge")
	require.NoError(t, os.MkdirAll(knowledgeDir, 0755))
	require.NoError(t, writeFile(knowledgeDir, "api-docs.md", `---
id: api-ref
keys: ["api", "reference"]
scope:
  type: all
---
API documentation content here.
`))

	loader := NewConfigLoader(dir)
	runReq, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.NoError(t, err)
	require.NotNil(t, runReq)

	assert.Equal(t, "test-flow", runReq.Flow.Name)
	assert.Equal(t, 3, runReq.Flow.LoopMaxRounds)
	assert.Len(t, runReq.Flow.Stages, 1)
	assert.Equal(t, "analyze", runReq.Flow.Stages[0].Name)
	assert.Equal(t, "analysis-team", runReq.Flow.Stages[0].Team)

	assert.Len(t, runReq.Teams, 1)
	assert.Contains(t, runReq.Teams, "analysis-team")
	assert.Len(t, runReq.Teams["analysis-team"].Stages, 1)
	assert.Len(t, runReq.Teams["analysis-team"].Stages[0].Tasks, 1)
	assert.Equal(t, "analyze-task", runReq.Teams["analysis-team"].Stages[0].Tasks[0].Name)
	assert.Equal(t, "analyzer", runReq.Teams["analysis-team"].Stages[0].Tasks[0].Agent)

	assert.Len(t, runReq.Agents, 1)
	assert.Contains(t, runReq.Agents, "analyzer")
	assert.Equal(t, "code reviewer", runReq.Agents["analyzer"].Persona.Role)
	assert.Equal(t, "You are a code reviewer. Analyze the code carefully.\n", runReq.Agents["analyzer"].Body)

	assert.Len(t, runReq.Rules, 1)
	assert.Equal(t, "safety-rule", runReq.Rules[0].ID)
	assert.Equal(t, "hard", runReq.Rules[0].Type)
	assert.Equal(t, "Never expose API keys in responses.\n", runReq.Rules[0].Content)

	assert.Len(t, runReq.Knowledge, 1)
	assert.Equal(t, "api-ref", runReq.Knowledge[0].ID)
	assert.Equal(t, "API documentation content here.\n", runReq.Knowledge[0].Content)
}

func TestLoadFlowWithMultipleStages(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: multi-stage-flow
loop_max_rounds: 5
stages:
  - name: plan
    team: planner
  - name: execute
    team: executor
  - name: review
    team: reviewer
`))

	// Create required teams
	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "planner.yml", `
name: planner
stages:
  - process: sequential
    tasks:
      - name: plan-task
        agent: plan-agent
`))
	require.NoError(t, writeFile(teamsDir, "executor.yml", `
name: executor
stages:
  - process: sequential
    tasks:
      - name: exec-task
        agent: exec-agent
`))
	require.NoError(t, writeFile(teamsDir, "reviewer.yml", `
name: reviewer
stages:
  - process: sequential
    tasks:
      - name: review-task
        agent: review-agent
`))

	// Create required agents
	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, writeFile(agentsDir, "plan-agent.md", formatAgentContent("plan-agent")))
	require.NoError(t, writeFile(agentsDir, "exec-agent.md", formatAgentContent("exec-agent")))
	require.NoError(t, writeFile(agentsDir, "review-agent.md", formatAgentContent("review-agent")))

	loader := NewConfigLoader(dir)
	runReq, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.NoError(t, err)

	assert.Len(t, runReq.Flow.Stages, 3)
	assert.Equal(t, "plan", runReq.Flow.Stages[0].Name)
	assert.Equal(t, "execute", runReq.Flow.Stages[1].Name)
	assert.Equal(t, "review", runReq.Flow.Stages[2].Name)
	assert.Len(t, runReq.Teams, 3)
	assert.Len(t, runReq.Agents, 3)
}

func formatAgentContent(name string) string {
	return `---
name: ` + name + `
persona:
  role: dev
  goal: work
  backstory: experienced
model:
  provider: openai
  model: gpt-4
  temperature: 0.7
tools:
  builtin: []
loop:
  max_rounds: 5
  tool_mode: sequential
---
Body content.
`
}

func TestLoadTeamWithMultipleStagesAndTasks(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: complex-flow
loop_max_rounds: 3
stages:
  - name: main
    team: main-team
`))

	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "main.yml", `
name: main-team
stages:
  - process: sequential
    tasks:
      - name: task1
        agent: agent1
      - name: task2
        agent: agent2
  - process: parallel
    tasks:
      - name: task3
        agent: agent3
      - name: task4
        agent: agent4
`))

	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	for _, name := range []string{"agent1", "agent2", "agent3", "agent4"} {
		require.NoError(t, writeFile(agentsDir, name+".md", formatAgentContent(name)))
	}

	loader := NewConfigLoader(dir)
	runReq, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.NoError(t, err)

	team := runReq.Teams["main-team"]
	assert.Equal(t, "main-team", team.Name)
	assert.Len(t, team.Stages, 2)

	assert.Equal(t, "sequential", team.Stages[0].Process)
	assert.Len(t, team.Stages[0].Tasks, 2)
	assert.Equal(t, "task1", team.Stages[0].Tasks[0].Name)
	assert.Equal(t, "agent1", team.Stages[0].Tasks[0].Agent)
	assert.Equal(t, "task2", team.Stages[0].Tasks[1].Name)
	assert.Equal(t, "agent2", team.Stages[0].Tasks[1].Agent)

	assert.Equal(t, "parallel", team.Stages[1].Process)
	assert.Len(t, team.Stages[1].Tasks, 2)
	assert.Equal(t, "task3", team.Stages[1].Tasks[0].Name)
	assert.Equal(t, "agent3", team.Stages[1].Tasks[0].Agent)
}

func TestValidationMissingFlowName(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: ""
loop_max_rounds: 1
stages:
  - name: stage1
    team: team1
`))

	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "team1.yml", `
name: team1
stages:
  - process: sequential
    tasks:
      - name: task1
        agent: agent1
`))

	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, writeFile(agentsDir, "agent1.md", formatAgentContent("agent1")))

	loader := NewConfigLoader(dir)
	_, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "flow name is required")
}

func TestValidationMissingStageName(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: test-flow
loop_max_rounds: 1
stages:
  - name: ""
    team: team1
`))

	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "team1.yml", `
name: team1
stages:
  - process: sequential
    tasks:
      - name: task1
        agent: agent1
`))

	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, writeFile(agentsDir, "agent1.md", formatAgentContent("agent1")))

	loader := NewConfigLoader(dir)
	_, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestValidationMissingTeam(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: test-flow
loop_max_rounds: 1
stages:
  - name: stage1
    team: nonexistent-team
`))

	loader := NewConfigLoader(dir)
	_, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestValidationMissingAgent(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: test-flow
loop_max_rounds: 1
stages:
  - name: stage1
    team: team1
`))

	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "team1.yml", `
name: team1
stages:
  - process: sequential
    tasks:
      - name: task1
        agent: nonexistent-agent
`))

	loader := NewConfigLoader(dir)
	_, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestLoadWithVariables(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: test-flow
loop_max_rounds: 1
stages:
  - name: stage1
    team: team1
`))

	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "team1.yml", `
name: team1
stages:
  - process: sequential
    tasks:
      - name: task1
        agent: agent1
`))

	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, writeFile(agentsDir, "agent1.md", formatAgentContent("agent1")))

	loader := NewConfigLoader(dir)
	runReq, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
		Variables: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	})
	require.NoError(t, err)

	assert.Len(t, runReq.Variables, 2)
	assert.Equal(t, "value1", runReq.Variables["key1"])
	assert.Equal(t, "value2", runReq.Variables["key2"])
}

func TestLoadWithOverrides(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: original-flow
loop_max_rounds: 1
stages:
  - name: stage1
    team: team1
`))

	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "team1.yml", `
name: team1
stages:
  - process: sequential
    tasks:
      - name: task1
        agent: agent1
`))

	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, writeFile(agentsDir, "agent1.md", formatAgentContent("agent1")))

	loader := NewConfigLoader(dir)

	// Override the flow name
	overrideFlow := types.FlowConfig{
		Name:          "overridden-flow",
		LoopMaxRounds: 10,
		Stages: []types.FlowStage{
			{Name: "overridden-stage", Team: "team1"},
		},
	}

	runReq, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
		Overrides: &RunOverrides{
			Flow: &overrideFlow,
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "overridden-flow", runReq.Flow.Name)
	assert.Equal(t, 10, runReq.Flow.LoopMaxRounds)
}

func TestLoadWithPrompts(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: test-flow
loop_max_rounds: 1
stages:
  - name: stage1
    team: team1
`))

	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "team1.yml", `
name: team1
stages:
  - process: sequential
    tasks:
      - name: task1
        agent: agent1
`))

	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, writeFile(agentsDir, "agent1.md", formatAgentContent("agent1")))

	promptsDir := filepath.Join(dir, "prompts")
	require.NoError(t, os.MkdirAll(promptsDir, 0755))
	require.NoError(t, writeFile(promptsDir, "greeting.md", "Hello, {{name}}!"))
	require.NoError(t, writeFile(promptsDir, "farewell.md", "Goodbye!"))

	loader := NewConfigLoader(dir)
	runReq, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.NoError(t, err)

	assert.Len(t, runReq.Prompts, 2)
	assert.Equal(t, "Hello, {{name}}!", runReq.Prompts["greeting"])
	assert.Equal(t, "Goodbye!", runReq.Prompts["farewell"])
}

func TestLoadAgentInSubdirectory(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: test-flow
loop_max_rounds: 1
stages:
  - name: stage1
    team: team1
`))

	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "team1.yml", `
name: team1
stages:
  - process: sequential
    tasks:
      - name: task1
        agent: sub-agent
`))

	// Create agent in a subdirectory with AGENT.md
	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	subAgentDir := filepath.Join(agentsDir, "sub-agent")
	require.NoError(t, os.MkdirAll(subAgentDir, 0755))
	require.NoError(t, writeFile(subAgentDir, "AGENT.md", formatAgentContent("sub-agent")))

	loader := NewConfigLoader(dir)
	runReq, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.NoError(t, err)

	assert.Len(t, runReq.Agents, 1)
	assert.Contains(t, runReq.Agents, "sub-agent")
}

func TestLoadNonexistentFlow(t *testing.T) {
	dir := t.TempDir()
	loader := NewConfigLoader(dir)

	_, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "nonexistent.yml"),
	})
	require.Error(t, err)
}

func TestValidationFlowWithoutStages(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: empty-flow
loop_max_rounds: 1
stages: []
`))

	loader := NewConfigLoader(dir)
	_, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must have at least one stage")
}

func TestValidationTeamMissingName(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: test-flow
loop_max_rounds: 1
stages:
  - name: stage1
    team: badteam
`))

	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "badteam.yml", `
name: badteam
stages:
  - process: sequential
    tasks:
      - name: task1
        agent: agent1
`))

	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, writeFile(agentsDir, "agent1.md", formatAgentContent("agent1")))

	loader := NewConfigLoader(dir)
	runReq, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.NoError(t, err)

	// Directly manipulate to trigger team name validation.
	// Set a team with empty name that is referenced by the flow.
	runReq.Teams["emptyteam"] = types.TeamConfig{Name: "", Stages: runReq.Teams["badteam"].Stages}
	delete(runReq.Teams, "badteam")
	runReq.Flow.Stages[0].Team = "emptyteam"

	err = loader.Validate(runReq)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestValidationTaskMissingName(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: test-flow
loop_max_rounds: 1
stages:
  - name: stage1
    team: team1
`))

	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "team1.yml", `
name: team1
stages:
  - process: sequential
    tasks:
      - name: ""
        agent: agent1
`))

	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, writeFile(agentsDir, "agent1.md", formatAgentContent("agent1")))

	loader := NewConfigLoader(dir)
	_, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestValidationTaskMissingAgent(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: test-flow
loop_max_rounds: 1
stages:
  - name: stage1
    team: team1
`))

	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "team1.yml", `
name: team1
stages:
  - process: sequential
    tasks:
      - name: task1
        agent: ""
`))

	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, writeFile(agentsDir, "agent1.md", formatAgentContent("agent1")))

	loader := NewConfigLoader(dir)
	_, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent is required")
}

func TestLoadConfigWithoutOptionalDirs(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeFile(dir, "flow.yml", `
name: minimal-flow
loop_max_rounds: 1
stages:
  - name: stage1
    team: team1
`))

	// Only create teams and agents (no rules, knowledge, prompts)
	teamsDir := filepath.Join(dir, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, writeFile(teamsDir, "team1.yml", `
name: team1
stages:
  - process: sequential
    tasks:
      - name: task1
        agent: agent1
`))

	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, writeFile(agentsDir, "agent1.md", formatAgentContent("agent1")))

	loader := NewConfigLoader(dir)
	runReq, err := loader.Load(context.Background(), LoadRequest{
		FlowPath: filepath.Join(dir, "flow.yml"),
	})
	require.NoError(t, err)

	assert.Empty(t, runReq.Rules)
	assert.Empty(t, runReq.Knowledge)
	assert.Empty(t, runReq.Prompts)
	assert.Empty(t, runReq.Variables)
}
