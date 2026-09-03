package agentstore

import "context"

// SpawnedCallSpec identifies one dynamically spawned child entity that joins
// the Team DAG as a synthetic call (design 21 §4.4, batch C): the child runs
// independently as a member of the spawning parent call's group and publishes
// records under the parent call's output.record name.
type SpawnedCallSpec struct {
	// AgentID is the target agent template.
	AgentID string
	// Key is the resolved entity key (created/reused via Registry.EnsureEntity).
	Key string
	// Item is the task data delivered to the child as the ## Your Item block.
	Item any
	// Depth is the spawn depth of the child turn (nested spawn limit).
	Depth int
	// ParentCallID is the call that spawned this child; the synthetic call id
	// derives from it. It is authoritative on the insertion service side.
	ParentCallID string
}

// ChildInserter is the insertion channel from the Spawn tool into the Team
// scheduler's main loop. The Team runtime creates one instance per Run and
// injects it into the execution context (the same wiring pattern as
// RecordCollector); Spawn(wait=false, deliver=downstream) consumes it to
// register each child entity as a synthetic Team call. Implementations must
// be safe for concurrent use.
type ChildInserter interface {
	InsertSpawnedCall(ctx context.Context, parentCallID string, spec SpawnedCallSpec) error
}

type childInserterKey struct{}

// WithChildInserter attaches the Team scheduler's child insertion channel to
// ctx. Only Agent turns executed as Team-scheduled calls carry it.
func WithChildInserter(ctx context.Context, inserter ChildInserter) context.Context {
	return context.WithValue(ctx, childInserterKey{}, inserter)
}

// ChildInserterFromContext returns the insertion channel attached to ctx, or
// nil when the current Agent turn is not a Team-scheduled call (for example a
// durable SpawnChild task, whose execution context is detached from the Team
// Run).
func ChildInserterFromContext(ctx context.Context) ChildInserter {
	if ctx == nil {
		return nil
	}
	inserter, _ := ctx.Value(childInserterKey{}).(ChildInserter)
	return inserter
}

// ChildCallID builds the call id of one spawned child ("<parent>/<key>").
// The Team runtime's synthetic calls, the Spawn tool's inline children, and
// session events all use it, so producers share one naming scheme.
func ChildCallID(parentCallID, key string) string {
	if parentCallID == "" {
		return "spawn/" + key
	}
	return parentCallID + "/" + key
}

type spawnDepthKey struct{}

// WithSpawnDepth carries the spawn depth of the current Agent turn so nested
// Spawn calls can enforce the recursion limit across execution paths.
func WithSpawnDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, spawnDepthKey{}, depth)
}

// SpawnDepthFromContext returns the spawn depth attached to ctx (0 when the
// turn was not itself spawned).
func SpawnDepthFromContext(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	depth, _ := ctx.Value(spawnDepthKey{}).(int)
	return depth
}
