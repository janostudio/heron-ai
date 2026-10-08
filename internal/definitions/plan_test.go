package definitions

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// halfPlanFiles are the bytes of a three-file commit: an agent and a team that
// the flow will reference, plus the flow that references them. Ranks 0, 1 and
// 2, so the boundary between "referenced" and "referencing" sits exactly in
// front of the third one.
const (
	halfAgentMD = "---\nname: half_agent\n---\n\n你是 half_agent。\n"
	halfTeamYML = `id: half_team
goal: 半写状态下依然存在的团队。

calls:
  answer:
    type: agent
    agent: half_agent
    responsibility: 回答。
    output:
      record: Answer

output:
  from: answer
  record: Answer
`
	halfFlowYML = `id: default
entry: default

teams:
  default:
    team: qa_team
    coordinator: true
    inputs:
      user_message: true
  half:
    team: half_team
`
)

func halfPlan(t *testing.T) *WritePlan {
	t.Helper()
	plan := &WritePlan{}
	require.NoError(t, plan.Add(WriteOp{
		Path:       "agents/half_agent/AGENT.md",
		Data:       []byte(halfAgentMD),
		Mode:       WriteCreate,
		CreatesDir: true,
	}))
	require.NoError(t, plan.Add(WriteOp{
		Path: "teams/half_team.yml",
		Data: []byte(halfTeamYML),
		Mode: WriteCreate,
	}))
	require.NoError(t, plan.Add(WriteOp{
		Path: "flows/default.yml",
		Data: []byte(halfFlowYML),
		Mode: WriteReplace,
	}))
	return plan
}

// TestApplyPlanToHookSplitsTheCommitHalves is the test applyPlanTo exists for.
//
// A multi-file commit is not atomic, so the only guarantee is the ORDER: every
// prefix of the commit order must still be a loadable tree. afterStage (the
// writer's other seam) fires before any byte lands, so it can only ever produce
// "nothing written". This hook is the only way to stop the commit between the
// referenced half and the referencing half, which is the interesting prefix.
func TestApplyPlanToHookSplitsTheCommitHalves(t *testing.T) {
	f := newFixture(t)
	flowBefore := f.read(t, "flows/default.yml")

	calls := 0
	var seenAgent, seenTeam, seenFlow bool

	crash := errors.New("simulated crash between the halves")
	err := halfPlan(t).applyPlanTo(f.root, func() error {
		calls++
		seenAgent = f.exists("agents/half_agent/AGENT.md")
		seenTeam = f.exists("teams/half_team.yml")
		seenFlow = f.read(t, "flows/default.yml") != flowBefore
		return crash
	})

	require.ErrorIs(t, err, crash, "a non-nil before must abort the remaining writes")
	require.Equal(t, 1, calls, "the hook fires once, at the boundary, not per file")

	// At the moment it fired: the referenced half is on disk, the referencing
	// file is not.
	require.True(t, seenAgent, "the agent must already be committed when the hook fires")
	require.True(t, seenTeam, "the team must already be committed when the hook fires")
	require.False(t, seenFlow, "the flow must not be touched yet when the hook fires")

	// And that state survives: the aborted commit left the tree half-written.
	require.True(t, f.exists("agents/half_agent/AGENT.md"))
	require.True(t, f.exists("teams/half_team.yml"))
	require.Equal(t, flowBefore, f.read(t, "flows/default.yml"),
		"the flow must still hold its pre-commit bytes")

	// The point of the whole ordering: that half-written tree still loads. The
	// new definitions are orphans nobody references yet, and the flow still
	// points at definitions that do exist — the reverse order would have left a
	// flow binding a team that is not there.
	loaded := f.load(t)
	require.Contains(t, loaded.Agents, "half_agent")
	require.Contains(t, loaded.Teams, "half_team")
	require.NotContains(t, loaded.Flow.Teams, "half",
		"the binding must not be visible before the flow lands")
	require.Equal(t, "qa_team", loaded.Flow.Teams["default"].TeamID)
}

// TestApplyPlanToContinuesWhenBeforeSucceeds pins the other half of the
// contract: the hook is a seam, not a stop. Returning nil must let the
// referencing files land, and must not fire again for them.
func TestApplyPlanToContinuesWhenBeforeSucceeds(t *testing.T) {
	f := newFixture(t)

	calls := 0
	require.NoError(t, halfPlan(t).applyPlanTo(f.root, func() error {
		calls++
		return nil
	}))
	require.Equal(t, 1, calls, "the hook fires once even with more files after it")

	require.True(t, f.exists("agents/half_agent/AGENT.md"))
	require.True(t, f.exists("teams/half_team.yml"))
	require.Contains(t, f.read(t, "flows/default.yml"), "half_team")

	loaded := f.load(t)
	require.Contains(t, loaded.Flow.Teams, "half")
	require.Equal(t, "half_team", loaded.Flow.Teams["half"].TeamID)
}

// TestApplyPlanToSkipsHookWithoutReferencingFile covers the plan shape the hook
// has no answer for: a create_agent plan touches only referenced definitions,
// so there is no boundary between the halves and the hook never runs.
func TestApplyPlanToSkipsHookWithoutReferencingFile(t *testing.T) {
	f := newFixture(t)

	plan := &WritePlan{}
	require.NoError(t, plan.Add(WriteOp{
		Path:       "agents/half_agent/AGENT.md",
		Data:       []byte(halfAgentMD),
		Mode:       WriteCreate,
		CreatesDir: true,
	}))

	calls := 0
	require.NoError(t, plan.applyPlanTo(f.root, func() error {
		calls++
		return errors.New("must not be reached")
	}))
	require.Zero(t, calls, "no referencing file means no boundary to stop at")
	require.True(t, f.exists("agents/half_agent/AGENT.md"),
		"skipping the hook must not skip the write")
}
