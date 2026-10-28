package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestRunTeamAcceptsAnswerOnlyTeam is the counterweight to
// TestRunTeamRejectsIdleCallee.
//
// A Team whose agent declares no tools is finished when it answers: there is
// nothing else it could have done. Treating "no tool calls" as failure there
// makes every answer-only callee unusable — the caller is told the callee
// "did not do the work" after it already did, and retries until its rounds run
// out (minimal A→B fixture, 2026-10-27).
func TestRunTeamAcceptsAnswerOnlyTeam(t *testing.T) {
	defs := testDefinitions()
	defs.Teams["verify-team"] = types.Team{
		ID: "verify-team",
		Calls: map[string]types.Call{
			"answer": {Type: types.CallAgent, AgentID: "answer-only"},
		},
	}
	defs.Agents = map[string]types.AgentConfig{
		"answer-only": {Name: "answer-only"},
	}

	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(defs, &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-answer-only", types.SessionWaitingInput)

	outcome, err := rt.RunTeam(context.Background(), types.TeamCallSpec{
		FlowSessionID: session.ID,
		CallerTeam:    "default",
		TeamID:        "verify",
		Item:          "版本号是多少",
	})

	require.NoError(t, err)
	require.NotEqual(t, "failed", outcome.Status,
		"a team with no tools cannot fail for not using a tool")
}

// TestRunTeamStillRejectsIdleTeamWithTools keeps the s19 guard: a team that HAS
// tools and used none really did nothing.
func TestRunTeamStillRejectsIdleTeamWithTools(t *testing.T) {
	defs := testDefinitions()
	defs.Teams["verify-team"] = types.Team{
		ID: "verify-team",
		Calls: map[string]types.Call{
			"work": {Type: types.CallAgent, AgentID: "tool-agent"},
		},
	}
	defs.Agents = map[string]types.AgentConfig{
		"tool-agent": {Name: "tool-agent", Tools: types.ToolConfig{Builtin: []string{"Bash"}}},
	}

	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(defs, &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-idle-tools", types.SessionWaitingInput)

	outcome, err := rt.RunTeam(context.Background(), types.TeamCallSpec{
		FlowSessionID: session.ID,
		CallerTeam:    "default",
		TeamID:        "verify",
		Item:          "核实主分支",
	})

	require.NoError(t, err)
	require.Equal(t, "failed", outcome.Status, "a team with tools that used none did nothing")
	require.Contains(t, outcome.Error, "without calling any tool")
}
