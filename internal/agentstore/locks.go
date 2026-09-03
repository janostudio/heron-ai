package agentstore

import "sync"

// EntityLocks serializes the turns of dynamic Agent entities across every
// execution path: the Spawn tool's inline children, durable SpawnChild tasks,
// and the Team scheduler's synthetic calls. One entity is one agent — never
// two concurrent turns. Locking never blocks: a busy entity fails fast.
type EntityLocks struct {
	locks sync.Map // agentID\x00key -> *sync.Mutex
}

// NewEntityLocks creates an empty lock set.
func NewEntityLocks() *EntityLocks {
	return &EntityLocks{}
}

// TryLock acquires the entity's turn lock. It returns the unlock function and
// true when acquired; (nil, false) when another turn of the entity is in
// flight. A nil lock set never blocks (locking is disabled).
func (l *EntityLocks) TryLock(agentID, key string) (func(), bool) {
	if l == nil {
		return func() {}, true
	}
	stored, _ := l.locks.LoadOrStore(agentID+"\x00"+key, &sync.Mutex{})
	entityMu := stored.(*sync.Mutex)
	if !entityMu.TryLock() {
		return nil, false
	}
	return entityMu.Unlock, true
}
