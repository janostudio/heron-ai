package team

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/internal/memory"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// runState is the Run-loop scheduling state (design 21 §4.4). It owns the
// remaining/completed call sets plus the explicit spawn-group registry, so
// Spawn(wait=false, deliver=downstream) can insert synthetic child calls from
// inside a parent call's execution goroutine while the main loop is blocked
// in runBatch. All access is serialized by mu: the main loop between batches,
// the insertion channel during batches, and record selection inside batch
// goroutines.
type runState struct {
	mu        sync.Mutex
	remaining map[string]types.Call
	completed map[string]bool
	// groups maps one spawned synthetic call id to the call that spawned it.
	// Membership is explicit — a static call whose name contains "/" is never
	// treated as a group member.
	groups map[string]string
	// specs carries the spawn identity (agent, key, item, depth) of every
	// synthetic call.
	specs map[string]agentstore.SpawnedCallSpec
	// locks serializes entity turns across the team runtime and the Spawn
	// tool's inline children (shared agentstore.EntityLocks).
	locks  *agentstore.EntityLocks
	closed bool
}

func newRunState(remaining map[string]types.Call, completed map[string]bool, locks *agentstore.EntityLocks) *runState {
	if locks == nil {
		locks = agentstore.NewEntityLocks()
	}
	return &runState{
		remaining: remaining,
		completed: completed,
		groups:    make(map[string]string),
		specs:     make(map[string]agentstore.SpawnedCallSpec),
		locks:     locks,
	}
}

// InsertSpawnedCall implements agentstore.ChildInserter: it registers one
// spawned child entity as a synthetic agent call in the parent call's group.
// It runs inside the Spawn tool while the parent call is executing, so the
// parent is still in remaining — insertions strictly precede the parent's
// completion accounting and no termination race with the main loop exists.
func (s *runState) InsertSpawnedCall(ctx context.Context, parentCallID string, spec agentstore.SpawnedCallSpec) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(parentCallID) == "" {
		return errors.New("spawn: parent call id is required")
	}
	if strings.TrimSpace(spec.AgentID) == "" {
		return errors.New("spawn: agent id is required")
	}
	if strings.TrimSpace(spec.Key) == "" {
		return errors.New("spawn: entity key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("spawn: team turn is no longer running; child %q cannot join", agentstore.ChildCallID(parentCallID, spec.Key))
	}
	parent, ok := s.remaining[parentCallID]
	if !ok {
		return fmt.Errorf("spawn: parent call %q is not a scheduled call of this team turn; only calls executed by the Team scheduler can deliver downstream", parentCallID)
	}
	if parent.Output.Record == "" {
		return fmt.Errorf("spawn: deliver=downstream requires parent call %q to configure output.record", parentCallID)
	}
	callID := agentstore.ChildCallID(parentCallID, spec.Key)
	if _, exists := s.remaining[callID]; exists {
		return fmt.Errorf("spawn: child call %q is already pending", callID)
	}
	spec.ParentCallID = parentCallID
	// The synthetic call inherits the parent's output.record name so the
	// child's records aggregate with the parent's through the existing
	// same-name record mechanism; RecordID keeps the "<parent>/<key>" call id
	// prefix so multiple children stay distinct.
	s.remaining[callID] = types.Call{
		ID:      callID,
		Type:    types.CallAgent,
		AgentID: spec.AgentID,
		Output:  parent.Output,
	}
	s.groups[callID] = parentCallID
	s.specs[callID] = spec
	return nil
}

func (s *runState) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func (s *runState) hasRemaining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.remaining) > 0
}

// call returns one not-yet-completed call by id.
func (s *runState) call(id string) (types.Call, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.remaining[id]
	return call, ok
}

// complete marks one call completed and removes it from remaining.
func (s *runState) complete(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.remaining, name)
	s.completed[name] = true
}

// readyCalls returns every remaining call whose all-of dependencies are
// satisfied. A dependency is satisfied when it — and, recursively, every
// member of its spawn group — has completed, so a downstream call waits for
// the whole group (design 20 §2.1: depends_on [parent] extends to the group).
func (s *runState) readyCalls() map[string]types.Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	ready := make(map[string]types.Call)
	for name, configured := range s.remaining {
		ok := true
		for _, dependency := range configured.DependsOn {
			if !s.groupCompletedLocked(dependency) {
				ok = false
				break
			}
		}
		if ok {
			ready[name] = configured
		}
	}
	return ready
}

// groupCompletedLocked reports whether callID and every member of its spawn
// group (recursively — a group member may itself have spawned children) have
// completed. With an empty group registry this is exactly the legacy
// completed[callID] lookup, so no-spawn teams keep their current behavior.
func (s *runState) groupCompletedLocked(callID string) bool {
	if !s.completed[callID] {
		return false
	}
	for member, parent := range s.groups {
		if parent == callID && !s.groupCompletedLocked(member) {
			return false
		}
	}
	return true
}

// specOf returns the spawn identity of a synthetic call (nil for static
// calls).
func (s *runState) specOf(callID string) (*agentstore.SpawnedCallSpec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, ok := s.specs[callID]
	if !ok {
		return nil, false
	}
	return &spec, true
}

// producersFrom expands one binding source to itself plus every recursive
// member of its spawn group, in breadth-first sorted order, so from-bindings
// aggregate the records of the parent call and all its spawned children.
func (s *runState) producersFrom(callID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.producersFromLocked(callID)
}

func (s *runState) producersFromLocked(callID string) []string {
	result := []string{callID}
	visited := map[string]bool{callID: true}
	queue := []string{callID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		members := make([]string, 0)
		for member, parent := range s.groups {
			if parent == current && !visited[member] {
				members = append(members, member)
			}
		}
		sort.Strings(members)
		for _, member := range members {
			visited[member] = true
			result = append(result, member)
			queue = append(queue, member)
		}
	}
	return result
}

// tryLockEntity serializes entity turns across the team runtime and the Spawn
// tool's inline children through the shared agentstore.EntityLocks.
func (s *runState) tryLockEntity(agentID, key string) (func(), bool) {
	return s.locks.TryLock(agentID, key)
}

// saveEntityMemory applies the same deterministic update the Spawn tool uses
// for inline children (spawn.go saveEntityMemory): the item becomes the goal,
// the first reply is confirmed, later replies become next steps, and
// workspace refs are tracked.
func saveEntityMemory(
	ctx context.Context,
	memories *memory.Store,
	spec agentstore.SpawnedCallSpec,
	previousMemoryText string,
	result types.CallResult,
) error {
	snapshot, err := memories.LoadEntity(ctx, spec.AgentID, spec.Key)
	if err != nil {
		return err
	}
	itemJSON, err := json.Marshal(spec.Item)
	if err != nil {
		return fmt.Errorf("encode spawn item: %w", err)
	}
	if snapshot.Goal == "" {
		snapshot.Goal = string(itemJSON)
	}
	if reply := strings.TrimSpace(result.Reply); reply != "" {
		if strings.TrimSpace(previousMemoryText) == "" {
			snapshot.Confirmed = append(snapshot.Confirmed, reply)
		} else {
			snapshot.NextSteps = append(snapshot.NextSteps, reply)
		}
	}
	for _, operation := range result.WorkspaceOps {
		if operation.Path == "" {
			continue
		}
		snapshot.Workspace = append(snapshot.Workspace, types.MemoryWorkspaceRef{
			Path:     operation.Path,
			Revision: operation.Revision,
		})
	}
	return memories.SaveEntity(ctx, spec.AgentID, spec.Key, snapshot)
}
