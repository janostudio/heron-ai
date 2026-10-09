package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// goldenSession is a real captured FlowSession committed under testdata/.
const goldenSession = "fs_831c8e5428a040f7"

func fixtureFacts(t *testing.T) *SessionFacts {
	t.Helper()
	return newFixture(t).build().
		publish(types.SharedRecord{
			RecordID: "research-findings", Kind: "agent_result", Name: "ResearchFindings",
			Scope: types.RecordScopeFlow, Status: types.RecordActive, Revision: 1,
			Producer: types.ProducerRef{
				FlowSessionID: fixtureSession, FlowTurnID: fixtureFlow1,
				TeamID: "research", TeamTurnID: fixtureTeam1,
				CallID: "searcher", CallTurnID: fixtureTeam1 + ":searcher",
			},
		}).
		observe()
}

func TestObserveFlowTurns(t *testing.T) {
	facts := fixtureFacts(t)

	if len(facts.FlowTurns) != 2 {
		t.Fatalf("expected 2 flow turns, got %d", len(facts.FlowTurns))
	}
	first := facts.FlowTurns[0]
	if first.FlowTurnID != fixtureFlow1 || first.Input != "fix the bug" {
		t.Fatalf("first flow turn = %+v", first)
	}
	if first.Status != types.TurnCompleted {
		t.Fatalf("first flow turn status = %q", first.Status)
	}
	if first.Next == nil || first.Next.Action != types.NextProceed {
		t.Fatalf("first flow turn next = %+v", first.Next)
	}
	// One turn, two teams, three calls.
	if !equalStrings(first.Teams, []string{"research", "default"}) {
		t.Fatalf("activated teams = %v", first.Teams)
	}
	if len(first.TeamTurns) != 2 {
		t.Fatalf("expected 2 team turns in %s, got %d", fixtureFlow1, len(first.TeamTurns))
	}
	if len(first.TeamTurns[0].Calls) != 2 || len(first.TeamTurns[1].Calls) != 1 {
		t.Fatalf("call counts = %d / %d", len(first.TeamTurns[0].Calls), len(first.TeamTurns[1].Calls))
	}

	second := facts.FlowTurns[1]
	if second.Status != types.TurnWaitingInput || second.Input != "thanks" {
		t.Fatalf("second flow turn = %+v", second)
	}
}

func TestObserveTokenUsage(t *testing.T) {
	facts := fixtureFacts(t)
	first := facts.FlowTurns[0]

	// Requests[] is the fact source: 100+200 prompt, 10+20 completion for
	// the searcher, plus 60/6 for the answer call. The command call reports
	// no requests, so it contributes nothing.
	if first.TeamTurns[0].Usage.PromptTokens != 300 || first.TeamTurns[0].Usage.CompletionTokens != 30 {
		t.Fatalf("team turn usage = %+v", first.TeamTurns[0].Usage)
	}
	if first.TeamTurns[1].Usage.PromptTokens != 60 {
		t.Fatalf("second team turn usage = %+v", first.TeamTurns[1].Usage)
	}
	if first.Usage.PromptTokens != 360 || first.Usage.CompletionTokens != 36 || first.Usage.TotalTokens != 396 {
		t.Fatalf("flow turn usage = %+v", first.Usage)
	}
	if facts.FlowTurns[1].Usage.PromptTokens != 60 {
		t.Fatalf("second flow turn usage = %+v", facts.FlowTurns[1].Usage)
	}
	// Flow level is summed by the consumer, and the second flow turn reuses
	// the "answer" call id, which is a new call turn with its own usage.
	total := facts.Usage()
	if total.PromptTokens != 420 || total.CompletionTokens != 42 {
		t.Fatalf("session usage = %+v", total)
	}
}

func TestObserveCallFacts(t *testing.T) {
	facts := fixtureFacts(t)
	calls := facts.FlowTurns[0].TeamTurns[0].Calls

	searcher := calls[0]
	if searcher.CallID != "searcher" || searcher.CallType != types.CallAgent {
		t.Fatalf("searcher identity = %+v", searcher)
	}
	if searcher.ModelRequests != 2 || searcher.ToolCalls != 3 {
		t.Fatalf("searcher model/tool counts = %d / %d", searcher.ModelRequests, searcher.ToolCalls)
	}
	// tool_call.failed only exists in agent.jsonl, not in CallResult.
	if searcher.ToolFailures != 1 {
		t.Fatalf("searcher tool failures = %d, want 1", searcher.ToolFailures)
	}
	if len(searcher.Records) != 1 || searcher.Records[0].Name != "SearcherEvidence" {
		t.Fatalf("searcher records = %+v", searcher.Records)
	}

	build := calls[1]
	if build.CallType != types.CallCommand || build.ModelRequests != 0 || build.Usage.TotalTokens != 0 {
		t.Fatalf("command call = %+v", build)
	}
	if build.ToolFailures != 0 {
		t.Fatalf("command tool failures = %d", build.ToolFailures)
	}
}

func TestObserveInterruptedCall(t *testing.T) {
	facts := fixtureFacts(t)
	calls := facts.FlowTurns[1].TeamTurns[0].Calls

	if len(calls) != 2 {
		t.Fatalf("expected 2 calls in the second flow turn, got %d", len(calls))
	}
	reviewer := calls[1]
	if reviewer.CallID != "reviewer" {
		t.Fatalf("second call = %+v", reviewer)
	}
	// Started but never completed: it must still show up, because a call
	// that vanished is exactly what eval should surface.
	if reviewer.Status != types.TurnRunning {
		t.Fatalf("interrupted call status = %q, want running", reviewer.Status)
	}
	// It started but never reached a terminal event, so it has a start and
	// no finish.
	if reviewer.StartedAt.IsZero() || !reviewer.FinishedAt.IsZero() {
		t.Fatalf("interrupted call timestamps = %v → %v", reviewer.StartedAt, reviewer.FinishedAt)
	}
}

func TestObserveWorkspace(t *testing.T) {
	facts := fixtureFacts(t)
	first := facts.FlowTurns[0]

	searcher := first.TeamTurns[0].Calls[0].Workspace
	if !equalStrings(searcher.ReadPaths, []string{"a.go"}) || !equalStrings(searcher.WrittenPaths, []string{"b.go"}) {
		t.Fatalf("searcher workspace = %+v", searcher)
	}
	if !searcher.HasSideEffects {
		t.Fatal("expected the write to count as a side effect")
	}
	if len(searcher.Commands) != 1 || searcher.Commands[0].Command != "go test ./..." || searcher.Commands[0].ExitCode != 0 {
		t.Fatalf("searcher commands = %+v", searcher.Commands)
	}

	// Merged up to the team turn: the command call adds the failed make.
	team := first.TeamTurns[0].Workspace
	if !equalStrings(team.WrittenPaths, []string{"b.go"}) {
		t.Fatalf("team written paths = %v", team.WrittenPaths)
	}
	if len(team.Commands) != 2 || team.Commands[1].Command != "make" || team.Commands[1].ExitCode != 1 {
		t.Fatalf("team commands = %+v", team.Commands)
	}
	if team.Operations != 4 {
		t.Fatalf("team operations = %d, want 4", team.Operations)
	}

	// The flow turn adds nothing of its own, so it matches the team turn.
	if !equalStrings(first.Workspace.WrittenPaths, []string{"b.go"}) || !first.Workspace.HasSideEffects {
		t.Fatalf("flow turn workspace = %+v", first.Workspace)
	}
	if facts.FlowTurns[1].Workspace.HasSideEffects {
		t.Fatal("second flow turn should have no side effects")
	}
}

func TestObserveRecordsAndEvidence(t *testing.T) {
	facts := fixtureFacts(t)
	first := facts.FlowTurns[0]

	if len(first.Records) != 1 {
		t.Fatalf("flow turn records = %+v", first.Records)
	}
	record := first.Records[0]
	if record.Name != "ResearchFindings" || record.Scope != types.RecordScopeFlow || record.Revision != 1 {
		t.Fatalf("record = %+v", record)
	}
	if record.Producer.CallTurnID != fixtureTeam1+":searcher" || record.Producer.FlowTurnID != fixtureFlow1 {
		t.Fatalf("record producer = %+v", record.Producer)
	}
	if len(facts.FlowTurns[1].Records) != 0 {
		t.Fatalf("second flow turn records = %+v", facts.FlowTurns[1].Records)
	}

	// evidence.jsonl is the Flow-scope history, read separately from the
	// event stream.
	if len(facts.Evidence) != 1 || facts.Evidence[0].RecordID != "research-findings" {
		t.Fatalf("evidence = %+v", facts.Evidence)
	}
}

// goldenStore stages the captured session under testdata/ into the
// .agents/data/sessions layout FileFactSource reads. The capture cannot live
// there in the repo because .gitignore excludes **/.agents/data/.
func goldenStore(t *testing.T) storage.FileStore {
	t.Helper()
	root := t.TempDir()
	target := filepath.Join(root, ".agents", "data", "sessions", goldenSession)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("create golden session dir: %v", err)
	}
	for _, name := range []string{"flow.jsonl", "team.jsonl", "agent.jsonl", "evidence.jsonl"} {
		data, err := os.ReadFile(filepath.Join("testdata", goldenSession, name))
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(target, name), data, 0o644); err != nil {
			t.Fatalf("stage golden %s: %v", name, err)
		}
	}
	return storage.NewFileStore(root)
}

func TestObserveGoldenSession(t *testing.T) {
	// A real captured session, byte for byte what the runtime wrote.
	source := NewFileFactSource(goldenStore(t))
	facts, err := NewObserver(source).Observe(context.Background(), goldenSession)
	if err != nil {
		t.Fatalf("observe golden session: %v", err)
	}

	if len(facts.FlowTurns) != 1 {
		t.Fatalf("expected 1 flow turn, got %d", len(facts.FlowTurns))
	}
	turn := facts.FlowTurns[0]
	if turn.Input != "hi" || turn.Status != types.TurnWaitingInput {
		t.Fatalf("golden flow turn = %+v", turn)
	}
	if !equalStrings(turn.Teams, []string{"default"}) {
		t.Fatalf("golden teams = %v", turn.Teams)
	}
	if len(turn.TeamTurns) != 1 || len(turn.TeamTurns[0].Calls) != 1 {
		t.Fatalf("golden call count = %d", len(turn.TeamTurns[0].Calls))
	}
	call := turn.TeamTurns[0].Calls[0]
	if call.CallID != "coordinator" || call.AgentID != "default-coordinator" {
		t.Fatalf("golden call = %+v", call)
	}
	if call.Usage.PromptTokens != 2136 || call.Usage.CompletionTokens != 72 || call.Usage.TotalTokens != 2208 {
		t.Fatalf("golden usage = %+v", call.Usage)
	}
	if turn.Usage.TotalTokens != 2208 || facts.Usage().TotalTokens != 2208 {
		t.Fatalf("golden aggregated usage = %+v / %+v", turn.Usage, facts.Usage())
	}
	if len(turn.Records) != 1 || turn.Records[0].Name != "CoordinatorReply" {
		t.Fatalf("golden records = %+v", turn.Records)
	}
	if len(facts.Evidence) != 1 || facts.Evidence[0].Name != "CoordinatorReply" {
		t.Fatalf("golden evidence = %+v", facts.Evidence)
	}

	// Correlation on real bytes: the record producer points back at the
	// call turn that produced it.
	producer := turn.Records[0].Producer
	node, ok := facts.Graph.Node(producer.CallTurnID)
	if !ok {
		t.Fatalf("producer call turn %q not in graph", producer.CallTurnID)
	}
	if node.Parent != turn.TeamTurns[0].TeamTurnID || node.TeamID != "default" {
		t.Fatalf("producer node = %+v", node)
	}
}

func TestObserveMissingSession(t *testing.T) {
	source := NewFileFactSource(storage.NewFileStore(t.TempDir()))
	facts, err := NewObserver(source).Observe(context.Background(), "fs_missing")
	if err != nil {
		t.Fatalf("a session with no persisted events is empty, not an error: %v", err)
	}
	if len(facts.FlowTurns) != 0 || len(facts.Events) != 0 || len(facts.Evidence) != 0 {
		t.Fatalf("expected empty facts, got %+v", facts)
	}
	if facts.Usage().TotalTokens != 0 {
		t.Fatal("empty session should have no usage")
	}
}

func TestObserveRequiresSessionID(t *testing.T) {
	if _, err := NewObserver(NewFileFactSource(storage.NewFileStore(t.TempDir()))).Observe(context.Background(), " "); err == nil {
		t.Fatal("expected an error for a blank flow session id")
	}
}
