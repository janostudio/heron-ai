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
	require.Len(t, team.Members, 1)

	answer := team.Members["answer"]
	require.Equal(t, types.MemberSubagent, answer.Type)
	require.Equal(t, "assistant", answer.AgentID)
	require.True(t, answer.Inputs.UserMessage)
	require.Equal(t, "Answer", answer.Output.Record)
	require.Contains(t, definitions.Agents, "assistant")
}

func TestCodeReviewExampleUsesParallelMembersAndOutputContract(t *testing.T) {
	definitions := loadDefinitions(t, "code-review", ".agents/code_review.yml")

	require.Equal(t, "code_review_flow", definitions.Flow.ID)
	require.Equal(t, "review", definitions.Flow.EntryTeamID)
	require.True(t, definitions.Flow.Teams["review"].Coordinator)

	team := definitions.Teams["review_team"]
	require.Len(t, team.Members, 3)

	require.Equal(t, types.MemberSubagent, team.Members["security_review"].Type)
	require.Equal(t, types.MemberSubagent, team.Members["performance_review"].Type)
	require.Equal(t, types.MemberSubagent, team.Members["aggregate"].Type)
	require.ElementsMatch(t,
		[]string{"security_review", "performance_review"},
		team.Members["aggregate"].DependsOn,
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
	require.Len(t, research.Members, 2)
	require.Equal(t, types.MemberSubagent, research.Members["research_topic"].Type)
	require.Equal(t, types.MemberSubagent, research.Members["plan_outline"].Type)
	require.Equal(t, "ResearchReport", research.Members["research_topic"].Output.Record)
	require.Equal(t, "BlogOutline", research.Members["plan_outline"].Output.Record)

	writing := definitions.Teams["writing_team"]
	require.Equal(t, []string{"research"}, definitions.Flow.Teams["writing"].DependsOn)
	require.Equal(t, "writer", writing.Members["write_blog"].AgentID)
	require.Equal(t, "BlogDraft", writing.Members["write_blog"].Output.Record)
	require.Len(t, writing.Members["write_blog"].Inputs.Records, 2)

	review := definitions.Teams["review_team"]
	require.Equal(t, "editor", review.Members["review_blog"].AgentID)
	require.Equal(t, "FinalBlog", review.Members["review_blog"].Output.Record)

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
	require.Len(t, team.Members, 3)
	require.ElementsMatch(t,
		[]string{"hero_act", "villain_act"},
		team.Members["narrate"].DependsOn,
	)
	require.Equal(t, "StoryTurn", team.Output.Record)
	require.Equal(t, "narrate", team.Output.From)
	require.Len(t, definitions.Agents, 3)
}
