package examples_test

import (
	"context"
	"fmt"
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

	team := definitions.Teams["qa_team"]
	require.Equal(t, "qa_team", team.ID)
	require.Len(t, team.Calls, 1)
	require.True(t, team.State.Enabled)
	require.Equal(t, 20, team.State.MaxItems)
	require.Equal(t, 4000, team.State.MaxChars)
	require.True(t, team.State.InjectSummary)

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
	require.Equal(t, []string{"writing"}, definitions.Flow.Teams["research"].OnProceed.Teams)
	require.Equal(t, []string{"review"}, definitions.Flow.Teams["writing"].OnProceed.Teams)
	// Session lifecycle is no longer configurable: the terminal team has no
	// on_proceed action, and a finished turn always stays resumable.
	require.Nil(t, definitions.Flow.Teams["review"].OnProceed)

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

func TestNovelRPExampleUsesEntryActivationAndParallelLanes(t *testing.T) {
	definitions := loadDefinitions(t, "novel-rp", ".agents/flows/flow.yml")

	require.Equal(t, "novel_story", definitions.Flow.ID)
	require.Equal(t, "lobby", definitions.Flow.EntryTeamID)
	require.Len(t, definitions.Flow.Teams, 3)

	// lobby is the entry and may activate perform.
	lobby := definitions.Flow.Teams["lobby"]
	require.Equal(t, []string{"perform"}, lobby.CanActivate)
	require.Nil(t, lobby.OnProceed)

	// perform runs after lobby and proceeds to weave.
	perform := definitions.Flow.Teams["perform"]
	require.Equal(t, []string{"lobby"}, perform.DependsOn)
	require.Equal(t, []string{"weave"}, perform.OnProceed.Teams)

	// weave terminates the turn; the next turn restarts from the entry.
	weave := definitions.Flow.Teams["weave"]
	require.Equal(t, []string{"perform"}, weave.DependsOn)
	require.Nil(t, weave.OnProceed)

	// lobby_team: the director plans the scene and publishes ScenePlan.
	lobbyTeam := definitions.Teams["lobby_team"]
	require.Equal(t, "director", lobbyTeam.Calls["direct"].AgentID)
	require.Equal(t, "ScenePlan", lobbyTeam.Output.Record)
	require.Equal(t, "direct", lobbyTeam.Output.From)

	// perform_team: six parallel lanes of the same generic role-actor.
	performTeam := definitions.Teams["perform_team"]
	require.Len(t, performTeam.Calls, 6)
	for i := 1; i <= 6; i++ {
		call, ok := performTeam.Calls[fmt.Sprintf("role_lane_%d", i)]
		require.True(t, ok, "role_lane_%d should exist", i)
		require.Equal(t, "role-actor", call.AgentID)
		require.Empty(t, call.DependsOn)
		require.Equal(t, "RolePerformances", call.Output.Record)
	}
	require.Len(t, performTeam.Output.Records, 6)

	// weave_team: the narrator weaves the final StoryTurn.
	weaveTeam := definitions.Teams["weave_team"]
	require.Equal(t, "narrator", weaveTeam.Calls["narrate"].AgentID)
	require.Equal(t, "StoryTurn", weaveTeam.Output.Record)

	require.Len(t, definitions.Agents, 3)
	for _, name := range []string{"director", "role-actor", "narrator"} {
		agent, ok := definitions.Agents[name]
		require.True(t, ok, "agent %s should exist", name)
		require.NotEmpty(t, agent.Body)
	}
}
