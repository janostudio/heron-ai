package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// The Bash gate (bash_gate.go) withholds Bash from an agent that declares it
// AND has a private knowledge tree. These tests pin the two directions that
// matter — withheld when there is knowledge to protect, GRANTED when there is
// not — plus the boundary definition, the fail-closed behaviour, the
// not-advertised property, and the fact that the gate is scoped to Bash alone.
//
// The granted direction is the one that should worry a reader: an over-eager
// gate is a silent capability regression for every existing workspace that uses
// Bash for ordinary work, and unlike a missing feature it has no error to
// notice. TestBashAllowedWithoutPrivateKnowledge is the regression guard.

// bashGateEntry is a valid knowledge file body for agent owner.
//
// The frontmatter must name the owner in scope.agents, because R1's validation
// (MarkdownStore.reconcileScope) rejects a file whose declared scope contradicts
// the location it lives in — including the case of claiming agent-private scope
// without naming an agent. A fixture that skipped that would fail to load, and
// the gate would (correctly) fail closed, so the test would be measuring the
// wrong thing.
func bashGateEntry(id, owner string) string {
	return "---\nid: " + id + "\ntitle: " + id + "\nscope:\n  type: agent\n  agents:\n    - " + owner +
		"\nstatus: active\n---\n\nBODY " + id + "\n"
}

// bashGateLoop builds a TurnLoop wired to a file store in dir, which is the
// minimum the gate needs to answer. Everything else the loop requires to Run is
// supplied by the caller.
func bashGateLoop(dir string) (*TurnLoop, storage.FileStore) {
	files := storage.NewFileStore(dir)
	loop := &TurnLoop{files: files}
	return loop, files
}

// writePrivateKnowledge writes one loadable entry into agent agentID's private
// knowledge tree.
func writePrivateKnowledge(t *testing.T, files storage.FileStore, agentID, id string) {
	t.Helper()
	path := privateKnowledgeDir(agentID) + "/" + id + ".md"
	require.NoError(t, files.Write(path, []byte(bashGateEntry(id, agentID))))
}

// TestBashDeniedForAgentWithPrivateKnowledge is the headline property: an agent
// that both declares Bash and owns private knowledge does not get Bash.
//
// Asserted at BOTH enforcement points, because they are independently
// reachable: the schema list (what the model is told exists) and toolDecision
// (what happens if it asks anyway).
func TestBashDeniedForAgentWithPrivateKnowledge(t *testing.T) {
	loop, files := bashGateLoop(t.TempDir())
	agent := types.AgentConfig{
		Name:  "holder",
		Tools: types.ToolConfig{Builtin: []string{"Bash", "Read"}},
	}
	writePrivateKnowledge(t, files, "holder", "secret")

	ctx := context.Background()

	// Enforcement point 1: the policy funnel every call path passes through.
	decision, reason, denied := loop.knowledgeGateDecision(ctx, agent, types.AgentRequest{}, types.ToolCall{Name: "Bash"})
	require.True(t, denied, "Bash must be refused for an agent with private knowledge")
	assert.Equal(t, ToolDeny, decision)
	assert.Contains(t, reason, ".agents/agents/<id>/knowledge",
		"the reason must name the cause so it is actionable")
	assert.Contains(t, reason, "tools.builtin",
		"the reason must name at least one way out")

	// The refusal must survive being asked through toolDecision, which is what
	// the three real call paths use.
	loop.toolPolicy = NewDefaultToolPolicy()
	decision, reason, err := loop.toolDecision(ctx, agent, types.AgentRequest{}, types.ToolCall{Name: "Bash"})
	require.NoError(t, err)
	assert.Equal(t, ToolDeny, decision)
	assert.Contains(t, reason, "private knowledge")

	// Enforcement point 2: not advertised.
	schemas := loop.buildToolSchemasFiltered(ctx, agent, mustWithheld(t, loop, agent))
	assert.NotContains(t, schemaNames(schemas), "Bash")
}

// TestBashAllowedWithoutPrivateKnowledge is the regression guard against
// over-denial: an agent that declares Bash but has no private knowledge keeps
// it, exactly as before this gate existed.
//
// This is the direction that is easy to break and hard to notice — a gate that
// denies too much produces no error, just an agent that mysteriously cannot run
// commands. It must hold both when there is no knowledge tree at all and when
// the agent has private knowledge but does not declare Bash.
func TestBashAllowedWithoutPrivateKnowledge(t *testing.T) {
	ctx := context.Background()

	t.Run("no private tree at all", func(t *testing.T) {
		loop, _ := bashGateLoop(t.TempDir())
		agent := types.AgentConfig{
			Name:  "plain",
			Tools: types.ToolConfig{Builtin: []string{"Bash"}},
		}

		decision, _, denied := loop.knowledgeGateDecision(ctx, agent, types.AgentRequest{}, types.ToolCall{Name: "Bash"})
		assert.False(t, denied, "an agent with no private knowledge must keep Bash")
		assert.Equal(t, ToolAllow, decision)

		schemas := loop.buildToolSchemasFiltered(ctx, agent, mustWithheld(t, loop, agent))
		assert.Contains(t, schemaNames(schemas), "Bash", "Bash must still be advertised")
	})

	t.Run("private tree but no Bash declared", func(t *testing.T) {
		loop, files := bashGateLoop(t.TempDir())
		agent := types.AgentConfig{
			Name:  "reader",
			Tools: types.ToolConfig{Builtin: []string{"Read", "Grep"}},
		}
		writePrivateKnowledge(t, files, "reader", "secret")

		// The gate is scoped to the intersection: knowledge alone does not
		// trigger it, so nothing is withheld and nothing else is taken away.
		withheld, err := loop.withheldTools(ctx, agent, types.AgentRequest{})
		require.NoError(t, err)
		assert.Empty(t, withheld, "an agent that does not declare Bash has nothing withheld")
	})

	t.Run("no file store wired", func(t *testing.T) {
		// A loop built without SetFileStore cannot evaluate the gate. It must
		// grant rather than deny — the store is what would have made private
		// knowledge reachable, so its absence cannot be leaking one, and the
		// ~30 test call sites that build bare loops depend on this.
		loop := &TurnLoop{}
		agent := types.AgentConfig{
			Name:  "plain",
			Tools: types.ToolConfig{Builtin: []string{"Bash"}},
		}
		withheld, err := loop.withheldTools(ctx, agent, types.AgentRequest{})
		require.NoError(t, err)
		assert.Empty(t, withheld)
	})
}

// TestBashDenialBoundaryIsLoadableEntries pins the definition of "has private
// knowledge" to loadable entries rather than directory presence, with the three
// cases that distinguish the two readings.
//
// The boundary is not cosmetic. Under "the directory exists", case 2 would
// withhold Bash for an agent whose only private file is the much-used (and
// content-free) index.md, trading a real capability for an empty guarantee.
// Under "loadable entries", the gate withholds exactly when there is a document
// the path filter is hiding from everyone else — which is the thing Bash could
// reach, and therefore the only thing worth withholding it for.
func TestBashDenialBoundaryIsLoadableEntries(t *testing.T) {
	ctx := context.Background()

	t.Run("absent directory: no knowledge", func(t *testing.T) {
		loop, _ := bashGateLoop(t.TempDir())
		has, ok := hasPrivateKnowledge(ctx, loop.files, "holder")
		require.True(t, ok, "an absent tree is a normal state, not a failure")
		assert.False(t, has)
	})

	t.Run("empty directory: no knowledge", func(t *testing.T) {
		loop, files := bashGateLoop(t.TempDir())
		// A directory with no entries in it. Created by writing and then
		// deleting the file, so the directory itself remains — which is
		// exactly the state "directory exists" would have counted.
		writePrivateKnowledge(t, files, "holder", "gone")
		require.NoError(t, files.Delete(privateKnowledgeDir("holder")+"/gone.md"))

		has, ok := hasPrivateKnowledge(ctx, loop.files, "holder")
		require.True(t, ok)
		assert.False(t, has, "an empty tree protects nothing, so it must not withhold Bash")
	})

	t.Run("index.md only: no knowledge", func(t *testing.T) {
		loop, files := bashGateLoop(t.TempDir())
		// index.md is the human-readable listing MarkdownStore.Load skips. It
		// is not an entry, so a tree holding only it has nothing to protect.
		require.NoError(t, files.Write(
			privateKnowledgeDir("holder")+"/index.md",
			[]byte("- [something](something.md)\n"),
		))

		has, ok := hasPrivateKnowledge(ctx, loop.files, "holder")
		require.True(t, ok)
		assert.False(t, has, "index.md is not a knowledge entry")
	})

	t.Run("one entry: has knowledge", func(t *testing.T) {
		loop, files := bashGateLoop(t.TempDir())
		writePrivateKnowledge(t, files, "holder", "secret")

		has, ok := hasPrivateKnowledge(ctx, loop.files, "holder")
		require.True(t, ok)
		assert.True(t, has)

		agent := types.AgentConfig{Name: "holder", Tools: types.ToolConfig{Builtin: []string{"Bash"}}}
		withheld, err := loop.withheldTools(ctx, agent, types.AgentRequest{})
		require.NoError(t, err)
		assert.Contains(t, withheld, "Bash")
	})

	t.Run("index.md plus one entry: has knowledge", func(t *testing.T) {
		loop, files := bashGateLoop(t.TempDir())
		writePrivateKnowledge(t, files, "holder", "secret")
		require.NoError(t, files.Write(
			privateKnowledgeDir("holder")+"/index.md",
			[]byte("- [secret](secret.md)\n"),
		))
		has, ok := hasPrivateKnowledge(ctx, loop.files, "holder")
		require.True(t, ok)
		assert.True(t, has)
	})

	t.Run("archived-only entry: no knowledge", func(t *testing.T) {
		// Load skips archived entries, and Grep/Glob cannot see them either
		// (the archive lives outside the active tree). The gate agreeing with
		// the loader here is the property that keeps the two from drifting.
		loop, files := bashGateLoop(t.TempDir())
		archived := "---\nid: old\ntitle: old\nscope:\n  type: agent\n  agents:\n    - holder\nstatus: archived\n---\n\nOLD\n"
		require.NoError(t, files.Write(privateKnowledgeDir("holder")+"/old.md", []byte(archived)))

		has, ok := hasPrivateKnowledge(ctx, loop.files, "holder")
		require.True(t, ok)
		assert.False(t, has, "an archived-only tree has no loadable entry, so it protects nothing")
	})

	t.Run("other agent's tree does not gate me", func(t *testing.T) {
		// The question is about the agent's OWN tree. Another owner's tree is
		// unreachable to it by the path filter, so it is not this agent's
		// private knowledge and must not cost it Bash.
		loop, files := bashGateLoop(t.TempDir())
		writePrivateKnowledge(t, files, "someone-else", "secret")

		has, ok := hasPrivateKnowledge(ctx, loop.files, "holder")
		require.True(t, ok)
		assert.False(t, has, "another agent's private tree is not this agent's to protect")
	})
}

// erroringFileStore reports its tree as existing but fails every listing, which
// is the shape of a permission failure on a directory that is really there.
//
// List rather than Read is the method that matters: MarkdownStore.Load walks
// the tree with List first (listMarkdown) and only reads files it found, so an
// unreadable directory fails there. Overriding Read alone would produce a store
// whose List succeeds and returns nothing — an empty tree, not a broken one —
// and the test would be asserting the wrong branch.
type erroringFileStore struct {
	storage.FileStore
	err error
}

func (e erroringFileStore) List(string) ([]string, error) { return nil, e.err }
func (e erroringFileStore) Exists(string) bool            { return true }

// TestBashDenialFailsClosedOnReadError pins the fail-closed direction: a tree
// that exists but cannot be read denies Bash rather than granting it.
//
// The asymmetry is the whole argument. Treating an unreadable tree as empty
// grants Bash in a state the engine never inspected, and the leak that follows
// has no symptom — it looks like ordinary tool output. Treating it as non-empty
// costs one agent one tool and produces a refusal that names the read failure.
// Fail where the failure is visible.
func TestBashDenialFailsClosedOnReadError(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	store := erroringFileStore{FileStore: storage.NewFileStore(base), err: errors.New("permission denied")}

	// Exists is overridden to true, so this exercises the Load path: the tree
	// is there and the walk over it fails.
	has, ok := hasPrivateKnowledge(ctx, store, "holder")
	assert.True(t, has, "an unreadable tree must be treated as having knowledge")
	assert.False(t, ok, "and the caller must be able to tell it was not a clean answer")

	loop := &TurnLoop{files: store}
	agent := types.AgentConfig{Name: "holder", Tools: types.ToolConfig{Builtin: []string{"Bash"}}}

	decision, reason, denied := loop.knowledgeGateDecision(ctx, agent, types.AgentRequest{}, types.ToolCall{Name: "Bash"})
	require.True(t, denied, "fail closed: the call must be refused")
	assert.Equal(t, ToolDeny, decision)
	assert.Contains(t, reason, "could not be read",
		"the refusal must say the tree was unreadable, not that it was empty")

	// And it must also disappear from the schema, so the model is not offered a
	// tool that will be refused.
	withheld, err := loop.withheldTools(ctx, agent, types.AgentRequest{})
	require.NoError(t, err)
	assert.Contains(t, withheld, "Bash")
}

// mustWithheld is the schema-layer helper: it computes the withheld set the way
// Run does (withheldTools) and fails the test if that reported an error, so a
// schema assertion never silently runs against a nil set.
func mustWithheld(t *testing.T, loop *TurnLoop, agent types.AgentConfig) map[string]string {
	t.Helper()
	withheld, err := loop.withheldTools(context.Background(), agent, types.AgentRequest{})
	require.NoError(t, err)
	return withheld
}

// schemaNames lists the tool names a schema slice advertises.
func schemaNames(schemas []types.JSONSchema) []string {
	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema.Name)
	}
	return names
}

// TestBashDenialIsNotAdvertisedToTheModel is the "not advertised" property, and
// it is the reason the gate is enforced at the schema layer and not only at
// call time.
//
// A tool the model can see but not call is worse than an absent one. The model
// spends a round trip on the refusal, and — because the refusal arrives as a
// tool error like any other — it cannot distinguish "this agent may not use
// Bash" from "that command was rejected", so a capable agent will retry with a
// different phrasing. Removing it from the schema makes the capability genuinely
// absent.
func TestBashDenialIsNotAdvertisedToTheModel(t *testing.T) {
	ctx := context.Background()
	loop, files := bashGateLoop(t.TempDir())
	// An agent declaring several tools, so the assertion is that Bash was
	// removed rather than that the list came back empty.
	agent := types.AgentConfig{
		Name:  "holder",
		Tools: types.ToolConfig{Builtin: []string{"Bash", "Read", "Write", "Grep", "Glob"}},
	}
	writePrivateKnowledge(t, files, "holder", "secret")

	schemas := loop.buildToolSchemasFiltered(ctx, agent, mustWithheld(t, loop, agent))
	names := schemaNames(schemas)

	assert.NotContains(t, names, "Bash", "a withheld tool must not be advertised")
	assert.Contains(t, names, "Read", "and the other declared tools must survive")
	assert.Contains(t, names, "Write")
	assert.Contains(t, names, "Grep")
	assert.Contains(t, names, "Glob")

	// Non-vacuous: without the withheld set, Bash IS advertised. This is what
	// makes the assertion above meaningful — it fails if the filtering is
	// bypassed, which is the negative-control property.
	unfiltered := schemaNames(loop.buildToolSchemasFiltered(ctx, agent, nil))
	assert.Contains(t, unfiltered, "Bash",
		"with nothing withheld, Bash is advertised — so the assertion above is not vacuous")
}

// TestOtherToolsUnaffectedByBashGate guards the scope of the gate: withholding
// Bash must not touch the file tools, which are the ones that carry the path
// restriction and are therefore the safe way to reach knowledge.
//
// This is the property that makes the gate a usable answer rather than a
// dead end: an agent keeps Read/Write/Grep/Glob/CodeNav and can still work with
// its own private knowledge and the shared tree, and only Bash — the one tool
// that cannot be restricted — is taken away.
func TestOtherToolsUnaffectedByBashGate(t *testing.T) {
	ctx := context.Background()
	loop, files := bashGateLoop(t.TempDir())
	agent := types.AgentConfig{
		Name: "holder",
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "Read", "Write", "Grep", "Glob", "CodeNav"},
		},
	}
	writePrivateKnowledge(t, files, "holder", "secret")

	loop.toolPolicy = NewDefaultToolPolicy()
	withheld := mustWithheld(t, loop, agent)
	require.Equal(t, map[string]string{"Bash": withheld["Bash"]}, withheld,
		"only Bash may be withheld, though the reason is carried alongside it")

	for _, name := range []string{"Read", "Write", "Grep", "Glob", "CodeNav"} {
		decision, reason, denied := loop.knowledgeGateDecision(ctx, agent, types.AgentRequest{}, types.ToolCall{Name: name})
		assert.False(t, denied, "%s must not be gated by the Bash rule", name)
		assert.Equal(t, ToolAllow, decision, "%s", name)
		assert.Empty(t, reason, "%s", name)

		schemas := loop.buildToolSchemasFiltered(ctx, agent, withheld)
		assert.Contains(t, schemaNames(schemas), name, "%s must stay advertised", name)

		// And through the real funnel, which also exercises the default policy.
		decision, reason, err := loop.toolDecision(ctx, agent, types.AgentRequest{}, types.ToolCall{Name: name})
		require.NoError(t, err)
		assert.NotEqual(t, ToolDeny, decision, "%s must survive the real decision path (%s)", name, reason)
	}
}

// TestBashGateAppliesToEveryCallPath is the test that justifies gating in
// toolDecision rather than in executeToolCalls' allowlist.
//
// executeToolCalls is only one of three routes from a model ToolCall to an
// executed Tool; the async dispatcher and the approval resume both reach the
// executor without touching the allowlist. Driving the resume path here — a
// checkpoint-approved Bash call — proves the gate covers a route the allowlist
// would have missed, which is the difference between the hole being closed and
// the common case being closed.
func TestBashGateAppliesToEveryCallPath(t *testing.T) {
	ctx := context.Background()
	loop, files := bashGateLoop(t.TempDir())
	agent := types.AgentConfig{
		Name:  "holder",
		Tools: types.ToolConfig{Builtin: []string{"Bash"}},
	}
	writePrivateKnowledge(t, files, "holder", "secret")
	loop.toolPolicy = NewDefaultToolPolicy()

	// The approval-resume path (runtime.go, the checkpoint.PendingApproval
	// branch) re-checks policy before executing. Simulate exactly that call:
	// a Bash call that was stored earlier and is being approved now.
	decision, reason, err := loop.toolDecision(ctx, agent, types.AgentRequest{
		AgentID: "holder",
	}, types.ToolCall{Name: "Bash", Arguments: map[string]any{"command": "cat .agents/agents/other/knowledge/secret.md"}})
	require.NoError(t, err)
	assert.Equal(t, ToolDeny, decision,
		"the resume path must refuse a stored Bash call once the agent has private knowledge")
	assert.Contains(t, reason, "private knowledge")
}

// TestBashGateKeysOffTheScopeOwnerNotTheDefinitionName is the guard for a
// fail-open that is easy to write and impossible to notice.
//
// The private tree the path filter protects is ToolPathRestriction's
// .agents/agents/<scope.AgentID>/knowledge, and scope.AgentID is
// agentIDOf(agent, req) — the request's AgentID wins over the definition's
// Name. A spawned child is exactly the case where they differ: it runs under
// the target agent's definition but carries its own request id.
//
// Resolving the gate's directory from agent.Name instead would check a
// different tree than the filter is hiding. In the arrangement below that is
// fail-open: the definition "worker" has no private tree, but the request id
// "child-7" does, so a Name-keyed gate would find nothing, grant Bash, and hand
// the model a shell into a tree the file tools are refusing it.
func TestBashGateKeysOffTheScopeOwnerNotTheDefinitionName(t *testing.T) {
	ctx := context.Background()
	loop, files := bashGateLoop(t.TempDir())
	// The private knowledge belongs to the REQUEST id, not the definition name.
	writePrivateKnowledge(t, files, "child-7", "secret")

	agent := types.AgentConfig{
		Name:  "worker",
		Tools: types.ToolConfig{Builtin: []string{"Bash"}},
	}
	req := types.AgentRequest{AgentID: "child-7"}

	// agentIDOf must be the id the fixture was written against, or the test is
	// asserting the wrong arrangement.
	require.Equal(t, "child-7", agentIDOf(agent, req))

	decision, reason, denied := loop.knowledgeGateDecision(ctx, agent, req, types.ToolCall{Name: "Bash"})
	require.True(t, denied,
		"the gate must key off the scope owner, not the definition name; a Name-keyed gate fails open here")
	assert.Equal(t, ToolDeny, decision)
	assert.Contains(t, reason, "private knowledge")

	withheld, err := loop.withheldTools(ctx, agent, req)
	require.NoError(t, err)
	assert.Contains(t, withheld, "Bash")

	schemas := loop.buildToolSchemasFiltered(ctx, agent, withheld)
	assert.NotContains(t, schemaNames(schemas), "Bash")

	// Control: the same agent with a request id that owns nothing keeps Bash,
	// so the assertion above is about the id and not about withholding
	// everything.
	other := types.AgentRequest{AgentID: "child-8"}
	decision, _, denied = loop.knowledgeGateDecision(ctx, agent, other, types.ToolCall{Name: "Bash"})
	assert.False(t, denied, "a request id with no private tree keeps Bash")
	assert.Equal(t, ToolAllow, decision)
}

// TestBashGateEndToEndTurn drives a complete AgentTurn and asserts the model is
// never offered Bash. It is the integration view of the schema assertion above:
// buildToolSchemas is the only thing that fills the model's tool list, so a
// turn whose agent has private knowledge must issue a model call with no Bash
// in it.
func TestBashGateEndToEndTurn(t *testing.T) {
	dir := t.TempDir()
	files := storage.NewFileStore(dir)
	writePrivateKnowledge(t, files, "holder", "secret")

	model := &mockModelProvider{
		responses: []types.ChatResponse{{Text: "done"}},
	}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hi"}}},
	)
	loop.SetFileStore(files)

	result, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "holder",
		Tools: types.ToolConfig{Builtin: []string{"Bash", "Read"}},
		Loop:  types.LoopConfig{MaxRounds: 1},
	}, types.AgentRequest{
		CallID:  "call-gate",
		AgentID: "holder",
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// The model provider recorded the tool list it was handed. Bash must not be
	// in it — this is the property that matters to the model's behaviour, and
	// the schema unit test only proves the function it goes through.
	require.NotNil(t, model.lastTools, "the model must have been called with a tool list")
	assert.NotContains(t, schemaNames(model.lastTools), "Bash",
		"the end-to-end turn must not advertise Bash")
	assert.Contains(t, schemaNames(model.lastTools), "Read",
		"and must still advertise the other declared tools")
}
