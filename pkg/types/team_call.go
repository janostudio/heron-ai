package types

import "context"

// ToolNameTeamRun is the internal tool name of the durable task that runs one
// Team call. It is never registered for models: only a task created by the Team
// tool reaches it. Shared so the flow can recognise such a task without
// importing the agent package.
const ToolNameTeamRun = "TeamRun"

// TeamCallSpec is one Agent's request to another Team.
//
// Collaboration is modelled as an asynchronous tool call: the caller hands over
// a task and gets a handle, the callee runs on its own, and the answer returns
// as the tool result. This struct is the tool call's arguments, widened with the
// caller's coordinates so the answer can route back.
type TeamCallSpec struct {
	// FlowSessionID scopes the call: the callee is part of the same session, not
	// a detached job.
	FlowSessionID string
	// CallerTeam is the Team that made the call.
	CallerTeam string
	// TeamID is the callee.
	TeamID string
	// CallID and AgentTurnID name the calling Agent, for provenance in events.
	CallID      string
	AgentTurnID string
	// TaskKey is the business key of the task. Required when the callee binding
	// uses lifecycle per_task: it is what lets a later dispatch find and
	// continue the same instance.
	TaskKey string
	// Subscriptions are the sources (issue / PR / CI / thread ids) whose later
	// events belong to this task. Frozen on the first dispatch: a later dispatch
	// may add none of them silently, because delivery depends on them.
	Subscriptions []string
	// Authorization is the permission the caller grants this task. It is frozen
	// on the first dispatch; a later dispatch of the same task_key may only
	// repeat it.
	Authorization *AuthorizationGrant
	// DispatchedBy names the calling Team instance. The callee's ownership is
	// fixed to it: only this instance may continue, close or reopen the task.
	DispatchedBy *TeamSessionRef
	// Item is the task handed to the callee.
	Item any
	// Summary is a short description of the task, for display while it runs.
	Summary string
}

// TeamCallOutcome is what the caller receives as the tool result.
type TeamCallOutcome struct {
	// Status mirrors the callee's own route (done / blocked / failed), so the
	// caller can tell "answered" from "could not answer" without reading prose.
	Status string
	// Reply is the callee's answer, self-contained.
	Reply string
	// Records are the names of the records the callee published, for the
	// caller's provenance display.
	Records []string
	// Error is set when the call itself failed (budget, timeout, crash).
	Error string
	// TeamSessionID is the callee instance that ran this call. Empty when the
	// call failed before an instance existed.
	TeamSessionID string
	// TaskKey echoes the business key so the caller keeps the handle it passed.
	TaskKey string
	// Reopened reports that an already-closed instance of this task was reused
	// instead of a new one being created.
	Reopened bool
}

// TaskInfo is one dispatched task as its dispatcher sees it: enough to decide
// what to do next without replaying the event stream.
type TaskInfo struct {
	TaskKey       string        `json:"task_key"`
	TeamID        string        `json:"team_id"`
	TeamSessionID string        `json:"team_session_id"`
	Status        SessionStatus `json:"status"`
	DispatchCount int           `json:"dispatch_count"`
	ReopenCount   int           `json:"reopen_count"`
	TaskContextID string        `json:"task_context_id,omitempty"`
	LastReply     string        `json:"last_reply,omitempty"`
}

// TaskCatalogSpec scopes a task query to one Flow session and one caller
// instance: a Team instance sees only the tasks it dispatched.
type TaskCatalogSpec struct {
	FlowSessionID       string
	CallerTeamSessionID string
}

// TaskTree groups the tasks of one instance by task context. It is the ops
// view: one entry per dispatched task tree, so "what is this session working on
// and how deep did it go" is answerable without scanning every task.
type TaskTree struct {
	TaskContextID string     `json:"task_context_id"`
	Tasks         []TaskInfo `json:"tasks"`
}

// TaskCatalog is the read side of the task routing table. The flow runtime
// derives it from the event stream (the fact source), so a restarted process
// answers the same questions without a separate index to keep in sync.
type TaskCatalog interface {
	ListTasks(ctx context.Context, spec TaskCatalogSpec) ([]TaskInfo, error)
	ListTaskTrees(ctx context.Context, spec TaskCatalogSpec) ([]TaskTree, error)
	GetTask(ctx context.Context, spec TaskCatalogSpec, taskKey string) (TaskInfo, error)
}

// TaskCloser soft-closes a task: the instance keeps its transcript and records,
// but stops accepting work. Reopening happens by dispatching the same task_key
// again, which is why closing is a state change rather than a delete.
type TaskCloser interface {
	CloseTask(ctx context.Context, spec TaskCatalogSpec, taskKey string) (TaskInfo, error)
}

// TeamRunner executes one Team call. The flow runtime implements it: only the
// flow knows the session, the Team bindings and how to persist a Team turn.
type TeamRunner interface {
	RunTeam(ctx context.Context, spec TeamCallSpec) (TeamCallOutcome, error)
}

// TeamCatalogEntry is one Team a caller may hand work to, as the caller needs to
// see it: what it is for, what it can read, what it produces.
//
// This is the projection of the Flow's graph into something a model can choose
// from. Configuration stays the source of truth; the catalog is a read-only
// view of it, scoped to one caller.
type TeamCatalogEntry struct {
	TeamID string `json:"subagent_type"`
	// Responsibility is the Team's own statement of what it does, taken from its
	// entry call. It is what the model reasons about when choosing.
	Responsibility string `json:"responsibility,omitempty"`
	// Receives lists the inputs the target can read: "user_message" and the
	// record names it subscribes to.
	Receives []string `json:"receives,omitempty"`
	// Produces is the record the target publishes when it finishes, so a caller
	// knows what to read next.
	Produces string `json:"produces,omitempty"`
	// Lifecycle tells the caller whether handing work over creates a task
	// instance ("per_task", needs task_key) or reuses the resident session.
	Lifecycle string `json:"lifecycle,omitempty"`
}

// TeamCatalogSpec identifies whose view is being read.
type TeamCatalogSpec struct {
	FlowSessionID string
	// CallerTeam is the Team asking: the answer is its can_activate, not the
	// whole graph.
	CallerTeam string
}

// TeamCatalog lists the Teams one caller may hand work to.
type TeamCatalog interface {
	ListTeams(ctx context.Context, spec TeamCatalogSpec) ([]TeamCatalogEntry, error)
}
