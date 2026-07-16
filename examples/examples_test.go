package examples_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/heron-ai/heron-engine/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodeReviewExample_LoadConfig verifies the code-review example loads correctly.
func TestCodeReviewExample_LoadConfig(t *testing.T) {
	loader := config.NewConfigLoader("../examples/code-review/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/code_review.yml",
		Variables: map[string]string{
			"LLM_PROVIDER": "openai",
			"LLM_MODEL":    "gpt-4o-mini",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, runReq)

	// Verify flow
	assert.Equal(t, "code_review_flow", runReq.Flow.Name)
	assert.Len(t, runReq.Flow.Stages, 1)
	assert.Equal(t, "review", runReq.Flow.Stages[0].Name)
	assert.Equal(t, "review_team", runReq.Flow.Stages[0].Team)

	// Verify team
	team, ok := runReq.Teams["review_team"]
	require.True(t, ok, "review_team should exist")
	assert.Len(t, team.Stages, 2)
	// Stage 1: parallel review
	assert.Equal(t, "parallel", team.Stages[0].Process)
	assert.Len(t, team.Stages[0].Tasks, 2)
	assert.Equal(t, "security_review", team.Stages[0].Tasks[0].Name)
	assert.Equal(t, "security-reviewer", team.Stages[0].Tasks[0].Agent)
	assert.Equal(t, "performance_review", team.Stages[0].Tasks[1].Name)
	assert.Equal(t, "performance-reviewer", team.Stages[0].Tasks[1].Agent)
	// Stage 2: sequential aggregate
	assert.Equal(t, "sequential", team.Stages[1].Process)
	assert.Len(t, team.Stages[1].Tasks, 1)
	assert.Equal(t, "aggregate", team.Stages[1].Tasks[0].Name)
	assert.Equal(t, "lead-reviewer", team.Stages[1].Tasks[0].Agent)

	// Verify agents
	assert.Len(t, runReq.Agents, 3)
	for _, name := range []string{"security-reviewer", "performance-reviewer", "lead-reviewer"} {
		agent, ok := runReq.Agents[name]
		require.True(t, ok, "agent %s should exist", name)
		assert.NotEmpty(t, agent.Body, "agent %s should have body", name)
		assert.NotEmpty(t, agent.Persona.Role, "agent %s should have role", name)
		assert.NotEmpty(t, agent.Persona.Goal, "agent %s should have goal", name)
	}

	// Verify security reviewer config
	secReviewer := runReq.Agents["security-reviewer"]
	assert.Equal(t, 0.3, secReviewer.Model.Temperature)
	assert.Equal(t, 3, secReviewer.Loop.MaxRounds)
	assert.Contains(t, secReviewer.Tools.Builtin, "Read")
	assert.Contains(t, secReviewer.Tools.Builtin, "Grep")

	// Verify lead reviewer config
	leadReviewer := runReq.Agents["lead-reviewer"]
	assert.Equal(t, 0.5, leadReviewer.Model.Temperature)
	assert.Equal(t, 3072, leadReviewer.Model.MaxTokens)

	// Verify rules
	assert.Len(t, runReq.Rules, 2)

	// Verify knowledge
	assert.Len(t, runReq.Knowledge, 1)
	assert.Contains(t, runReq.Knowledge[0].Content, "Go 安全最佳实践")
	assert.Contains(t, runReq.Knowledge[0].Content, "Go 性能最佳实践")
}

// TestSimpleQAExample_LoadConfig verifies the simple-qa example loads correctly.
func TestSimpleQAExample_LoadConfig(t *testing.T) {
	loader := config.NewConfigLoader("../examples/simple-qa/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/default.yml",
	})
	require.NoError(t, err)
	require.NotNil(t, runReq)

	assert.Equal(t, "default_flow", runReq.Flow.Name)
	assert.Len(t, runReq.Flow.Stages, 1)
	assert.Equal(t, "qa", runReq.Flow.Stages[0].Name)

	team, ok := runReq.Teams["qa_team"]
	require.True(t, ok)
	assert.Len(t, team.Stages, 1)
	assert.Len(t, team.Stages[0].Tasks, 1)
	assert.Equal(t, "assistant", team.Stages[0].Tasks[0].Agent)

	agent, ok := runReq.Agents["assistant"]
	require.True(t, ok)
	assert.NotEmpty(t, agent.Body)

	assert.Len(t, runReq.Rules, 2)
}

// TestNovelRPExample_LoadConfig verifies the novel-rp example loads correctly.
func TestNovelRPExample_LoadConfig(t *testing.T) {
	loader := config.NewConfigLoader("../examples/novel-rp/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/flow.yml",
	})
	require.NoError(t, err)
	require.NotNil(t, runReq)

	assert.Equal(t, "novel_story", runReq.Flow.Name)
	assert.Len(t, runReq.Flow.Stages, 4)
	assert.Equal(t, "opening", runReq.Flow.Stages[0].Name)
	assert.Equal(t, "development", runReq.Flow.Stages[1].Name)
	assert.Equal(t, "climax", runReq.Flow.Stages[2].Name)
	assert.Equal(t, "ending", runReq.Flow.Stages[3].Name)

	// All 4 stages use the same team
	for _, stage := range runReq.Flow.Stages {
		assert.Equal(t, "story_team", stage.Team)
	}

	team, ok := runReq.Teams["story_team"]
	require.True(t, ok)
	assert.Len(t, team.Stages, 2)
	assert.Equal(t, "parallel", team.Stages[0].Process)
	assert.Equal(t, "sequential", team.Stages[1].Process)

	// 3 agents: hero, villain, narrator
	assert.Len(t, runReq.Agents, 3)
	for _, name := range []string{"hero", "villain", "narrator"} {
		agent, ok := runReq.Agents[name]
		require.True(t, ok, "agent %s should exist", name)
		assert.NotEmpty(t, agent.Body)
	}

	assert.Len(t, runReq.Knowledge, 1)
	assert.Contains(t, runReq.Knowledge[0].Content, "血月")
}

// TestBlogWriterExample_LoadConfig verifies the blog-writer example loads correctly.
func TestBlogWriterExample_LoadConfig(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
		Variables: map[string]string{
			"LLM_PROVIDER": "openai",
			"LLM_MODEL":    "gpt-4o-mini",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, runReq)

	assert.Equal(t, "blog_writer_flow", runReq.Flow.Name)
	assert.Len(t, runReq.Flow.Stages, 3)
	assert.Equal(t, "research_stage", runReq.Flow.Stages[0].Name)
	assert.Equal(t, "research_team", runReq.Flow.Stages[0].Team)
	assert.Equal(t, "writing_stage", runReq.Flow.Stages[1].Name)
	assert.Equal(t, "writing_team", runReq.Flow.Stages[1].Team)
	assert.Equal(t, "review_stage", runReq.Flow.Stages[2].Name)
	assert.Equal(t, "review_team", runReq.Flow.Stages[2].Team)

	// Verify signal routing
	assert.NotNil(t, runReq.Flow.Stages[0].OnSignal.Continue)
	assert.Equal(t, "writing_stage", *runReq.Flow.Stages[0].OnSignal.Continue)
	assert.NotNil(t, runReq.Flow.Stages[1].OnSignal.Continue)
	assert.Equal(t, "review_stage", *runReq.Flow.Stages[1].OnSignal.Continue)
	assert.Nil(t, runReq.Flow.Stages[2].OnSignal.Continue)

	// Verify research team has parallel stage
	researchTeam, ok := runReq.Teams["research_team"]
	require.True(t, ok)
	assert.Len(t, researchTeam.Stages, 1)
	assert.Equal(t, "parallel", researchTeam.Stages[0].Process)
	assert.Len(t, researchTeam.Stages[0].Tasks, 2)

	// Verify writing team has sequential stage
	writingTeam, ok := runReq.Teams["writing_team"]
	require.True(t, ok)
	assert.Len(t, writingTeam.Stages, 1)
	assert.Equal(t, "sequential", writingTeam.Stages[0].Process)
	assert.Len(t, writingTeam.Stages[0].Tasks, 1)

	// Verify review team has sequential stage
	reviewTeam, ok := runReq.Teams["review_team"]
	require.True(t, ok)
	assert.Len(t, reviewTeam.Stages, 1)
	assert.Equal(t, "sequential", reviewTeam.Stages[0].Process)
	assert.Len(t, reviewTeam.Stages[0].Tasks, 1)

	// Verify all 4 agents exist with proper configuration
	assert.Len(t, runReq.Agents, 4)
	for _, name := range []string{"researcher", "planner", "writer", "editor"} {
		agent, ok := runReq.Agents[name]
		require.True(t, ok, "agent %s should exist", name)
		assert.NotEmpty(t, agent.Body, "agent %s should have body content", name)
		assert.NotEmpty(t, agent.Persona.Role, "agent %s should have a role", name)
		assert.NotEmpty(t, agent.Persona.Goal, "agent %s should have a goal", name)
	}

	// Researcher should have lower temperature for factual accuracy
	researcher := runReq.Agents["researcher"]
	assert.Equal(t, 0.3, researcher.Model.Temperature)
	assert.Contains(t, researcher.Tools.Builtin, "Grep")
	assert.Len(t, researcher.Tools.Builtin, 6, "researcher should have all 6 builtin tools")

	// Writer should have higher temperature for creativity
	writer := runReq.Agents["writer"]
	assert.Equal(t, 0.7, writer.Model.Temperature)
	assert.Greater(t, writer.Model.MaxTokens, 2048, "writer needs large context window")
	assert.Len(t, writer.Tools.Builtin, 6, "writer should have all 6 builtin tools")

	// Planner should have all tools
	planner := runReq.Agents["planner"]
	assert.Contains(t, planner.Tools.Builtin, "Write")
	assert.Len(t, planner.Tools.Builtin, 6, "planner should have all 6 builtin tools")

	// Editor should have all tools
	editor := runReq.Agents["editor"]
	assert.Len(t, editor.Tools.Builtin, 6, "editor should have all 6 builtin tools")

	// Verify rules (3 global rules + 4 agent-private rules = 7 total)
	assert.Len(t, runReq.Rules, 7)

	// Verify task descriptions contain template variables
	parallelTasks := researchTeam.Stages[0].Tasks
	assert.Contains(t, parallelTasks[0].Description, "{{.Input}}", "research task should reference input")
	assert.Contains(t, parallelTasks[1].Description, "{{.Input}}", "plan task should reference input")
}

func TestBlogWriterExample_AllAgentsHaveValidConfig(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
		Variables: map[string]string{
			"LLM_PROVIDER": "openai",
			"LLM_MODEL":    "gpt-4o-mini",
		},
	})
	require.NoError(t, err)

	for name, agent := range runReq.Agents {
		t.Run(name, func(t *testing.T) {
			assert.NotEmpty(t, agent.Name)
			assert.NotEmpty(t, agent.Persona.Role)
			assert.NotEmpty(t, agent.Model.Model)
			assert.Greater(t, agent.Loop.MaxRounds, 0, "agent %s should have max rounds", name)
			assert.Greater(t, agent.Model.MaxTokens, 0, "agent %s should have max tokens", name)
		})
	}
}

func TestBlogWriterExample_TeamStructureIsCorrect(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
		Variables: map[string]string{
			"LLM_PROVIDER": "openai",
			"LLM_MODEL":    "gpt-4o-mini",
		},
	})
	require.NoError(t, err)

	// Verify 3 teams exist
	teamNames := []string{"research_team", "writing_team", "review_team"}
	for _, name := range teamNames {
		team, ok := runReq.Teams[name]
		require.True(t, ok, "team %s should exist", name)
		assert.NotEmpty(t, team.Stages, "team %s should have stages", name)
	}

	// Research team: parallel stage with researcher + planner
	researchTeam := runReq.Teams["research_team"]
	assert.Equal(t, "parallel", researchTeam.Stages[0].Process)
	assert.Len(t, researchTeam.Stages[0].Tasks, 2)
	assert.Equal(t, "researcher", researchTeam.Stages[0].Tasks[0].Agent)
	assert.Equal(t, "planner", researchTeam.Stages[0].Tasks[1].Agent)
	assert.NotEqual(t, researchTeam.Stages[0].Tasks[0].Agent, researchTeam.Stages[0].Tasks[1].Agent, "parallel tasks should use different agents")

	// Writing team: sequential stage with writer
	writingTeam := runReq.Teams["writing_team"]
	assert.Equal(t, "sequential", writingTeam.Stages[0].Process)
	assert.Len(t, writingTeam.Stages[0].Tasks, 1)
	assert.Equal(t, "writer", writingTeam.Stages[0].Tasks[0].Agent)

	// Review team: sequential stage with editor
	reviewTeam := runReq.Teams["review_team"]
	assert.Equal(t, "sequential", reviewTeam.Stages[0].Process)
	assert.Len(t, reviewTeam.Stages[0].Tasks, 1)
	assert.Equal(t, "editor", reviewTeam.Stages[0].Tasks[0].Agent)

	// Verify config loader validates this correctly
	assert.NoError(t, loader.Validate(runReq))
}

func TestBlogWriterExample_AllToolsConfigured(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	expectedTools := []string{"Read", "Write", "Grep", "Glob", "TodoWrite", "TodoRead"}

	for name, agent := range runReq.Agents {
		t.Run(name, func(t *testing.T) {
			assert.Len(t, agent.Tools.Builtin, 6, "agent %s should have all 6 builtin tools", name)
			for _, tool := range expectedTools {
				assert.Contains(t, agent.Tools.Builtin, tool, "agent %s should have %s tool", name, tool)
			}
		})
	}
}

func TestBlogWriterExample_StructuredOutput(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	for name, agent := range runReq.Agents {
		t.Run(name, func(t *testing.T) {
			require.NotNil(t, agent.Structured, "agent %s should have structured output config", name)
			assert.Equal(t, "json", agent.Structured.Type, "agent %s structured output should be json", name)
			assert.NotEmpty(t, agent.Structured.Schema, "agent %s should have schema fields", name)

			// Verify required fields exist
			schema := agent.Structured.Schema
			for key, val := range schema {
				if fieldMap, ok := val.(map[string]interface{}); ok {
					if required, ok := fieldMap["required"]; ok {
						if requiredBool, ok := required.(bool); ok && requiredBool {
							assert.NotEmpty(t, key, "required field key should not be empty")
						}
					}
				}
			}
		})
	}
}

func TestBlogWriterExample_Hooks(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	// Researcher should have 4 hooks
	researcher := runReq.Agents["researcher"]
	assert.Len(t, researcher.Hooks, 4, "researcher should have 4 hooks")

	hookEvents := make(map[string]bool)
	for _, h := range researcher.Hooks {
		hookEvents[h.Event] = true
		assert.NotEmpty(t, h.Command, "hook command should not be empty")
	}
	assert.True(t, hookEvents["on_start"], "should have on_start hook")
	assert.True(t, hookEvents["on_end"], "should have on_end hook")
	assert.True(t, hookEvents["on_tool_start"], "should have on_tool_start hook")
	assert.True(t, hookEvents["on_error"], "should have on_error hook")

	// Planner should have hooks
	planner := runReq.Agents["planner"]
	assert.GreaterOrEqual(t, len(planner.Hooks), 2, "planner should have at least 2 hooks")

	// Writer should have hooks
	writer := runReq.Agents["writer"]
	assert.GreaterOrEqual(t, len(writer.Hooks), 3, "writer should have at least 3 hooks")

	// Editor should have hooks
	editor := runReq.Agents["editor"]
	assert.GreaterOrEqual(t, len(editor.Hooks), 2, "editor should have at least 2 hooks")
}

func TestBlogWriterExample_Handoff(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	// Planner should have handoff to writer
	planner := runReq.Agents["planner"]
	assert.Contains(t, planner.Handoffs, "writer", "planner should be able to handoff to writer")

	// Researcher and writer should not have handoffs
	researcher := runReq.Agents["researcher"]
	assert.Empty(t, researcher.Handoffs, "researcher should not have handoffs")

	writer := runReq.Agents["writer"]
	assert.Empty(t, writer.Handoffs, "writer should not have handoffs")
}

func TestBlogWriterExample_KnowledgeBase(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	assert.Len(t, runReq.Knowledge, 6, "should have 6 knowledge entries (2 global + 4 private)")

	// Check SEO guide
	foundSEO := false
	foundStyle := false
	for _, entry := range runReq.Knowledge {
		assert.NotEmpty(t, entry.Content, "knowledge entry should have content")
		assert.NotEmpty(t, entry.Keys, "knowledge entry should have keys")
		// Note: some entries are agent-scoped, not all-scoped
		if containsStr(entry.Content, "SEO") || containsStr(entry.Content, "标题优化") {
			foundSEO = true
		}
		if containsStr(entry.Content, "写作风格") {
			foundStyle = true
			assert.Contains(t, entry.Content, "语气定位")
		}
	}
	assert.True(t, foundSEO, "should have SEO knowledge entry")
	assert.True(t, foundStyle, "should have writing style knowledge entry")
}

func TestBlogWriterExample_Guardrails(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	assert.Len(t, runReq.Rules, 7, "should have 7 rules (3 global + 4 agent-private)")

	foundHard := false
	for _, rule := range runReq.Rules {
		assert.NotEmpty(t, rule.Content, "rule should have content")
		if rule.Type == "hard" {
			foundHard = true
		}
	}
	assert.True(t, foundHard, "should have at least one hard rule")
}

func TestBlogWriterExample_TaskTemplates(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	researchTeam := runReq.Teams["research_team"]

	// Parallel stage tasks should reference {{.Input}} for variable injection
	for _, task := range researchTeam.Stages[0].Tasks {
		assert.Contains(t, task.Description, "{{.Input}}",
			"task %s should reference {{.Input}} for variable injection", task.Name)
	}

	// Writing team task should reference previous stage results
	writingTeam := runReq.Teams["writing_team"]
	writingTask := writingTeam.Stages[0].Tasks[0]
	assert.Contains(t, writingTask.Description, "research", "writer should reference research")

	// Review team task should reference review context
	reviewTeam := runReq.Teams["review_team"]
	reviewTask := reviewTeam.Stages[0].Tasks[0]
	assert.Contains(t, reviewTask.Description, "Review", "editor should reference review")
}

func TestBlogWriterExample_LoopConfigs(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	for name, agent := range runReq.Agents {
		t.Run(name, func(t *testing.T) {
			expectedRounds := 5
			if name == "editor" {
				expectedRounds = 3 // Editor has fewer rounds for review focus
			}
			assert.Equal(t, expectedRounds, agent.Loop.MaxRounds, "agent %s should have correct max rounds", name)
			assert.Equal(t, "sequential", agent.Loop.ToolMode, "agent %s should use sequential tool mode", name)
			assert.Equal(t, "120s", agent.Loop.Timeout, "agent %s should have 120s timeout", name)
		})
	}
}

func TestBlogWriterExample_HITL(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	// Researcher has HITL disabled
	researcher := runReq.Agents["researcher"]
	require.NotNil(t, researcher.HITL, "researcher should have HITL config")
	assert.False(t, researcher.HITL.Enabled, "researcher HITL should be disabled")

	// Planner and writer also have HITL config (all disabled)
	planner := runReq.Agents["planner"]
	require.NotNil(t, planner.HITL, "planner should have HITL config")
	assert.False(t, planner.HITL.Enabled, "planner HITL should be disabled")

	writer := runReq.Agents["writer"]
	require.NotNil(t, writer.HITL, "writer should have HITL config")
	assert.False(t, writer.HITL.Enabled, "writer HITL should be disabled")

	// Editor also has HITL config (disabled)
	editor := runReq.Agents["editor"]
	require.NotNil(t, editor.HITL, "editor should have HITL config")
	assert.False(t, editor.HITL.Enabled, "editor HITL should be disabled")
}

func TestBlogWriterExample_AgentPrivateKnowledge(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	// Check for agent-scoped knowledge entries
	agentScopedCount := 0
	for _, entry := range runReq.Knowledge {
		if entry.Scope.Type == "agents" {
			agentScopedCount++
			// Verify the scope includes the correct agent
			assert.NotEmpty(t, entry.Scope.Agents, "agent-scoped knowledge should list agents")
		}
	}
	assert.Equal(t, 4, agentScopedCount, "should have 4 agent-private knowledge entries")

	// Check for agent-scoped rules
	agentRuleCount := 0
	for _, rule := range runReq.Rules {
		if rule.Scope.Type == "agents" {
			agentRuleCount++
			assert.NotEmpty(t, rule.Scope.Agents, "agent-scoped rule should list agents")
		}
	}
	assert.Equal(t, 4, agentRuleCount, "should have 4 agent-private rules")
}

func TestBlogWriterExample_AllConfigFields(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	for name, agent := range runReq.Agents {
		t.Run(name, func(t *testing.T) {
			// Persona fields
			assert.NotEmpty(t, agent.Persona.Role)
			assert.NotEmpty(t, agent.Persona.Goal)
			assert.NotEmpty(t, agent.Persona.Backstory)

			// Model fields (all populated)
			assert.NotEmpty(t, agent.Model.Model)
			assert.Greater(t, agent.Model.Temperature, 0.0)
			assert.Greater(t, agent.Model.MaxTokens, 0)
			assert.NotEmpty(t, agent.Model.APIKey)
			assert.NotEmpty(t, agent.Model.BaseURL)

			// Tools
			assert.NotEmpty(t, agent.Tools.Builtin)
			assert.NotNil(t, agent.Tools.Custom)
			assert.NotNil(t, agent.Tools.MCP)

			// Skills
			assert.NotEmpty(t, agent.Skills, "agent %s should reference skills", name)

			// Knowledge references
			assert.NotEmpty(t, agent.Knowledge, "agent %s should reference global knowledge", name)

			// Rules references
			assert.NotEmpty(t, agent.Rules, "agent %s should reference global rules", name)

			// Loop
			assert.Greater(t, agent.Loop.MaxRounds, 0)
			assert.NotEmpty(t, agent.Loop.ToolMode)
			assert.NotEmpty(t, agent.Loop.Timeout)

			// Structured output
			assert.NotNil(t, agent.Structured)
			assert.Equal(t, "json", agent.Structured.Type)
			assert.NotEmpty(t, agent.Structured.Schema)

			// HITL
			assert.NotNil(t, agent.HITL)
			assert.NotEmpty(t, agent.HITL.Timeout)

			// Hooks
			assert.NotEmpty(t, agent.Hooks)
			for _, h := range agent.Hooks {
				assert.NotEmpty(t, h.Event)
				assert.NotEmpty(t, h.Command)
				assert.NotEmpty(t, h.Timeout)
			}

			// Body
			assert.NotEmpty(t, agent.Body)
		})
	}
}

func TestBlogWriterExample_SettingsConfig(t *testing.T) {
	// Verify settings.json exists and is valid
	data, err := os.ReadFile("../examples/blog-writer/.agents/settings.json")
	require.NoError(t, err)

	var settings map[string]interface{}
	err = json.Unmarshal(data, &settings)
	require.NoError(t, err)

	// Check required sections
	assert.Contains(t, settings, "flow")
	assert.Contains(t, settings, "team")
	assert.Contains(t, settings, "agent")
	assert.Contains(t, settings, "logging")
	assert.Contains(t, settings, "observability")
	assert.Contains(t, settings, "paths")
}

func TestBlogWriterExample_ModelsConfig(t *testing.T) {
	// Verify models.json exists and is valid
	data, err := os.ReadFile("../examples/blog-writer/.agents/models.json")
	require.NoError(t, err)

	var models map[string]interface{}
	err = json.Unmarshal(data, &models)
	require.NoError(t, err)

	assert.Contains(t, models, "model")
	assert.Contains(t, models, "models")

	modelList, ok := models["models"].([]interface{})
	require.True(t, ok)
	assert.GreaterOrEqual(t, len(modelList), 2, "should have at least 2 models")

	// Default model should be deepseek-v4-flash
	assert.Equal(t, "deepseek-v4-flash", models["model"])
}

func TestBlogWriterExample_SettingsLoaded(t *testing.T) {
	// Verify the config loader can find settings.json
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	// After loading flow, settings should also be accessible
	assert.NotNil(t, runReq)
}

func TestBlogWriterExample_SkillsDirectory(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	// Verify skill references are valid
	allSkillNames := make(map[string]bool)
	for _, agent := range runReq.Agents {
		for _, skillName := range agent.Skills {
			allSkillNames[skillName] = true
		}
	}

	expectedSkills := []string{"deep_research", "content_planning", "blog_writing", "content_review"}
	for _, expected := range expectedSkills {
		assert.True(t, allSkillNames[expected], "skill %s should be referenced by at least one agent", expected)
	}

	// Verify skill files exist
	skillsDir := "../examples/blog-writer/.agents/skills"
	for _, skillName := range expectedSkills {
		skillFile := filepath.Join(skillsDir, skillName, "SKILL.md")
		_, err := os.Stat(skillFile)
		assert.NoError(t, err, "skill file %s should exist", skillFile)
	}
}

func TestBlogWriterExample_ResearcherHasMCP(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	researcher := runReq.Agents["researcher"]
	assert.Contains(t, researcher.Tools.MCP, "web_search", "researcher should have MCP web_search tool")
	assert.Contains(t, researcher.Tools.Custom, "search_knowledge", "researcher should have custom search_knowledge tool")
}

func TestBlogWriterExample_MultiTeamFlow(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	// Verify 3 stages with signal routing
	assert.Len(t, runReq.Flow.Stages, 3)

	// Stage 1: research → writing
	assert.Equal(t, "research_stage", runReq.Flow.Stages[0].Name)
	assert.Equal(t, "research_team", runReq.Flow.Stages[0].Team)
	assert.NotNil(t, runReq.Flow.Stages[0].OnSignal.Continue)
	assert.Equal(t, "writing_stage", *runReq.Flow.Stages[0].OnSignal.Continue)

	// Stage 2: writing → review
	assert.Equal(t, "writing_stage", runReq.Flow.Stages[1].Name)
	assert.Equal(t, "writing_team", runReq.Flow.Stages[1].Team)
	assert.Equal(t, "review_stage", *runReq.Flow.Stages[1].OnSignal.Continue)

	// Stage 3: review (terminal)
	assert.Equal(t, "review_stage", runReq.Flow.Stages[2].Name)
	assert.Equal(t, "review_team", runReq.Flow.Stages[2].Team)
	assert.Nil(t, runReq.Flow.Stages[2].OnSignal.Continue)
}

func TestBlogWriterExample_ThreeTeams(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	// Verify 3 teams exist
	teamNames := []string{"research_team", "writing_team", "review_team"}
	for _, name := range teamNames {
		team, ok := runReq.Teams[name]
		require.True(t, ok, "team %s should exist", name)
		assert.NotEmpty(t, team.Stages, "team %s should have stages", name)
	}

	// Research team: parallel
	researchTeam := runReq.Teams["research_team"]
	assert.Equal(t, "parallel", researchTeam.Stages[0].Process)
	assert.Len(t, researchTeam.Stages[0].Tasks, 2)

	// Writing team: sequential
	writingTeam := runReq.Teams["writing_team"]
	assert.Equal(t, "sequential", writingTeam.Stages[0].Process)
	assert.Len(t, writingTeam.Stages[0].Tasks, 1)

	// Review team: sequential
	reviewTeam := runReq.Teams["review_team"]
	assert.Equal(t, "sequential", reviewTeam.Stages[0].Process)
	assert.Len(t, reviewTeam.Stages[0].Tasks, 1)
}

func TestBlogWriterExample_FourAgents(t *testing.T) {
	loader := config.NewConfigLoader("../examples/blog-writer/")
	runReq, err := loader.Load(context.Background(), config.LoadRequest{
		FlowPath: ".agents/flows/blog.yml",
	})
	require.NoError(t, err)

	// Now 4 agents: researcher, planner, writer, editor
	assert.Len(t, runReq.Agents, 4)

	agentNames := []string{"researcher", "planner", "writer", "editor"}
	for _, name := range agentNames {
		agent, ok := runReq.Agents[name]
		require.True(t, ok, "agent %s should exist", name)
		assert.NotEmpty(t, agent.Body)
	}
}

// containsStr is a helper function that checks if s contains substr.
func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
