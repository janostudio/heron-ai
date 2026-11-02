package flow

import "sync"

// resumeLock returns the unlock function for one session's resume path.
//
// The lock is per session, not per runtime: two conversations resuming at the
// same time are independent, and serialising them made one slow Team call hold
// every other session's mailbox wake-up and scheduled wake-up behind it. What
// the lock actually protects is one session's turn entry points from each
// other — a Tool result, a mailbox wake-up and an approval can all arrive while
// that session is deciding what to do next.
//
// sync.Map suits this: one entry per session is written once and read on every
// resume, and the read path never takes a runtime-wide mutex.
func (r *Runtime) resumeLock(sessionID string) func() {
	value, _ := r.resumeLocks.LoadOrStore(sessionID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}
