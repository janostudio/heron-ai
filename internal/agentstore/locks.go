package agentstore

import "sync"

// EntityLocks serializes concurrent turns of one Agent. Since design doc 26
// the lock is keyed by agent (not by entity/key): multiple concurrent
// instances of the same agent share one state workbench, so only one instance
// may run a turn at a time. Locking never blocks: a busy agent fails fast.
type EntityLocks struct {
	locks sync.Map // agentID -> *sync.Mutex
}

// NewEntityLocks creates an empty lock set.
func NewEntityLocks() *EntityLocks {
	return &EntityLocks{}
}

// TryLock acquires the agent's turn lock. It returns the unlock function and
// true when acquired; (nil, false) when another turn of the agent is in
// flight. A nil lock set never blocks (locking is disabled).
func (l *EntityLocks) TryLock(agentID string) (func(), bool) {
	if l == nil {
		return func() {}, true
	}
	stored, _ := l.locks.LoadOrStore(agentID, &sync.Mutex{})
	agentMu := stored.(*sync.Mutex)
	if !agentMu.TryLock() {
		return nil, false
	}
	return agentMu.Unlock, true
}
