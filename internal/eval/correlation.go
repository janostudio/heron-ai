package eval

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// NodeKind identifies one level of the Flow → Team → Call execution tree.
//
// Relation to docs/generic-engine/15 §3.1: that section lists
// `agent_session_id` and a per-call-type turn id (`AgentTurn` /
// `CommandTurn` / `WebhookTurn`) as separate correlation fields, and its §9
// sketch declares them as three distinct query parameters. Neither exists in
// the published contract. types.EventHeader carries a single `call_turn_id`
// for all three call types, discriminated by `call_type`, and no agent
// session id at all — the AgentSession id only appears inside the
// `agent_session.created` payload. This graph therefore models one call_turn
// node kind plus a separate agent_session kind hanging off the TeamSession,
// which is what the event stream can actually support.
type NodeKind string

const (
	NodeFlowSession  NodeKind = "flow_session"
	NodeFlowTurn     NodeKind = "flow_turn"
	NodeTeamSession  NodeKind = "team_session"
	NodeTeamTurn     NodeKind = "team_turn"
	NodeAgentSession NodeKind = "agent_session"
	NodeCallTurn     NodeKind = "call_turn"
)

// Node is one addressable unit of execution and the index entry that lets a
// caller start from any id it happens to hold — a record producer's
// call_turn_id, a log line's team_turn_id — and walk up or down the tree from
// there.
type Node struct {
	Kind NodeKind
	ID   string
	// Parent is the owning node. Ownership follows lifetime, not causation:
	// a TeamSession outlives the FlowTurns that touch it, so it hangs off
	// the FlowSession, while a TeamTurn hangs off the FlowTurn that caused
	// it. Use FlowTurnID / TeamSessionID to cross the two axes.
	Parent   string
	Children []string

	// Scope ids copied from the event envelope. Every level that applies to
	// this node is populated, so a call_turn node answers "which FlowTurn
	// did this run inside" without walking up the tree.
	FlowSessionID string
	FlowTurnID    string
	TeamSessionID string
	TeamTurnID    string
	TeamID        string
	CallID        string
	CallType      types.CallType
	AgentID       string
	Attempt       int

	// Status is the raw status of the turn or session payload. The two
	// status vocabularies (types.TurnStatus, types.SessionStatus) share
	// their values, so one field covers both.
	Status types.TurnStatus

	// StartedAt / FinishedAt are zero when the event stream never carried
	// the corresponding timestamp. For turns, FinishedAt is set by any
	// terminal event, including the waiting_* ones: a turn that ends
	// waiting is resumable, but it did leave the running state.
	StartedAt  time.Time
	FinishedAt time.Time

	// FirstSeq / LastSeq bound the events addressed to this node in the
	// merged timeline. Only the most specific node an event names gets
	// credit, so a team_turn's range does not cover its calls' events.
	FirstSeq int64
	LastSeq  int64
}

// Graph is the correlation index of one FlowSession's event stream.
type Graph struct {
	nodes map[string]*Node
	order []string
}

// NewGraph folds an event timeline into the execution tree. Events are
// expected in Seq order, which is what storage.SessionWriter.Replay returns.
func NewGraph(events []storage.SessionEvent) *Graph {
	graph := &Graph{nodes: make(map[string]*Node)}
	for _, event := range events {
		graph.addEvent(event)
	}
	return graph
}

// Node looks a node up by id. Ids are unique across kinds by construction
// (each level prefixes its own id), so no kind is needed here.
func (g *Graph) Node(id string) (*Node, bool) {
	node, ok := g.nodes[id]
	return node, ok
}

// NodesOfKind returns every node of one kind in first-seen order.
func (g *Graph) NodesOfKind(kind NodeKind) []*Node {
	nodes := make([]*Node, 0, len(g.order))
	for _, id := range g.order {
		if node := g.nodes[id]; node.Kind == kind {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// Children returns the nodes owned by id, in first-seen order.
func (g *Graph) Children(id string) []*Node {
	parent, ok := g.nodes[id]
	if !ok {
		return nil
	}
	children := make([]*Node, 0, len(parent.Children))
	for _, childID := range parent.Children {
		if child, ok := g.nodes[childID]; ok {
			children = append(children, child)
		}
	}
	return children
}

// Path returns the ownership chain from the root down to id, inclusive. It is
// empty when id is unknown.
func (g *Graph) Path(id string) []*Node {
	var path []*Node
	for current, ok := g.nodes[id]; ok; current, ok = g.nodes[current.Parent] {
		path = append([]*Node{current}, path...)
		if current.Parent == "" {
			break
		}
	}
	return path
}

// Ancestors returns the enclosing nodes of id, root first, excluding id.
func (g *Graph) Ancestors(id string) []*Node {
	path := g.Path(id)
	if len(path) == 0 {
		return nil
	}
	return path[:len(path)-1]
}

// Descendants returns the whole subtree under id in depth-first pre-order,
// excluding id. Because ownership follows lifetime, a TeamSession's
// descendants are its AgentSessions, while a FlowTurn's descendants are the
// TeamTurns it caused and the calls those ran.
func (g *Graph) Descendants(id string) []*Node {
	if _, ok := g.nodes[id]; !ok {
		return nil
	}
	var out []*Node
	var walk func(nodeID string)
	walk = func(nodeID string) {
		for _, child := range g.Children(nodeID) {
			out = append(out, child)
			walk(child.ID)
		}
	}
	walk(id)
	return out
}

// ensure returns the node of kind and id, creating it on first sight. A node
// whose id collides with an existing one of a different kind is left alone:
// silently re-labelling it would corrupt every path through it.
func (g *Graph) ensure(kind NodeKind, id string) *Node {
	if id == "" {
		return nil
	}
	if node, ok := g.nodes[id]; ok {
		if node.Kind != kind {
			return nil
		}
		return node
	}
	node := &Node{Kind: kind, ID: id}
	g.nodes[id] = node
	g.order = append(g.order, id)
	return node
}

// link records ownership once. Later events repeat the same links; the first
// one wins so Children stays in first-seen order.
func (g *Graph) link(child, parent *Node) {
	if child == nil || parent == nil || child == parent {
		return
	}
	if child.Parent != "" {
		return
	}
	child.Parent = parent.ID
	parent.Children = append(parent.Children, child.ID)
}

func (g *Graph) addEvent(event storage.SessionEvent) {
	var flowSession, flowTurn, teamSession, teamTurn, callTurn *Node

	if event.FlowSessionID != "" {
		flowSession = g.ensure(NodeFlowSession, event.FlowSessionID)
	}
	if event.FlowTurnID != "" {
		flowTurn = g.ensure(NodeFlowTurn, event.FlowTurnID)
		g.link(flowTurn, flowSession)
	}
	if event.TeamSessionID != "" {
		teamSession = g.ensure(NodeTeamSession, event.TeamSessionID)
		g.link(teamSession, flowSession)
	}
	if event.TeamTurnID != "" {
		teamTurn = g.ensure(NodeTeamTurn, event.TeamTurnID)
		g.link(teamTurn, flowTurn)
	}
	if event.CallTurnID != "" {
		callTurn = g.ensure(NodeCallTurn, event.CallTurnID)
		g.link(callTurn, teamTurn)
	}

	for _, node := range []*Node{flowSession, flowTurn, teamSession, teamTurn, callTurn} {
		if node != nil {
			applyScope(node, event)
		}
	}

	g.applyTurnLifecycle(event, teamSession)

	if owner := g.owner(event); owner != nil {
		if owner.FirstSeq == 0 || event.Seq < owner.FirstSeq {
			owner.FirstSeq = event.Seq
		}
		if event.Seq > owner.LastSeq {
			owner.LastSeq = event.Seq
		}
	}
}

// owner is the most specific node an event is addressed to.
func (g *Graph) owner(event storage.SessionEvent) *Node {
	switch {
	case event.CallTurnID != "":
		return g.nodes[event.CallTurnID]
	case event.TeamTurnID != "":
		return g.nodes[event.TeamTurnID]
	case event.FlowTurnID != "":
		return g.nodes[event.FlowTurnID]
	default:
		return g.nodes[event.FlowSessionID]
	}
}

func applyScope(node *Node, event storage.SessionEvent) {
	if event.FlowSessionID != "" {
		node.FlowSessionID = event.FlowSessionID
	}
	if event.FlowTurnID != "" {
		node.FlowTurnID = event.FlowTurnID
	}
	if event.TeamSessionID != "" {
		node.TeamSessionID = event.TeamSessionID
	}
	if event.TeamTurnID != "" {
		node.TeamTurnID = event.TeamTurnID
	}
	if event.TeamID != "" {
		node.TeamID = event.TeamID
	}
	if event.CallID != "" {
		node.CallID = event.CallID
	}
	if event.CallType != "" {
		node.CallType = event.CallType
	}
	if event.Attempt > 0 {
		node.Attempt = event.Attempt
	}
}

// applyTurnLifecycle updates the status and timestamps of the node addressed
// by a lifecycle event. Call turns carry no timestamps in their payload
// (types.CallResult has none), so the event's own CreatedAt is the only
// source for them.
func (g *Graph) applyTurnLifecycle(event storage.SessionEvent, teamSession *Node) {
	switch event.Type {
	case types.EventFlowSessionCreated, types.EventFlowSessionUpdated:
		if session, ok := payloadAs[types.FlowSession](event.Payload, "session"); ok {
			g.track(NodeFlowSession, session.ID, event, types.TurnStatus(session.Status), session.CreatedAt, session.UpdatedAt, 0)
		}
	case types.EventTeamSessionCreated, types.EventTeamSessionUpdated:
		if session, ok := payloadAs[types.TeamSession](event.Payload, "team_session"); ok {
			g.track(NodeTeamSession, session.ID, event, types.TurnStatus(session.Status), session.CreatedAt, session.UpdatedAt, 0)
		}
	case types.EventAgentSessionCreated, types.EventAgentSessionUpdated:
		if session, ok := payloadAs[types.AgentSession](event.Payload, "agent_session"); ok {
			g.track(NodeAgentSession, session.ID, event, types.TurnStatus(session.Status), session.CreatedAt, session.UpdatedAt, 0)
			if node := g.nodes[session.ID]; node != nil {
				applyScope(node, event)
				node.TeamSessionID = session.TeamSessionID
				node.CallID = session.CallID
				node.AgentID = session.AgentID
				g.link(node, teamSession)
			}
		}
	case types.EventFlowTurnStarted, types.EventFlowTurnCompleted,
		types.EventFlowTurnWaitingInput, types.EventFlowTurnWaitingTool, types.EventFlowTurnWaitingApproval:
		if turn, ok := payloadAs[types.FlowTurn](event.Payload, "turn"); ok {
			id := turn.ID
			if id == "" {
				id = event.FlowTurnID
			}
			finished := time.Time{}
			if turn.FinishedAt != nil {
				finished = *turn.FinishedAt
			}
			g.track(NodeFlowTurn, id, event, turn.Status, turn.StartedAt, finished, turn.Attempt)
		}
	case types.EventTeamTurnStarted, types.EventTeamTurnCompleted,
		types.EventTeamTurnWaitingInput, types.EventTeamTurnWaitingTool, types.EventTeamTurnWaitingApproval:
		id := event.TeamTurnID
		var status types.TurnStatus
		started := time.Time{}
		finished := time.Time{}
		if turn, ok := payloadAs[types.TeamTurn](event.Payload, "team_turn"); ok {
			if turn.ID != "" {
				id = turn.ID
			}
			status = turn.Status
			started = turn.StartedAt
			if turn.FinishedAt != nil {
				finished = *turn.FinishedAt
			}
		}
		if result, ok := payloadAs[types.TeamTurnResult](event.Payload, "team_result"); ok {
			if result.Turn.ID != "" {
				id = result.Turn.ID
			}
			status = result.Turn.Status
			started = result.Turn.StartedAt
			if result.Turn.FinishedAt != nil {
				finished = *result.Turn.FinishedAt
			}
		}
		g.track(NodeTeamTurn, id, event, status, started, finished, event.Attempt)
	case types.EventAgentTurnStarted, types.EventCommandTurnStarted, types.EventWebhookTurnStarted:
		if call, ok := payloadAs[types.Call](event.Payload, "call"); ok {
			if node := g.ensure(NodeCallTurn, event.CallTurnID); node != nil && node.AgentID == "" {
				node.AgentID = call.AgentID
			}
		}
		g.track(NodeCallTurn, event.CallTurnID, event, turnStatusForEvent(event.Type), time.Time{}, time.Time{}, event.Attempt)
	case types.EventAgentTurnCompleted, types.EventCommandTurnCompleted, types.EventWebhookTurnCompleted:
		if result, ok := payloadAs[types.CallResult](event.Payload, "call_result"); ok {
			id := event.CallTurnID
			if result.CallTurnID != "" {
				id = result.CallTurnID
			}
			g.track(NodeCallTurn, id, event, result.Status, time.Time{}, time.Time{}, event.Attempt)
			if node := g.nodes[id]; node != nil && node.AgentID == "" {
				node.AgentID = result.AgentID
			}
		}
	case types.EventAgentTurnWaitingInput, types.EventAgentTurnWaitingTool, types.EventAgentTurnWaitingApproval:
		status := turnStatusForEvent(event.Type)
		if result, ok := payloadAs[types.CallResult](event.Payload, "call_result"); ok && result.Status != "" {
			status = result.Status
		}
		g.track(NodeCallTurn, event.CallTurnID, event, status, time.Time{}, time.Time{}, event.Attempt)
	}
}

// track records status and timestamps on a node. Timestamps already known win
// over zero values; when the payload carries none, the event's own CreatedAt
// is used for the end of a terminal event or the start of a started one.
func (g *Graph) track(kind NodeKind, id string, event storage.SessionEvent, status types.TurnStatus, startedAt, finishedAt time.Time, attempt int) {
	node := g.ensure(kind, id)
	if node == nil {
		return
	}
	if status != "" {
		node.Status = status
	}
	if attempt > 0 {
		node.Attempt = attempt
	}
	if !startedAt.IsZero() {
		node.StartedAt = startedAt
	} else if node.StartedAt.IsZero() && strings.HasSuffix(event.Type, ".started") {
		node.StartedAt = event.CreatedAt
	}
	if !finishedAt.IsZero() {
		node.FinishedAt = finishedAt
	} else if node.FinishedAt.IsZero() && !strings.HasSuffix(event.Type, ".started") {
		node.FinishedAt = event.CreatedAt
	}
}

func turnStatusForEvent(eventType string) types.TurnStatus {
	switch eventType {
	case types.EventFlowTurnWaitingInput, types.EventTeamTurnWaitingInput, types.EventAgentTurnWaitingInput:
		return types.TurnWaitingInput
	case types.EventFlowTurnWaitingTool, types.EventTeamTurnWaitingTool, types.EventAgentTurnWaitingTool:
		return types.TurnWaitingTool
	case types.EventFlowTurnWaitingApproval, types.EventTeamTurnWaitingApproval, types.EventAgentTurnWaitingApproval:
		return types.TurnWaitingApproval
	default:
		return types.TurnRunning
	}
}

// payloadAs decodes a business payload that survived the round trip through
// JSONL as map[string]any.
func payloadAs[T any](payload map[string]any, key string) (T, bool) {
	var value T
	raw, ok := payload[key]
	if !ok {
		return value, false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return value, false
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return value, false
	}
	return value, true
}
