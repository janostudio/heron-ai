package flow

import (
	"testing"
	"time"
)

// The scheduler loop used to run under context.Background(), so nothing could
// stop it except process exit — a rebuilt runtime left the previous loop alive.
func TestStopCancelsRuntimeLifecycle(t *testing.T) {
	r := NewRuntime(nil, nil, nil, nil)

	ctx := r.lifecycleContext()
	select {
	case <-ctx.Done():
		t.Fatal("lifecycle was cancelled before Stop")
	default:
	}

	r.Stop()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the lifecycle context")
	}
}

func TestStopIsSafeWithoutALifecycle(t *testing.T) {
	var r Runtime
	r.Stop() // a zero-value runtime has nothing to stop
	if r.lifecycleContext() == nil {
		t.Fatal("lifecycleContext returned nil")
	}
}

func TestWakeupSemaphoreHasABound(t *testing.T) {
	r := NewRuntime(nil, nil, nil, nil)
	sem := r.wakeupSemaphore()
	if cap(sem) != maxConcurrentWakeupFires {
		t.Fatalf("semaphore capacity %d, want %d", cap(sem), maxConcurrentWakeupFires)
	}
}
