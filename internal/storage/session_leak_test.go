package storage

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// These tests guard against goroutine leaks in JSONLSessionWriter. Subscribe
// spawns a cleanup goroutine that must exit when its context is cancelled; a
// bug there would leak one goroutine per subscription and eventually exhaust
// the runtime. Append itself is synchronous, so a high-frequency Append burst
// must not leave any goroutine behind.

// maxGoroutineGrowth is the tolerated jitter between baseline and steady-state
// counts. The Go runtime may transiently start/stop background goroutines
// (GC, netpoll, timers), so we allow a small margin rather than asserting an
// exact match.
const maxGoroutineGrowth = 8

// settle waits for background goroutines (including the Subscribe cleanup
// goroutine) to observe context cancellation and exit, polling until stable.
func settle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(50 * time.Millisecond)
		if runtime.NumGoroutine() < maxGoroutineGrowth+2 {
			return
		}
	}
}

func TestJSONLSessionWriterSubscribeDoesNotLeakGoroutines(t *testing.T) {
	store := NewFileStore(t.TempDir())
	writer := NewJSONLSessionWriter(store)
	ctx := context.Background()

	if _, err := writer.Append(ctx, "leak-flow", LayerFlow, SessionEvent{EventHeader: types.EventHeader{Type: types.EventFlowSessionCreated}}); err != nil {
		t.Fatal(err)
	}

	// Warm up so lazy runtime goroutines are already running before baseline.
	baseline := runtime.NumGoroutine()

	const subs = 50
	cancels := make([]context.CancelFunc, 0, subs)
	for i := 0; i < subs; i++ {
		subCtx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		if _, err := writer.Subscribe(subCtx, "leak-flow", 0); err != nil {
			t.Fatal(err)
		}
	}

	// Cancel every subscription and wait for cleanup goroutines to exit.
	for _, cancel := range cancels {
		cancel()
	}
	settle(t)

	after := runtime.NumGoroutine()
	if after > baseline+maxGoroutineGrowth {
		t.Fatalf("goroutine leak detected after Subscribe/cancel: baseline=%d after=%d (growth=%d, tolerance=%d)",
			baseline, after, after-baseline, maxGoroutineGrowth)
	}
}

func TestJSONLSessionWriterAppendDoesNotLeakGoroutines(t *testing.T) {
	store := NewFileStore(t.TempDir())
	writer := NewJSONLSessionWriter(store)
	ctx := context.Background()

	baseline := runtime.NumGoroutine()

	const appends = 10000
	for i := 0; i < appends; i++ {
		if _, err := writer.Append(ctx, "leak-flow", LayerFlow, SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventAgentTurnCompleted,
				FlowSessionID: "leak-flow",
			},
			Payload: map[string]any{"i": i},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Append is synchronous: no goroutine should be created by the burst.
	// A short settle covers timer/GC goroutines that may have started lazily.
	settle(t)

	after := runtime.NumGoroutine()
	if after > baseline+maxGoroutineGrowth {
		t.Fatalf("goroutine leak detected after %d Appends: baseline=%d after=%d (growth=%d, tolerance=%d)",
			appends, baseline, after, after-baseline, maxGoroutineGrowth)
	}
}
