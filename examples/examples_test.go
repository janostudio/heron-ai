package examples_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/config"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func loadDefinitions(t *testing.T, example, flowPath string) *types.Definitions {
	t.Helper()

	root := filepath.Join("..", "examples", example)
	definitions, err := config.NewConfigLoader(root).LoadDefinitions(
		context.Background(),
		config.DefinitionsLoadRequest{
			FlowPath: flowPath,
		},
	)
	require.NoError(t, err)
	require.NotNil(t, definitions)
	return definitions
}

func TestSimpleQAExampleUsesNewRuntimeConfiguration(t *testing.T) {
	definitions := loadDefinitions(t, "simple-qa", ".agents/flows/default.yml")

	require.Equal(t, "default", definitions.Flow.ID)
	require.Equal(t, "default", definitions.Flow.EntryTeamID)
	require.Len(t, definitions.Flow.Teams, 1)
	require.True(t, definitions.Flow.Teams["default"].Coordinator)

	team := definitions.Teams["qa_team"]
	require.Equal(t, "qa_team", team.ID)
	require.Len(t, team.Calls, 1)
	require.True(t, team.Memory.Enabled)
	require.Equal(t, 20, team.Memory.MaxItems)
	require.Equal(t, 4000, team.Memory.MaxChars)
	require.True(t, team.Memory.InjectSummary)

	answer := team.Calls["answer"]
	require.Equal(t, types.CallAgent, answer.Type)
	require.Equal(t, "assistant", answer.AgentID)
	require.True(t, answer.Inputs.UserMessage)
	require.Equal(t, "Answer", answer.Output.Record)
	require.Contains(t, definitions.Agents, "assistant")
	require.Equal(t, []string{"qa-guide"}, definitions.Agents["assistant"].Knowledge)
}

func TestCodeReviewExampleUsesParallelCallsAndOutputContract(t *testing.T) {
	definitions := loadDefinitions(t, "code-review", ".agents/code_review.yml")

	require.Equal(t, "code_review_flow", definitions.Flow.ID)
	require.Equal(t, "review", definitions.Flow.EntryTeamID)
	require.True(t, definitions.Flow.Teams["review"].Coordinator)

	team := definitions.Teams["review_team"]
	require.Len(t, team.Calls, 3)

	require.Equal(t, types.CallAgent, team.Calls["security_review"].Type)
	require.Equal(t, types.CallAgent, team.Calls["performance_review"].Type)
	require.Equal(t, types.CallAgent, team.Calls["aggregate"].Type)
	require.ElementsMatch(t,
		[]string{"security_review", "performance_review"},
		team.Calls["aggregate"].DependsOn,
	)
	require.Equal(t, "CodeReviewReport", team.Output.Record)
	require.Equal(t, "aggregate", team.Output.From)
}

func TestBlogWriterExampleUsesTeamRoutingAndSharedRecords(t *testing.T) {
	definitions := loadDefinitions(t, "blog-writer", ".agents/flows/blog.yml")

	require.Equal(t, "blog_writer_flow", definitions.Flow.ID)
	require.Equal(t, "research", definitions.Flow.EntryTeamID)
	require.Len(t, definitions.Flow.Teams, 3)
	require.True(t, definitions.Flow.Teams["research"].Coordinator)
	require.Equal(t, []string{"writing"}, definitions.Flow.Teams["research"].OnProceed.Teams)
	require.Equal(t, []string{"review"}, definitions.Flow.Teams["writing"].OnProceed.Teams)
	require.Equal(t, types.NextComplete, definitions.Flow.Teams["review"].OnProceed.Action)

	research := definitions.Teams["research_team"]
	require.Len(t, research.Calls, 2)
	require.Equal(t, types.CallAgent, research.Calls["research_topic"].Type)
	require.Equal(t, types.CallAgent, research.Calls["plan_outline"].Type)
	require.Equal(t, "ResearchReport", research.Calls["research_topic"].Output.Record)
	require.Equal(t, "BlogOutline", research.Calls["plan_outline"].Output.Record)

	writing := definitions.Teams["writing_team"]
	require.Equal(t, []string{"research"}, definitions.Flow.Teams["writing"].DependsOn)
	require.Equal(t, "writer", writing.Calls["write_blog"].AgentID)
	require.Equal(t, "BlogDraft", writing.Calls["write_blog"].Output.Record)
	require.Len(t, writing.Calls["write_blog"].Inputs.Records, 2)

	review := definitions.Teams["review_team"]
	require.Equal(t, "editor", review.Calls["review_blog"].AgentID)
	require.Equal(t, "FinalBlog", review.Calls["review_blog"].Output.Record)

	require.Len(t, definitions.Agents, 4)
	for _, name := range []string{"researcher", "planner", "writer", "editor"} {
		agent, ok := definitions.Agents[name]
		require.True(t, ok, "agent %s should exist", name)
		require.NotEmpty(t, agent.Body)
		require.NotEmpty(t, agent.Persona.Role)
		require.NotEmpty(t, agent.Model.Model)
		require.Greater(t, agent.Loop.MaxRounds, 0)
	}
}

func TestNovelRPExampleUsesRepeatedTeamActivation(t *testing.T) {
	definitions := loadDefinitions(t, "novel-rp", ".agents/flows/flow.yml")

	require.Equal(t, "novel_story", definitions.Flow.ID)
	require.Equal(t, "opening", definitions.Flow.EntryTeamID)
	require.Len(t, definitions.Flow.Teams, 4)

	expected := []struct {
		name    string
		next    string
		depends []string
	}{
		{name: "opening", next: "development"},
		{name: "development", next: "climax", depends: []string{"opening"}},
		{name: "climax", next: "ending", depends: []string{"development"}},
		{name: "ending", depends: []string{"climax"}},
	}
	for _, item := range expected {
		binding := definitions.Flow.Teams[item.name]
		require.Equal(t, item.depends, binding.DependsOn, item.name)
		if item.next == "" {
			require.Equal(t, types.NextComplete, binding.OnProceed.Action, item.name)
		} else {
			require.Equal(t, []string{item.next}, binding.OnProceed.Teams, item.name)
		}
	}

	team := definitions.Teams["story_team"]
	require.Len(t, team.Calls, 3)
	require.ElementsMatch(t,
		[]string{"hero_act", "villain_act"},
		team.Calls["narrate"].DependsOn,
	)
	require.Equal(t, "StoryTurn", team.Output.Record)
	require.Equal(t, "narrate", team.Output.From)
	require.Len(t, definitions.Agents, 3)
}

func TestAutoBugfixGitignoreExampleUsesNativeAgentsSkillsScriptsAndDeterministicCommands(t *testing.T) {
	definitions := loadDefinitions(t, "auto-bugfix-gitignore", ".agents/flows/auto_bugfix.yml")

	require.Equal(t, "auto_bugfix_gitignore", definitions.Flow.ID)
	require.Equal(t, "default", definitions.Flow.EntryTeamID)
	require.Equal(t, 20, definitions.Limits.MaxTeamTurns)
	require.Equal(t, 20, definitions.Limits.MaxCallsPerTeamTurn)
	require.Equal(t, 200, definitions.Limits.MaxAgentRounds)

	require.ElementsMatch(t,
		[]string{"default", "diagnose", "challenge", "fix", "test", "review", "learn", "audit"},
		keys(definitions.Flow.Teams),
	)
	require.Len(t, definitions.Agents, 10)
	require.ElementsMatch(t,
		[]string{
			"root-cause-analysis-skill",
			"challenge-review",
			"code-fix",
			"verification",
			"code-review",
			"knowledge-learning",
			"gitignore-diagnostics",
			"workspace-exploration",
			"session-observation",
			"backend-test",
			"self-evolving",
			"code-review-skill",
			"fixture-maintenance",
		},
		keys(definitions.Skills),
	)
	require.Equal(t,
		[]string{
			"scripts/git_snapshot.sh",
			"scripts/verify_gitignore.sh",
			"scripts/check_project_scope.sh",
		},
		definitions.Skills["gitignore-diagnostics"].Scripts,
	)
	require.Equal(t,
		[]string{
			"scripts/learn.py",
			"scripts/skill_evolve.py",
		},
		definitions.Skills["self-evolving"].Scripts,
	)
	require.Contains(t, definitions.Rules, "safety")

	testTeam := definitions.Teams["test_team"]
	require.Equal(t, types.CallCommand, testTeam.Calls["check_ignore"].Type)
	require.Equal(t, types.ReplayIdempotent, testTeam.Calls["check_ignore"].Command.ReplayPolicy)
	require.Equal(t, "gitignore-check-${flow_turn_id}", testTeam.Calls["check_ignore"].Command.IdempotencyKey)
	require.Equal(t, types.ReplayIdempotent, testTeam.Calls["check_status"].Command.ReplayPolicy)
	require.Equal(t, "git-status-${flow_turn_id}", testTeam.Calls["check_status"].Command.IdempotencyKey)
	require.ElementsMatch(t,
		[]string{"VerificationReport", "GitStatusReport"},
		[]string{
			testTeam.Output.Records[0].Record,
			testTeam.Output.Records[1].Record,
		},
	)

	fixTeam := definitions.Teams["fix_team"]
	require.Equal(t, types.CallAgent, fixTeam.Calls["fix"].Type)
	require.Equal(t, "code-fixer", fixTeam.Calls["fix"].AgentID)
	require.ElementsMatch(t, []string{"code-fix", "gitignore-diagnostics"}, definitions.Agents["code-fixer"].Skills)
	require.ElementsMatch(t, []string{"safety", "fix-boundary"}, definitions.Agents["code-fixer"].Rules)
}

func TestAutoBugfixGitignoreExampleHasLowComplexityMultiAgentSmokeFlow(t *testing.T) {
	definitions := loadDefinitions(t, "auto-bugfix-gitignore", ".agents/flows/multi_agent_smoke.yml")

	require.Equal(t, "auto_bugfix_multi_agent_smoke", definitions.Flow.ID)
	require.Equal(t, "diagnose", definitions.Flow.EntryTeamID)
	require.Len(t, definitions.Flow.Teams, 2)
	require.True(t, definitions.Flow.Teams["diagnose"].Coordinator)
	require.Equal(t, []string{"review"}, definitions.Flow.Teams["diagnose"].OnProceed.Teams)
	require.Equal(t, []string{"diagnose"}, definitions.Flow.Teams["review"].DependsOn)
	require.Equal(t, types.NextComplete, definitions.Flow.Teams["review"].OnProceed.Action)

	diagnose := definitions.Teams["diagnose_team"]
	require.Equal(t, types.CallCommand, diagnose.Calls["git_snapshot"].Type)
	require.Equal(t, types.CallAgent, diagnose.Calls["explorer"].Type)
	require.Equal(t, types.CallAgent, diagnose.Calls["inspect"].Type)
	require.Equal(t, []string{"git_snapshot", "explorer"}, diagnose.Calls["inspect"].DependsOn)

	review := definitions.Teams["collab_review_team"]
	require.Len(t, review.Calls, 1)
	require.Equal(t, types.CallAgent, review.Calls["review"].Type)
	require.Equal(t, "challenger", review.Calls["review"].AgentID)
	require.Equal(t, "ChallengeReport", review.Calls["review"].Output.Record)
}

func keys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}
