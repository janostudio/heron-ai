package eval

import (
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// SessionFacts is the Phase-1 observation of one FlowSession: the correlation
// graph plus the facts derived per FlowTurn.
//
// It is deliberately narrower than the Observation Report in
// docs/generic-engine/15 §4. That section also specifies goal/acceptance,
// budget limits, duplicate-read detection and basis completeness. None of
// those can be derived from the event stream alone — they need the Flow
// definition and the workspace revision history — so they stay out of Phase 1
// rather than being guessed at. What is here is everything the persisted
// events actually prove.
type SessionFacts struct {
	FlowSessionID string
	// Events is the merged three-layer timeline the facts were derived from.
	Events []storage.SessionEvent
	// Graph is the correlation index over Events.
	Graph *Graph
	// Evidence is the complete evidence.jsonl history: the Flow-scope
	// SharedRecords, including superseded revisions.
	Evidence  []types.SharedRecord
	FlowTurns []FlowTurnFact
}

// FlowTurnFact is one user-visible turn: which Teams it ran, what they cost,
// what they published and what they did to the workspace.
type FlowTurnFact struct {
	FlowTurnID string
	Input      string
	Status     types.TurnStatus
	Next       *types.Route
	// Teams are the Team ids activated during this turn, in execution order.
	Teams     []string
	TeamTurns []TeamTurnFact
	Usage     types.TokenUsage
	// Records are the SharedRecords published with this FlowTurn as their
	// producer, i.e. what the turn handed to its successors.
	Records    []RecordFact
	Workspace  WorkspaceFact
	StartedAt  time.Time
	FinishedAt time.Time
}

// TeamTurnFact is one Team invocation.
type TeamTurnFact struct {
	TeamTurnID    string
	TeamID        string
	TeamSessionID string
	Status        types.TurnStatus
	Next          *types.Route
	Calls         []CallTurnFact
	Usage         types.TokenUsage
	Workspace     WorkspaceFact
	StartedAt     time.Time
	FinishedAt    time.Time
}

// CallTurnFact is one Call execution: an Agent, Command or Webhook turn.
type CallTurnFact struct {
	CallTurnID string
	CallID     string
	CallType   types.CallType
	AgentID    string
	Status     types.TurnStatus
	Usage      types.TokenUsage
	// ModelRequests counts the model requests the call reported. Requests[]
	// is the fact source for consumption, so this is also the number of
	// usage entries summed into Usage.
	ModelRequests int
	ToolCalls     int
	// ToolFailures counts `tool_call.failed` events in agent.jsonl. It is
	// not available from CallResult, which only reports successful tool
	// call counts.
	ToolFailures int
	Workspace    WorkspaceFact
	Records      []RecordFact
	// CheckpointID is non-empty when the call ended waiting and can be
	// resumed from a checkpoint.
	CheckpointID string
	Error        string
	StartedAt    time.Time
	FinishedAt   time.Time
}

// WorkspaceFact is the cwd side-effect summary of a turn or call. Paths are
// de-duplicated on the way up, so a FlowTurn reports each touched path once.
type WorkspaceFact struct {
	Operations int
	ReadPaths  []string
	// WrittenPaths are the paths a write operation targeted. Non-empty means
	// the turn had a real cwd side effect.
	WrittenPaths []string
	Commands     []WorkspaceCommand
	// HasSideEffects is true when at least one write was recorded.
	HasSideEffects bool
}

// WorkspaceCommand is one test or shell command run against the workspace.
type WorkspaceCommand struct {
	Kind     string // test | bash
	Command  string
	ExitCode int
}

// RecordFact is the observation view of a SharedRecord: the envelope the core
// owns, without the business Data payload.
type RecordFact struct {
	RecordID string
	Kind     string
	Name     string
	Scope    types.RecordScope
	Status   types.RecordStatus
	Revision int
	Producer types.ProducerRef
}

// Usage returns the Flow-level token total, summed from the TeamTurns. There
// is no Flow-level usage field in the event stream; per the consumption model
// the consumer computes it, and this is where eval does.
func (s *SessionFacts) Usage() types.TokenUsage {
	var total types.TokenUsage
	for _, turn := range s.FlowTurns {
		addUsage(&total, turn.Usage)
	}
	return total
}

// factIndex holds the payloads a single pass over the timeline extracted,
// keyed so the graph walk can look them up without rescanning.
type factIndex struct {
	callResults  map[string]types.CallResult
	teamResults  map[string]types.TeamTurnResult
	flowTurns    map[string]types.FlowTurn
	teamTurns    map[string]types.TeamTurn
	toolFailures map[string]int
	records      map[string][]RecordFact
}

func newFactIndex(events []storage.SessionEvent) *factIndex {
	index := &factIndex{
		callResults:  make(map[string]types.CallResult),
		teamResults:  make(map[string]types.TeamTurnResult),
		flowTurns:    make(map[string]types.FlowTurn),
		teamTurns:    make(map[string]types.TeamTurn),
		toolFailures: make(map[string]int),
		records:      make(map[string][]RecordFact),
	}
	for _, event := range events {
		switch event.Type {
		case types.EventFlowTurnStarted, types.EventFlowTurnCompleted,
			types.EventFlowTurnWaitingInput, types.EventFlowTurnWaitingTool, types.EventFlowTurnWaitingApproval:
			if turn, ok := payloadAs[types.FlowTurn](event.Payload, "turn"); ok && turn.ID != "" {
				index.flowTurns[turn.ID] = turn
			}
		case types.EventTeamTurnStarted:
			if turn, ok := payloadAs[types.TeamTurn](event.Payload, "team_turn"); ok && turn.ID != "" {
				index.teamTurns[turn.ID] = turn
			}
		case types.EventTeamTurnCompleted, types.EventTeamTurnWaitingInput,
			types.EventTeamTurnWaitingTool, types.EventTeamTurnWaitingApproval:
			if result, ok := payloadAs[types.TeamTurnResult](event.Payload, "team_result"); ok {
				if result.Turn.ID != "" {
					index.teamResults[result.Turn.ID] = result
					index.teamTurns[result.Turn.ID] = result.Turn
				}
			}
		case types.EventAgentTurnCompleted, types.EventCommandTurnCompleted, types.EventWebhookTurnCompleted,
			types.EventAgentTurnWaitingInput, types.EventAgentTurnWaitingTool, types.EventAgentTurnWaitingApproval:
			// A retried call repeats its call_turn_id; the newest result wins,
			// which is also how the runtime folds state on resume.
			if result, ok := payloadAs[types.CallResult](event.Payload, "call_result"); ok {
				id := event.CallTurnID
				if result.CallTurnID != "" {
					id = result.CallTurnID
				}
				index.callResults[id] = result
			}
		case types.EventToolCallFailed:
			if event.CallTurnID != "" {
				index.toolFailures[event.CallTurnID]++
			}
		case types.EventSharedRecordPublished:
			if record, ok := payloadAs[types.SharedRecord](event.Payload, "record"); ok {
				index.records[event.FlowTurnID] = append(index.records[event.FlowTurnID], newRecordFact(record))
			}
		}
	}
	return index
}

func deriveFlowTurns(graph *Graph, events []storage.SessionEvent) []FlowTurnFact {
	index := newFactIndex(events)

	// Group by the graph's ownership links rather than by the scope fields
	// on the nodes: an event that carries both ids establishes the link even
	// if no single event ever carried the enclosing id for this node.
	teamTurnsByFlow := childrenOfKind(graph, NodeFlowTurn, NodeTeamTurn)
	callTurnsByTeam := childrenOfKind(graph, NodeTeamTurn, NodeCallTurn)

	facts := make([]FlowTurnFact, 0, len(graph.NodesOfKind(NodeFlowTurn)))
	for _, node := range graph.NodesOfKind(NodeFlowTurn) {
		fact := FlowTurnFact{
			FlowTurnID: node.ID,
			Status:     node.Status,
			StartedAt:  node.StartedAt,
			FinishedAt: node.FinishedAt,
			Records:    index.records[node.ID],
		}
		if turn, ok := index.flowTurns[node.ID]; ok {
			fact.Input = turn.Input
			fact.Next = turn.Next
		}

		seenTeams := make(map[string]bool)
		for _, teamTurnNode := range teamTurnsByFlow[node.ID] {
			teamTurn := deriveTeamTurn(index, teamTurnNode, callTurnsByTeam[teamTurnNode.ID])
			fact.TeamTurns = append(fact.TeamTurns, teamTurn)
			addUsage(&fact.Usage, teamTurn.Usage)
			fact.Workspace = mergeWorkspace(fact.Workspace, teamTurn.Workspace)
			if teamTurn.TeamID != "" && !seenTeams[teamTurn.TeamID] {
				seenTeams[teamTurn.TeamID] = true
				fact.Teams = append(fact.Teams, teamTurn.TeamID)
			}
		}
		facts = append(facts, fact)
	}
	return facts
}

// childrenOfKind indexes the children of every node of parentKind by their
// parent id, keeping only children of childKind.
func childrenOfKind(graph *Graph, parentKind, childKind NodeKind) map[string][]*Node {
	out := make(map[string][]*Node)
	for _, parent := range graph.NodesOfKind(parentKind) {
		for _, child := range graph.Children(parent.ID) {
			if child.Kind == childKind {
				out[parent.ID] = append(out[parent.ID], child)
			}
		}
	}
	return out
}

func deriveTeamTurn(index *factIndex, node *Node, callNodes []*Node) TeamTurnFact {
	fact := TeamTurnFact{
		TeamTurnID:    node.ID,
		TeamID:        node.TeamID,
		TeamSessionID: node.TeamSessionID,
		Status:        node.Status,
		StartedAt:     node.StartedAt,
		FinishedAt:    node.FinishedAt,
	}
	for _, callNode := range callNodes {
		call := deriveCallTurn(index, callNode)
		fact.Calls = append(fact.Calls, call)
		addUsage(&fact.Usage, call.Usage)
		fact.Workspace = mergeWorkspace(fact.Workspace, call.Workspace)
	}
	if result, ok := index.teamResults[node.ID]; ok {
		fact.Next = result.Next
		if fact.TeamID == "" {
			fact.TeamID = result.Turn.TeamID
		}
		if fact.TeamSessionID == "" {
			fact.TeamSessionID = result.Turn.TeamSessionID
		}
	}
	if fact.Next == nil {
		if turn, ok := index.teamTurns[node.ID]; ok {
			fact.Next = turn.Next
		}
	}
	return fact
}

func deriveCallTurn(index *factIndex, node *Node) CallTurnFact {
	fact := CallTurnFact{
		CallTurnID:   node.ID,
		CallID:       node.CallID,
		CallType:     node.CallType,
		AgentID:      node.AgentID,
		Status:       node.Status,
		ToolFailures: index.toolFailures[node.ID],
		StartedAt:    node.StartedAt,
		FinishedAt:   node.FinishedAt,
	}
	result, ok := index.callResults[node.ID]
	if !ok {
		// Started but never completed: the turn is interrupted or still
		// running. It stays in the report with the status the graph knows,
		// because a vanished call is exactly what eval should surface.
		return fact
	}
	fact.Status = result.Status
	fact.ModelRequests = len(result.Requests)
	fact.ToolCalls = result.ToolCalls
	fact.CheckpointID = result.CheckpointID
	fact.Error = result.Error
	fact.Usage = requestUsage(result)
	fact.Workspace = workspaceFact(result.WorkspaceOps)
	fact.Records = recordFacts(result.Records)
	if result.AgentID != "" {
		fact.AgentID = result.AgentID
	}
	return fact
}

// requestUsage sums the per-request usage the provider reported.
//
// Requests[].usage is the fact source for consumption (see the consumption
// model in CODEBUDDY.md and docs/ARCHITECTURE.md). CallResult.Usage is the
// executor's own total and is only used when a call reported no requests at
// all, which is the normal case for command and webhook calls.
func requestUsage(result types.CallResult) types.TokenUsage {
	if len(result.Requests) == 0 {
		return result.Usage
	}
	var total types.TokenUsage
	for _, request := range result.Requests {
		addUsage(&total, request.Usage)
	}
	return total
}

func workspaceFact(ops []types.WorkspaceOperation) WorkspaceFact {
	fact := WorkspaceFact{}
	for _, op := range ops {
		fact.Operations++
		switch op.Kind {
		case "write":
			fact.WrittenPaths = appendUnique(fact.WrittenPaths, op.Path)
			fact.HasSideEffects = true
		case "read", "search":
			fact.ReadPaths = appendUnique(fact.ReadPaths, op.Path)
		case "test", "bash":
			command := WorkspaceCommand{Kind: op.Kind, Command: op.Command, ExitCode: op.ExitCode}
			if !hasCommand(fact.Commands, command) {
				fact.Commands = append(fact.Commands, command)
			}
		}
	}
	return fact
}

func mergeWorkspace(dst, src WorkspaceFact) WorkspaceFact {
	if src.Operations == 0 {
		return dst
	}
	dst.Operations += src.Operations
	dst.HasSideEffects = dst.HasSideEffects || src.HasSideEffects
	for _, path := range src.ReadPaths {
		dst.ReadPaths = appendUnique(dst.ReadPaths, path)
	}
	for _, path := range src.WrittenPaths {
		dst.WrittenPaths = appendUnique(dst.WrittenPaths, path)
	}
	for _, command := range src.Commands {
		if !hasCommand(dst.Commands, command) {
			dst.Commands = append(dst.Commands, command)
		}
	}
	return dst
}

func recordFacts(records []types.SharedRecord) []RecordFact {
	facts := make([]RecordFact, 0, len(records))
	for _, record := range records {
		facts = append(facts, newRecordFact(record))
	}
	return facts
}

func newRecordFact(record types.SharedRecord) RecordFact {
	return RecordFact{
		RecordID: record.RecordID,
		Kind:     record.Kind,
		Name:     record.Name,
		Scope:    record.Scope,
		Status:   record.Status,
		Revision: record.Revision,
		Producer: record.Producer,
	}
}

func addUsage(dst *types.TokenUsage, src types.TokenUsage) {
	dst.PromptTokens += src.PromptTokens
	dst.CompletionTokens += src.CompletionTokens
	dst.ReasoningTokens += src.ReasoningTokens
	dst.TotalTokens += src.TotalTokens
	dst.PromptCacheHitTokens += src.PromptCacheHitTokens
	dst.PromptCacheMissTokens += src.PromptCacheMissTokens
	dst.CacheReadInputTokens += src.CacheReadInputTokens
	dst.CacheCreationInputTokens += src.CacheCreationInputTokens
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func hasCommand(commands []WorkspaceCommand, command WorkspaceCommand) bool {
	for _, existing := range commands {
		if existing == command {
			return true
		}
	}
	return false
}
