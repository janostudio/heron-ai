package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// The startup replay used to launch one goroutine per terminal task, so a
// long-lived store turned a restart into a burst of concurrent session resumes.
func TestReplayTerminalTasksBoundsConcurrency(t *testing.T) {
	var inFlight, peak, done int32
	handle := func(_ context.Context, _ types.ToolTask) {
		current := atomic.AddInt32(&inFlight, 1)
		for {
			observed := atomic.LoadInt32(&peak)
			if current <= observed || atomic.CompareAndSwapInt32(&peak, observed, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		atomic.AddInt32(&done, 1)
	}

	tasks := make([]types.ToolTask, 12)
	replayTerminalTasks(tasks, handle)

	if got := atomic.LoadInt32(&done); got != int32(len(tasks)) {
		t.Fatalf("replayed %d tasks, want %d", got, len(tasks))
	}
	if got := atomic.LoadInt32(&peak); got > terminalReplayWorkers {
		t.Fatalf("peak concurrency %d exceeds the bound %d", got, terminalReplayWorkers)
	}
}

func TestReplayTerminalTasksWithNoTasksIsANoop(t *testing.T) {
	called := false
	replayTerminalTasks(nil, func(context.Context, types.ToolTask) { called = true })
	if called {
		t.Fatal("handler ran for an empty replay")
	}
}
