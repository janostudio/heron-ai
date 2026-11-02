package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A runtime serves every conversation in the process. Resume paths of two
// different sessions must therefore run independently — one conversation's
// slow Team call used to delay every other session's mailbox and scheduled
// wake-ups.
func TestResumeLockIsPerSession(t *testing.T) {
	runtime := &Runtime{}

	releaseA := make(chan struct{})
	acquiredA := make(chan struct{})
	go func() {
		unlock := runtime.resumeLock("fs_a")
		close(acquiredA)
		<-releaseA
		unlock()
	}()
	<-acquiredA

	// Another session must not wait for A.
	doneB := make(chan struct{})
	go func() {
		unlock := runtime.resumeLock("fs_b")
		unlock()
		close(doneB)
	}()
	select {
	case <-doneB:
	case <-time.After(2 * time.Second):
		t.Fatal("a different session waited for session A's resume lock")
	}

	// The same session must serialise: two resumed turns on one conversation
	// would fight over the same session state.
	startedA := make(chan struct{})
	acquiredA2 := make(chan struct{})
	go func() {
		close(startedA)
		unlock := runtime.resumeLock("fs_a")
		unlock()
		close(acquiredA2)
	}()
	<-startedA
	select {
	case <-acquiredA2:
		t.Fatal("the same session took two resume locks at once")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseA)
	select {
	case <-acquiredA2:
	case <-time.After(2 * time.Second):
		t.Fatal("the second lock on the same session was never released")
	}
}

// The unlock function must be safe to call once per acquisition.
func TestResumeLockReleasesOnUnlock(t *testing.T) {
	runtime := &Runtime{}
	unlock := runtime.resumeLock("fs_a")
	unlock()

	done := make(chan struct{})
	go func() {
		unlockAgain := runtime.resumeLock("fs_a")
		unlockAgain()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the lock was not released")
	}
	require.True(t, true)
}
