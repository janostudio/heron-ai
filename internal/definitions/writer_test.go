package definitions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/config"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestCreateAgentHappy(t *testing.T) {
	f := newFixture(t)

	result, err := f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode:     ModeCreate,
		Template: "agent.generic",
		Spec: map[string]any{
			"name": "reviewer",
			"persona": map[string]any{
				"role": "代码审查员",
				"goal": "找出缺陷",
			},
			"model": map[string]any{"provider": "openai", "model": "gpt-4o-mini"},
		},
	})
	require.NoError(t, err)

	require.Equal(t, "create_agent", result.Action)
	require.Equal(t, "created", result.Mode)
	require.Equal(t, []string{"reviewer"}, result.CreatedAgents)
	require.True(t, result.Reloaded)
	require.Equal(t, []string{filepath.Join(f.root, "agents", "reviewer", "AGENT.md")}, result.Files)
	require.True(t, result.BoundToFlow == "", "creating an agent must not touch the flow")

	// The store picks the new agent up without a restart, which is the whole
	// point of the feature.
	require.Contains(t, f.store.Snapshot().Agents, "reviewer")
	require.Equal(t, "代码审查员", f.store.Snapshot().Agents["reviewer"].Persona.Role)

	// And the tree still loads from scratch, so the file is genuinely valid and
	// not just accepted by an in-memory path.
	require.Contains(t, f.load(t).Agents, "reviewer")
	requireNoStagingDir(t, f.root)
}

func TestCreateTeamCreatesInlineAgentsAndBindsFlow(t *testing.T) {
	f := newFixture(t)

	result, err := f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode:     ModeCreate,
		Template: "team.sequential",
		Spec: map[string]any{
			"id":   "research_team",
			"goal": "调研并汇总结论。",
			"calls": map[string]any{
				"collect": map[string]any{
					"type":           "agent",
					"agent":          "researcher",
					"responsibility": "收集资料。",
					"inputs":         map[string]any{"user_message": true},
					"output":         map[string]any{"record": "Research"},
				},
			},
			"agent_specs": map[string]any{
				"researcher": map[string]any{
					"persona": map[string]any{"role": "调研员"},
					"model":   map[string]any{"model": "gpt-4o-mini"},
				},
			},
			"bind": map[string]any{
				"key":          "research",
				"can_activate": []any{"research"},
			},
		},
	})
	require.NoError(t, err)

	require.Equal(t, "create_team", result.Action)
	require.Equal(t, "created", result.Mode)
	require.Equal(t, []string{"researcher"}, result.CreatedAgents)
	require.Equal(t, "default", result.BoundToFlow)
	require.True(t, result.Reloaded)

	// One call produced three files, and the commit order wrote the referenced
	// definitions before the flow that references them.
	require.Equal(t, []string{
		filepath.Join(f.root, "agents", "researcher", "AGENT.md"),
		filepath.Join(f.root, "teams", "research_team.yml"),
		filepath.Join(f.root, "flows", "default.yml"),
	}, result.Files)

	require.True(t, f.exists("teams/research_team.yml"))
	require.True(t, f.exists("agents/researcher/AGENT.md"))

	snapshot := f.store.Snapshot()
	require.Contains(t, snapshot.Teams, "research_team")
	require.Contains(t, snapshot.Agents, "researcher")
	require.Contains(t, snapshot.Flow.Teams, "research",
		"the new binding must be visible on the next turn")
	require.Equal(t, "research_team", snapshot.Flow.Teams["research"].TeamID)
	require.False(t, snapshot.Flow.Teams["research"].Coordinator,
		"the new binding must not become a second coordinator")

	// The flow file is re-marshalled, so the original binding survives.
	require.Equal(t, "qa_team", snapshot.Flow.Teams["default"].TeamID)

	reloaded := f.load(t)
	require.Contains(t, reloaded.Flow.Teams, "research")
	require.Equal(t, "调研员", reloaded.Agents["researcher"].Persona.Role)
}

func TestUpsertAgentMergesScalarsKeepsUnset(t *testing.T) {
	f := newFixture(t)

	before := f.store.Snapshot().Agents["assistant"]
	require.Equal(t, "助手", before.Persona.Role)

	result, err := f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{
			"name":  "assistant",
			"model": map[string]any{"model": "claude-sonnet-5"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "updated", result.Mode)
	require.Equal(t, []string{"assistant"}, result.UpdatedAgents)
	require.Empty(t, result.CreatedAgents)

	agent := f.store.Snapshot().Agents["assistant"]
	require.Equal(t, "claude-sonnet-5", agent.Model.Model, "supplied key must overwrite")
	require.Equal(t, "openai", agent.Model.Provider, "sibling key inside a nested object must survive")
	require.Equal(t, "助手", agent.Persona.Role, "an absent nested object must survive untouched")
	require.Equal(t, "回答用户问题", agent.Persona.Goal)
	require.Equal(t, []string{"Read", "Grep", "Glob"}, agent.Tools.Builtin,
		"an absent list must survive untouched")
	require.Equal(t, 14, agent.Loop.MaxRounds)
	require.True(t, agent.HITL != nil && agent.HITL.Enabled)
	require.Contains(t, agent.Body, "你是助手",
		"an absent body must survive untouched")
}

func TestUpsertAgentReplacesListsWholesale(t *testing.T) {
	f := newFixture(t)

	_, err := f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{
			"name":  "assistant",
			"tools": map[string]any{"builtin": []any{"Bash"}},
		},
	})
	require.NoError(t, err)

	agent := f.store.Snapshot().Agents["assistant"]
	require.Equal(t, []string{"Bash"}, agent.Tools.Builtin,
		"a supplied list replaces wholesale; a union would make entries impossible to remove")

	// Omitting the list on a second upsert leaves the replaced value alone, i.e.
	// the rule is "supplied replaces, absent keeps" and not "the merge is
	// sticky".
	_, err = f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{"name": "assistant", "persona": map[string]any{"goal": "新的目标"}},
	})
	require.NoError(t, err)

	agent = f.store.Snapshot().Agents["assistant"]
	require.Equal(t, []string{"Bash"}, agent.Tools.Builtin,
		"omitting tools must preserve the list from the previous upsert")
	require.Equal(t, "新的目标", agent.Persona.Goal)
}

// TestUpsertAgentDecodesZeroValue is the test that justifies funnelling the
// merge through map[string]any instead of merging typed structs: with a struct
// merge, `temperature: 0` and an omitted temperature are both the zero value,
// so one of the two behaviours below would be impossible to express.
func TestUpsertAgentDecodesZeroValue(t *testing.T) {
	f := newFixture(t)

	require.NotNil(t, f.store.Snapshot().Agents["assistant"].Model.Temperature)
	require.InDelta(t, 0.7, *f.store.Snapshot().Agents["assistant"].Model.Temperature, 1e-9)

	// 1. Omitting temperature keeps the existing value.
	_, err := f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{"name": "assistant", "model": map[string]any{"provider": "openai"}},
	})
	require.NoError(t, err)
	require.NotNil(t, f.store.Snapshot().Agents["assistant"].Model.Temperature)
	require.InDelta(t, 0.7, *f.store.Snapshot().Agents["assistant"].Model.Temperature, 1e-9,
		"an omitted temperature must not reset to zero")

	// 2. Explicitly passing 0 overwrites the existing value with zero.
	_, err = f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{"name": "assistant", "model": map[string]any{"temperature": 0}},
	})
	require.NoError(t, err)

	agent := f.store.Snapshot().Agents["assistant"]
	require.NotNil(t, agent.Model.Temperature, "an explicit zero must be written, not dropped")
	require.InDelta(t, 0.0, *agent.Model.Temperature, 1e-9,
		"an explicit temperature: 0 must differ from omitting it")

	// The distinction must also survive a fresh read of the file on disk.
	require.InDelta(t, 0.0, *f.load(t).Agents["assistant"].Model.Temperature, 1e-9)
}

func TestUpsertAgentRejectsRename(t *testing.T) {
	f := newFixture(t)

	_, err := f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{"name": "someone-new"},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "does not exist")

	// An upsert of an existing agent whose spec names a different target is
	// caught before it can leave a stray definition behind.
	err = checkNoRename("assistant", "someone-new", "agent")
	require.ErrorContains(t, err, "cannot rename agent")
	require.NoError(t, checkNoRename("assistant", "assistant", "agent"))
	require.NoError(t, checkNoRename("assistant", "", "agent"))
}

func TestUpsertTeamMergesCallsByName(t *testing.T) {
	f := newFixture(t)

	result, err := f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{
			"id":   "qa_team",
			"goal": "更新后的目标。",
			"calls": map[string]any{
				"verify": map[string]any{
					"type":           "command",
					"command":        "go test ./...",
					"responsibility": "运行测试。",
					"output":         map[string]any{"record": "Verification"},
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "updated", result.Mode)
	require.Empty(t, result.CreatedAgents)
	require.Empty(t, result.UpdatedAgents, "the result tracks inline AGENTS, not the team itself")

	team := f.store.Snapshot().Teams["qa_team"]
	require.Contains(t, team.Calls, "answer", "adding a call must not erase existing calls")
	require.Contains(t, team.Calls, "verify", "the new call must be added")
	require.Equal(t, types.CallCommand, team.Calls["verify"].Type)
	require.Equal(t, "go test ./...", team.Calls["verify"].Command.Command)
	require.Equal(t, "agent", string(team.Calls["answer"].Type))
	require.Equal(t, "assistant", team.Calls["answer"].AgentID)
	require.Equal(t, "更新后的目标。", team.Goal)

	// A same-name call is overwritten rather than duplicated.
	_, err = f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{
			"id": "qa_team",
			"calls": map[string]any{
				"answer": map[string]any{"responsibility": "改写后的职责。"},
			},
		},
	})
	require.NoError(t, err)

	team = f.store.Snapshot().Teams["qa_team"]
	require.Len(t, team.Calls, 2)
	require.Equal(t, "改写后的职责。", team.Calls["answer"].Responsibility)
	require.Equal(t, "assistant", team.Calls["answer"].AgentID,
		"overwriting one key of a call must not drop its siblings")
	require.Contains(t, team.Calls, "verify")
}

func TestModeCreateRejectsExistingTarget(t *testing.T) {
	f := newFixture(t)

	_, err := f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeCreate,
		Spec: map[string]any{"name": "assistant"},
	})
	require.ErrorContains(t, err, `agent "assistant" already exists`)
	require.ErrorContains(t, err, "upsert")

	_, err = f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeCreate,
		Spec: map[string]any{"id": "qa_team"},
	})
	require.ErrorContains(t, err, `team "qa_team" already exists`)

	// Upserting a target that does not exist is the mirror-image mistake.
	_, err = f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{"name": "nobody"},
	})
	require.ErrorContains(t, err, `agent "nobody" does not exist`)
}

func TestApplyRejectsDuplicateAgentName(t *testing.T) {
	f := newFixture(t)

	// A team whose call names an existing agent must not try to create it —
	// otherwise the tree would hold two definitions with one name.
	result, err := f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeCreate,
		Spec: map[string]any{
			"id": "dup_team",
			"calls": map[string]any{
				"answer": map[string]any{"type": "agent", "agent": "assistant"},
			},
			"agent_specs": map[string]any{
				"assistant": map[string]any{"persona": map[string]any{"role": "覆盖"}},
			},
		},
	})
	require.NoError(t, err)

	require.Equal(t, "助手", f.store.Snapshot().Agents["assistant"].Persona.Role,
		"an existing agent referenced by a new team must not be overwritten")
	require.Empty(t, result.CreatedAgents, "no inline agent should have been created")

	// The duplicate-name check itself.
	err = checkAgentNameAvailable(map[string]types.AgentConfig{"assistant": {Name: "assistant"}}, "assistant")
	require.ErrorContains(t, err, `agent "assistant" already exists`)
	require.NoError(t, checkAgentNameAvailable(map[string]types.AgentConfig{}, "assistant"))
}

func TestApplyRejectsDanglingAgentRef(t *testing.T) {
	f := newFixture(t)

	// A team file written by hand that names an agent nobody defined. The
	// loader catches it, and the writer must surface that instead of committing
	// a tree the engine cannot load.
	f.write(t, "teams/broken.yml", `
id: broken_team
calls:
  answer:
    type: agent
    agent: ghost
`)
	// The writer resolves targets against the live snapshot, so the hand-written
	// file has to be picked up before the upsert can address it.
	f.reload(t)

	_, err := f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeCreate,
		Spec: map[string]any{
			"id": "new_team",
			"calls": map[string]any{
				"answer": map[string]any{"type": "agent", "agent": "assistant"},
			},
			"bind": map[string]any{"key": "new"},
		},
	})
	require.NoError(t, err, "an unreferenced broken team does not block an unrelated create")
	require.NotContains(t, f.store.Snapshot().Agents, "ghost",
		"an existing team's dangling call must not be papered over with a generated agent")

	// Now bind the broken team into the flow, which makes the dangling
	// reference reachable and therefore fatal.
	_, err = f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{
			"id":   "broken_team",
			"bind": map[string]any{"key": "broken"},
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "references missing agent definition",
		"the loader's dangling-reference message must reach the caller")
	require.NotContains(t, f.store.Snapshot().Flow.Teams, "broken")
	requireNoStagingDir(t, f.root)
}

func TestApplyRejectsSecondCoordinator(t *testing.T) {
	f := newFixture(t)

	_, err := f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeCreate,
		Spec: map[string]any{
			"id": "second_team",
			"calls": map[string]any{
				"answer": map[string]any{"type": "agent", "agent": "assistant"},
			},
			"bind": map[string]any{"key": "second", "coordinator": true},
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "exactly one coordinator team is required")

	// Nothing landed: the flow is untouched and the new definitions were never
	// committed, because the failure happens while the plan is being built.
	require.Len(t, f.store.Snapshot().Flow.Teams, 1)
	require.NotContains(t, f.store.Snapshot().Teams, "second_team")
	require.False(t, f.exists("teams/second_team.yml"))

	// The pure helper covers the same rule directly.
	flow := types.Flow{
		ID:          "default",
		EntryTeamID: "a",
		Teams: map[string]types.FlowTeamBinding{
			"a": {ID: "a", TeamID: "team-a", Coordinator: true},
			"b": {ID: "b", TeamID: "team-b", Coordinator: true},
		},
	}
	require.ErrorContains(t, checkSingleCoordinator(flow), "exactly one coordinator")
	delete(flow.Teams, "b")
	require.NoError(t, checkSingleCoordinator(flow))
}

func TestApplyRejectsOutsideConfigRoot(t *testing.T) {
	f := newFixture(t)

	for _, name := range []string{"../../evil", `..\..\evil`, "/tmp/evil", "a/b"} {
		t.Run(name, func(t *testing.T) {
			_, err := f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
				Mode: ModeCreate,
				Spec: map[string]any{"name": name},
			})
			require.Error(t, err)
			require.Regexp(t, `path separator|absolute path`, err.Error())
		})
	}

	// The escape must also be refused at the path layer, which is what a
	// template or a future caller would go through.
	for _, rel := range []string{"../evil", "agents/../../evil"} {
		_, err := safeJoin(f.root, rel)
		require.ErrorContains(t, err, "escapes the config root")
	}
	for _, rel := range []string{"/etc/passwd", "/tmp/evil"} {
		_, err := safeJoin(f.root, rel)
		require.ErrorContains(t, err, "must be relative to the config root")
	}

	joined, err := safeJoin(f.root, "agents/ok/AGENT.md")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(f.root, "agents", "ok", "AGENT.md"), joined)

	// Nothing escaped.
	parent := filepath.Dir(f.root)
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotEqual(t, "evil", entry.Name())
	}
}

func TestApplyRollbackOnValidationFailure(t *testing.T) {
	f := newFixture(t)

	// A team bound into the flow whose call names an agent that does not exist:
	// the plan stages fine but the candidate tree fails validation. The only
	// reason this is a validation failure and not a pre-flight rejection is that
	// the dangling reference lives in an existing hand-written file.
	f.write(t, "teams/orphan.yml", `
id: orphan_team
calls:
  answer:
    type: agent
    agent: missing-agent
`)
	f.reload(t)

	// Re-baseline: the hand-written file is part of the "before" tree now.
	before := snapshotTree(t, f.root)

	_, err := f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{
			"id":          "orphan_team",
			"description": "nope",
			"bind":        map[string]any{"key": "orphan"},
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "references missing agent definition")

	after := snapshotTree(t, f.root)
	require.Equal(t, before, after, "the tree must be byte-identical after a failed apply")
	requireNoStagingDir(t, f.root)
	require.Len(t, f.store.Snapshot().Flow.Teams, 1)
}

func TestApplyCommitOrderWritesReferencedFirst(t *testing.T) {
	f := newFixture(t)

	// The staging phase is the last moment before any byte is committed, so
	// failing there simulates a crash with zero files written.
	w := f.writer(t)
	w.afterStage = func() error { return os.ErrClosed }

	_, err := w.CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeCreate,
		Spec: map[string]any{
			"id": "partial_team",
			"calls": map[string]any{
				"answer": map[string]any{"type": "agent", "agent": "partial_agent"},
			},
			"bind": map[string]any{"key": "partial"},
		},
	})
	require.Error(t, err)
	require.False(t, f.exists("teams/partial_team.yml"))
	require.False(t, f.exists("flows/default.yml.tmp"))
	require.Len(t, f.store.Snapshot().Flow.Teams, 1)
	requireNoStagingDir(t, f.root)

	// The ordering guarantee itself: the referenced definitions rank before the
	// referencing flow.
	plan := &WritePlan{}
	require.NoError(t, plan.Add(WriteOp{Path: "flows/default.yml"}))
	require.NoError(t, plan.Add(WriteOp{Path: "teams/new.yml"}))
	require.NoError(t, plan.Add(WriteOp{Path: "agents/new/AGENT.md"}))

	require.Equal(t, []string{
		filepath.Join("agents", "new", "AGENT.md"),
		filepath.Join("teams", "new.yml"),
		filepath.Join("flows", "default.yml"),
	}, orderedPaths(plan))

	// A tree holding only the first two prefixes of that order still loads: an
	// unreferenced agent or team is an orphan, not a broken reference. That is
	// exactly why this order, and not the reverse, is the atomicity guarantee.
	prefix := newFixture(t)
	prefix.write(t, "teams/extra.yml", "id: extra_team\ncalls:\n  answer:\n    type: agent\n    agent: assistant\n")
	prefix.load(t)

	conflicting := &WritePlan{}
	require.NoError(t, conflicting.Add(WriteOp{Path: "flows/default.yml"}))
	require.ErrorContains(t, conflicting.Add(WriteOp{Path: "flows/default.yml"}), "twice",
		"two writes to one path would corrupt the rollback image")
}

func orderedPaths(p *WritePlan) []string {
	out := make([]string, 0, len(p.Ops))
	for _, op := range p.InCommitOrder() {
		out = append(out, op.Path)
	}
	return out
}

func TestApplyConcurrentModificationAborts(t *testing.T) {
	f := newFixture(t)

	flowBefore := f.read(t, "flows/default.yml")
	require.NotContains(t, flowBefore, "sneaky")

	w := f.writer(t)
	// afterStage is the last point before the commit re-reads the tree, so an
	// edit made here is indistinguishable from a human saving the file between
	// the snapshot and the commit.
	w.afterStage = func() error {
		f.write(t, "flows/default.yml", flowBefore+"\n# 人工编辑\n")
		return nil
	}

	_, err := w.CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeCreate,
		Spec: map[string]any{
			"id": "sneaky_team",
			"calls": map[string]any{
				"answer": map[string]any{"type": "agent", "agent": "assistant"},
			},
			"bind": map[string]any{"key": "sneaky"},
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "config changed concurrently")

	// Optimistic concurrency: the human's edit is what survives, and the
	// definitions that had already been committed are rolled back.
	require.Equal(t, flowBefore+"\n# 人工编辑\n", f.read(t, "flows/default.yml"))
	require.False(t, f.exists("teams/sneaky_team.yml"),
		"the committed team must be rolled back when a later file conflicts")
	requireNoStagingDir(t, f.root)

	// The tree still loads and the live store was never republished.
	require.NoError(t, func() error { _, err := os.Stat(f.flowPath); return err }())
	f.load(t)
	require.NotContains(t, f.store.Snapshot().Flow.Teams, "sneaky")
}

func TestCreateAgentRejectsUnknownSpecField(t *testing.T) {
	f := newFixture(t)

	_, err := f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeCreate,
		Spec: map[string]any{
			"name":        "typo_agent",
			"temperature": 0.2, // belongs under model:
		},
	})
	require.ErrorContains(t, err, "unsupported field")
	require.ErrorContains(t, err, "temperature")
	require.False(t, f.exists("agents/typo_agent/AGENT.md"))
}

func TestCreateAgentRejectsMissingSkill(t *testing.T) {
	f := newFixture(t)

	_, err := f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeCreate,
		Spec: map[string]any{"name": "needy", "skills": []any{"nonexistent-skill"}},
	})
	require.ErrorContains(t, err, `references missing skill "nonexistent-skill"`)

	// An unreachable agent's skill list is never checked by the loader, so a
	// typo there would otherwise be silent forever.
	require.NotContains(t, f.store.Snapshot().Agents, "needy")
}

func TestCreateTeamRejectsDuplicateBindingKey(t *testing.T) {
	f := newFixture(t)

	_, err := f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeCreate,
		Spec: map[string]any{
			"id": "other_team",
			"calls": map[string]any{
				"answer": map[string]any{"type": "agent", "agent": "assistant"},
			},
			"bind": map[string]any{"key": "default"},
		},
	})
	require.ErrorContains(t, err, `team key "default" is already taken`)

	// Binding the SAME team definition under a second key is also refused: the
	// flow would then have two identities for one team, and can_activate /
	// on_proceed routes address teams by key.
	err = checkFlowBindingKeyAvailable(f.store.Snapshot().Flow, "default", "qa_team")
	require.ErrorContains(t, err, "already binds team")
}

func TestWriterRequiresAbsoluteRootAndFlow(t *testing.T) {
	// A store built as a pure holder (no root/flow) cannot support writes; the
	// error has to say so instead of writing to the process's cwd.
	holder := types.NewDefinitionStore(&types.Definitions{}, "", "")
	w := NewWriter(holder)

	_, err := w.CreateAgent(ctx(), CreateAgentRequest{Mode: ModeCreate, Spec: map[string]any{"name": "x"}})
	require.ErrorContains(t, err, "no config root")

	holder2 := types.NewDefinitionStore(&types.Definitions{}, t.TempDir(), "")
	_, err = NewWriter(holder2).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeCreate,
		Spec: map[string]any{"name": "x"},
	})
	require.ErrorContains(t, err, "no flow path")
}

func TestSpecNameAcceptsIDOrName(t *testing.T) {
	name, err := specName(map[string]any{"name": "a"})
	require.NoError(t, err)
	require.Equal(t, "a", name)

	name, err = specName(map[string]any{"id": "b"})
	require.NoError(t, err)
	require.Equal(t, "b", name)

	_, err = specName(map[string]any{})
	require.ErrorContains(t, err, "spec.name is required")

	_, err = specName(map[string]any{"name": "   "})
	require.ErrorContains(t, err, "non-empty string")

	_, err = specName(map[string]any{"name": 42})
	require.ErrorContains(t, err, "non-empty string")
}

func TestDeepMergeSemantics(t *testing.T) {
	base := map[string]any{
		"scalar": "keep",
		"nested": map[string]any{"a": 1, "b": 2},
		"list":   []any{"x", "y"},
	}
	overlay := map[string]any{
		"nested": map[string]any{"b": 20, "c": 30},
		"list":   []any{"z"},
		"added":  true,
	}

	merged := deepMergeMap(base, overlay)
	require.Equal(t, "keep", merged["scalar"])
	require.Equal(t, map[string]any{"a": 1, "b": 20, "c": 30}, merged["nested"])
	require.Equal(t, []any{"z"}, merged["list"], "lists are leaves and replace wholesale")
	require.Equal(t, true, merged["added"])

	// Neither input may be mutated: the base can be a shared template document.
	require.Equal(t, []any{"x", "y"}, base["list"])
	require.Equal(t, map[string]any{"a": 1, "b": 2}, base["nested"])
	require.Equal(t, []any{"z"}, overlay["list"])

	// A nil overlay is a no-op rather than a wipe.
	require.Equal(t, base, deepMergeMap(base, nil))

	// deepCopyMap has to be deep, or the first instantiation leaks into the
	// next one through the shared template map.
	copied := deepCopyMap(base)
	copied["nested"].(map[string]any)["a"] = 99
	copied["list"].([]any)[0] = "changed"
	require.Equal(t, 1, base["nested"].(map[string]any)["a"])
	require.Equal(t, "x", base["list"].([]any)[0])
}

func TestSafeJoinRejectsEmptyAndAcceptNested(t *testing.T) {
	_, err := safeJoin("/root", "  ")
	require.ErrorContains(t, err, "must not be empty")

	_, err = safeJoin("/root", ".")
	require.ErrorContains(t, err, "escapes the config root")

	joined, err := safeJoin("/root", "agents/a/AGENT.md")
	require.NoError(t, err)
	require.Equal(t, filepath.Join("/root", "agents", "a", "AGENT.md"), joined)
}

func TestValidateDefinitionName(t *testing.T) {
	require.NoError(t, validateDefinitionName("reviewer", "agent"))
	require.NoError(t, validateDefinitionName("review-er_1", "agent"))

	require.ErrorContains(t, validateDefinitionName("", "agent"), "agent name is required")
	require.ErrorContains(t, validateDefinitionName("a/b", "agent"), "path separator")
	require.ErrorContains(t, validateDefinitionName("..", "agent"), "path separator")
	require.ErrorContains(t, validateDefinitionName("/abs", "agent"), "absolute path")
}

func TestKnowledgeIDsScansGlobalAndPrivate(t *testing.T) {
	f := newFixture(t)

	require.Nil(t, knowledgeIDs(f.root), "no knowledge directories means no ids to validate against")

	f.write(t, "knowledge/flow-doc.md", "---\nid: flow-doc\n---\nbody\n")
	f.write(t, "knowledge/index.md", "# index\n")
	f.write(t, "agents/assistant/knowledge/private.md", "---\nid: private\n---\nbody\n")

	ids := knowledgeIDs(f.root)
	require.Contains(t, ids, "flow-doc")
	require.Contains(t, ids, "private")
	require.NotContains(t, ids, "index", "index.md is an index file, not an entry")
}

func TestMergeTeamSpecKeepsCallsOnAdditiveUpdate(t *testing.T) {
	existing := map[string]any{
		"calls": map[string]any{
			"first":  map[string]any{"type": "agent", "agent": "a"},
			"second": map[string]any{"type": "agent", "agent": "b"},
		},
	}
	spec := map[string]any{
		"calls": map[string]any{
			"third": map[string]any{"type": "agent", "agent": "c"},
		},
	}

	merged := mergeTeamSpec(existing, spec)
	calls := merged["calls"].(map[string]any)
	require.Len(t, calls, 3, "calls merge by name; a wholesale replace would drop first and second")

	// Without a calls key on either side the merge is a plain deep merge.
	require.Equal(t,
		map[string]any{"goal": "g"},
		mergeTeamSpec(map[string]any{"goal": "g"}, map[string]any{}),
	)
}

func TestCommitOrderRankingUnknownPathsLast(t *testing.T) {
	require.Equal(t, 0, commitRank("agents/a/AGENT.md"))
	require.Equal(t, 1, commitRank("teams/a.yml"))
	require.Equal(t, 2, commitRank("flows/default.yml"))
	require.Equal(t, 3, commitRank("settings.json"),
		"an unrecognised file is more likely to reference than to be referenced")
	require.Equal(t, 3, commitRank("mystery"))
}

func TestRollbackRestoresAndRemovesInReverse(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "agents", "a")
	require.NoError(t, os.MkdirAll(tree, 0o755))
	target := filepath.Join(tree, "AGENT.md")
	require.NoError(t, os.WriteFile(target, []byte("new"), 0o644))

	created := filepath.Join(root, "teams", "t.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(created), 0o755))
	require.NoError(t, os.WriteFile(created, []byte("new"), 0o644))

	result := rollback(root, []WriteOp{
		{Path: "agents/a/AGENT.md", Original: []byte("old"), Existed: true},
		{Path: "teams/t.yml", Existed: false},
	})
	require.True(t, result.OK(), result.Error())

	restored, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "old", string(restored))

	_, err = os.Stat(created)
	require.True(t, os.IsNotExist(err), "a file this apply created is removed, not emptied")

	// Rolling back a file that is already gone is a success, not a failure.
	result = rollback(root, []WriteOp{{Path: "teams/t.yml", Existed: false}})
	require.True(t, result.OK())
}

func TestRenderAgentRoundTripsThroughFrontmatter(t *testing.T) {
	spec := map[string]any{
		"name":    "round-trip",
		"persona": map[string]any{"role": "r"},
		"model":   map[string]any{"temperature": 0},
		"tools":   map[string]any{"builtin": []any{"Bash"}},
	}
	data, err := renderAgent(spec, "正文内容\n")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "---\n"))

	front, body, err := splitFrontmatter(string(data))
	require.NoError(t, err)
	require.NotEmpty(t, front)
	// A blank line separates the closing delimiter from the prompt, matching the
	// hand-written examples; the loader trims it on the way back in.
	require.Equal(t, "正文内容", strings.TrimSpace(body))
	decoded, decodedBody, err := frontmatterAndBody(data)
	require.NoError(t, err)
	require.Equal(t, "round-trip", decoded["name"])
	require.Contains(t, decodedBody, "正文内容")

	agent, err := decodeAgent(spec, body)
	require.NoError(t, err)
	require.Equal(t, "round-trip", agent.Name)
	require.NotNil(t, agent.Model.Temperature)
	require.InDelta(t, 0.0, *agent.Model.Temperature, 1e-9)
	require.Equal(t, []string{"Bash"}, agent.Tools.Builtin)
}

func TestCreateTeamWithoutBindLeavesFlowAlone(t *testing.T) {
	f := newFixture(t)

	flowBefore := f.read(t, "flows/default.yml")

	result, err := f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeCreate,
		Spec: map[string]any{
			"id": "unbound_team",
			"calls": map[string]any{
				"answer": map[string]any{"type": "agent", "agent": "assistant"},
			},
		},
	})
	require.NoError(t, err)

	require.Empty(t, result.BoundToFlow)
	require.Equal(t, flowBefore, f.read(t, "flows/default.yml"),
		"a team without bind must not rewrite the flow")
	require.NotContains(t, result.Files, filepath.Join(f.root, "flows", "default.yml"))
	require.Contains(t, f.store.Snapshot().Teams, "unbound_team")
	require.NoError(t, func() error { _, err := os.Stat(f.flowPath); return err }())
}

func TestUpsertTeamWritesBackToTheExistingFile(t *testing.T) {
	f := newFixture(t)

	// The file stem and the declared id disagree, which the loader allows. An
	// upsert addresses the team by id but must write back to the file that holds
	// it — otherwise a second file would appear and the flow would keep
	// resolving to the stale one.
	f.write(t, "teams/odd-name.yml", `
id: odd_team
calls:
  answer:
    type: agent
    agent: assistant
`)
	f.reload(t)

	_, err := f.writer(t).CreateTeam(ctx(), CreateTeamRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{"id": "odd_team", "goal": "改写后的目标。"},
	})
	require.NoError(t, err)

	require.False(t, f.exists("teams/odd_team.yml"),
		"the upsert must not mint a second file for the same team")
	require.Contains(t, f.read(t, "teams/odd-name.yml"), "改写后的目标。")
	require.Equal(t, "改写后的目标。", f.load(t).Teams["odd_team"].Goal)
}

func TestCreateAgentDeleteIsNotSupportedByUpsert(t *testing.T) {
	f := newFixture(t)

	// An upsert that omits a whole section leaves it alone; there is no way to
	// express "remove this" through the merge, which is the documented trade-off
	// of "absent keeps".
	_, err := f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeUpsert,
		Spec: map[string]any{"name": "assistant"},
	})
	require.NoError(t, err)

	agent := f.store.Snapshot().Agents["assistant"]
	require.NotEmpty(t, agent.Tools.Builtin)
	require.True(t, agent.HITL != nil)
	require.Contains(t, agent.Body, "你是助手")
}

func TestApplyLeavesNoTempFiles(t *testing.T) {
	f := newFixture(t)

	_, err := f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeCreate,
		Spec: map[string]any{"name": "temp_check"},
	})
	require.NoError(t, err)

	require.NoError(t, filepath.WalkDir(f.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		require.NotContains(t, entry.Name(), ".heron-tmp-",
			"the atomic write must clean up its temp file")
		return nil
	}))
}

// TestApplyPreservesRuntimeLimitsAcrossReload guards the loader base directory.
//
// ConfigLoader reads definition files relative to the config root but reads
// ".agents/settings.json" relative to its BASE directory. A writer that rooted
// its loader at the config root would find no settings file, silently fall back
// to RuntimeLimits defaults, and — because apply() publishes the validated tree
// — reset the running engine's limits on every Define call. The unit tests that
// inject a synthetic loader could not catch this; only the real loader can.
func TestApplyPreservesRuntimeLimitsAcrossReload(t *testing.T) {
	f := newFixture(t)
	settings := `{"runtime":{"max_agent_rounds":3,"max_team_turns":7}}`
	f.write(t, "settings.json", settings)

	// Build the baseline the way the ENGINE does, not the way the fixture helper
	// does: ConfigLoader reads ".agents/settings.json" relative to its base, and
	// the real config root IS that ".agents" directory, so the base is its
	// parent. (fixture.load() anchors at the root itself, which is fine for
	// settings-free trees and wrong for this test.)
	base := filepath.Dir(f.root)
	before, err := config.NewConfigLoader(base).LoadDefinitions(
		context.Background(),
		config.DefinitionsLoadRequest{FlowPath: f.flowPath},
	)
	require.NoError(t, err)
	require.Equal(t, 3, before.Limits.MaxAgentRounds, "fixture baseline")
	require.Equal(t, 7, before.Limits.MaxTeamTurns, "fixture baseline")
	f.store = types.NewDefinitionStore(before, f.root, f.flowPath)

	_, err = f.writer(t).CreateAgent(ctx(), CreateAgentRequest{
		Mode: ModeCreate,
		Spec: map[string]any{"name": "limits-probe", "body": "probe"},
	})
	require.NoError(t, err)

	after := f.store.Snapshot()
	require.Equal(t, 3, after.Limits.MaxAgentRounds,
		"a Define call must not reset runtime limits; writer loader is anchored wrong")
	require.Equal(t, 7, after.Limits.MaxTeamTurns,
		"a Define call must not reset runtime limits; writer loader is anchored wrong")
}
