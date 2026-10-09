package eval

import (
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestGraphPathFromCallTurn(t *testing.T) {
	facts := newFixture(t).build().observe()
	graph := facts.Graph

	path := graph.Path(fixtureTeam1 + ":searcher")
	if len(path) != 4 {
		t.Fatalf("expected a 4-node path from the call turn to the session, got %d: %v", len(path), nodeIDs(path))
	}
	want := []string{fixtureSession, fixtureFlow1, fixtureTeam1, fixtureTeam1 + ":searcher"}
	for i, node := range path {
		if node.ID != want[i] {
			t.Fatalf("path[%d] = %s, want %s", i, node.ID, want[i])
		}
	}
	if path[0].Kind != NodeFlowSession || path[1].Kind != NodeFlowTurn ||
		path[2].Kind != NodeTeamTurn || path[3].Kind != NodeCallTurn {
		t.Fatalf("unexpected path kinds: %v", nodeKinds(path))
	}
}

func TestGraphAncestorsAndDescendants(t *testing.T) {
	facts := newFixture(t).build().observe()
	graph := facts.Graph

	ancestors := graph.Ancestors(fixtureTeam1 + ":build")
	if len(ancestors) != 3 {
		t.Fatalf("expected 3 ancestors, got %v", nodeIDs(ancestors))
	}

	// A FlowTurn owns the TeamTurns it caused even though those TeamTurns
	// keep their state in a TeamSession owned by the FlowSession.
	descendants := graph.Descendants(fixtureFlow1)
	want := []string{fixtureTeam1, fixtureTeam1 + ":searcher", fixtureTeam1 + ":build", fixtureTeam2, fixtureTeam2 + ":answer"}
	if got := nodeIDs(descendants); !equalStrings(got, want) {
		t.Fatalf("descendants of %s = %v, want %v", fixtureFlow1, got, want)
	}

	// The TeamSession subtree is the sessions only.
	sessionChildren := nodeIDs(graph.Descendants("fs_eval_fixture:research"))
	if !equalStrings(sessionChildren, []string{"fs_eval_fixture:research:searcher"}) {
		t.Fatalf("team session descendants = %v", sessionChildren)
	}
}

func TestGraphNodeScope(t *testing.T) {
	facts := newFixture(t).build().observe()
	graph := facts.Graph

	node, ok := graph.Node(fixtureTeam1 + ":searcher")
	if !ok {
		t.Fatal("call turn node missing")
	}
	if node.TeamID != "research" || node.FlowTurnID != fixtureFlow1 || node.TeamTurnID != fixtureTeam1 {
		t.Fatalf("call turn scope = %+v", node)
	}
	if node.CallType != types.CallAgent || node.AgentID != "research-searcher" {
		t.Fatalf("call turn identity = %+v", node)
	}
	if node.Status != types.TurnCompleted {
		t.Fatalf("call turn status = %q, want completed", node.Status)
	}
	if node.StartedAt.IsZero() || node.FinishedAt.IsZero() {
		t.Fatalf("call turn timestamps missing: %v → %v", node.StartedAt, node.FinishedAt)
	}

	build, ok := graph.Node(fixtureTeam1 + ":build")
	if !ok {
		t.Fatal("command call turn node missing")
	}
	if build.CallType != types.CallCommand || build.AgentID != "" {
		t.Fatalf("command call turn identity = %+v", build)
	}
}

func TestGraphTeamTurnScopedToFlowTurn(t *testing.T) {
	facts := newFixture(t).build().observe()
	graph := facts.Graph

	turns := graph.NodesOfKind(NodeTeamTurn)
	if len(turns) != 3 {
		t.Fatalf("expected 3 team turns, got %v", nodeIDs(turns))
	}
	if turns[0].FlowTurnID != fixtureFlow1 || turns[1].FlowTurnID != fixtureFlow1 || turns[2].FlowTurnID != fixtureFlow2 {
		t.Fatalf("team turn flow turns = %v", nodeIDs(turns))
	}
	if turns[1].TeamID != "default" || turns[1].TeamSessionID != "fs_eval_fixture:default" {
		t.Fatalf("team turn scope = %+v", turns[1])
	}
}

func TestGraphAgentSessionParent(t *testing.T) {
	facts := newFixture(t).build().observe()
	graph := facts.Graph

	// The "answer" call runs in two different FlowTurns but has one
	// AgentSession, so the graph must deduplicate it and keep it under the
	// TeamSession rather than under the turn that first touched it.
	node, ok := graph.Node("fs_eval_fixture:default:answer")
	if !ok {
		t.Fatal("agent session node missing")
	}
	if node.Kind != NodeAgentSession {
		t.Fatalf("kind = %s, want %s", node.Kind, NodeAgentSession)
	}
	if node.Parent != "fs_eval_fixture:default" {
		t.Fatalf("agent session parent = %q", node.Parent)
	}
	if node.AgentID != "default-answer" {
		t.Fatalf("agent id = %q", node.AgentID)
	}
}

func TestGraphUnknownID(t *testing.T) {
	facts := newFixture(t).build().observe()
	graph := facts.Graph

	if _, ok := graph.Node("tt_missing"); ok {
		t.Fatal("unknown id should not resolve")
	}
	if path := graph.Path("tt_missing"); len(path) != 0 {
		t.Fatalf("path of unknown id = %v", nodeIDs(path))
	}
	if ancestors := graph.Ancestors("tt_missing"); ancestors != nil {
		t.Fatalf("ancestors of unknown id = %v", nodeIDs(ancestors))
	}
	if descendants := graph.Descendants("tt_missing"); descendants != nil {
		t.Fatalf("descendants of unknown id = %v", nodeIDs(descendants))
	}
}

func TestGraphEventRange(t *testing.T) {
	facts := newFixture(t).build().observe()

	node, ok := facts.Graph.Node(fixtureTeam1 + ":searcher")
	if !ok {
		t.Fatal("call turn node missing")
	}
	if node.FirstSeq == 0 || node.LastSeq < node.FirstSeq {
		t.Fatalf("event range = %d..%d", node.FirstSeq, node.LastSeq)
	}
	inside := 0
	for _, event := range facts.Events {
		if event.Seq >= node.FirstSeq && event.Seq <= node.LastSeq {
			inside++
		}
	}
	if inside == 0 {
		t.Fatal("event range covers no event")
	}
}

func nodeIDs(nodes []*Node) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func nodeKinds(nodes []*Node) []NodeKind {
	kinds := make([]NodeKind, 0, len(nodes))
	for _, node := range nodes {
		kinds = append(kinds, node.Kind)
	}
	return kinds
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
