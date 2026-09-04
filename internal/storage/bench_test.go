package storage

import (
	"context"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// BenchmarkSessionAppend measures the append hot path of JSONLSessionWriter.
// This is performance-sensitive because every turn of every team/agent writes
// at least one event through Append, so a slow Append directly throttles the
// whole engine's throughput. It is dominated by json.Marshal + file Append +
// mutex contention (the RunParallel sub-benchmark isolates the contention).
func BenchmarkSessionAppend(b *testing.B) {
	b.Run("serial", func(b *testing.B) {
		store := NewFileStore(b.TempDir())
		writer := NewJSONLSessionWriter(store)
		ctx := context.Background()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := writer.Append(ctx, "bench-flow", LayerFlow, SessionEvent{
				EventHeader: types.EventHeader{
					Type:          types.EventAgentTurnCompleted,
					FlowSessionID: "bench-flow",
				},
				Payload: map[string]any{"answer": "hello", "tokens": 1234},
			}); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("parallel", func(b *testing.B) {
		store := NewFileStore(b.TempDir())
		writer := NewJSONLSessionWriter(store)
		ctx := context.Background()
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := writer.Append(ctx, "bench-flow", LayerFlow, SessionEvent{
					EventHeader: types.EventHeader{
						Type:          types.EventToolCallCompleted,
						FlowSessionID: "bench-flow",
					},
					Payload: map[string]any{"result": "ok"},
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	})
}

// BenchmarkSessionReplay measures JSONL replay cost against session size.
// Replay is on the session-resume hot path: after a crash or reconnect the
// runtime folds all events back into state, and cost grows linearly with the
// number of events (100 vs 1000). It is dominated by line splitting +
// json.Unmarshal + sort.
func BenchmarkSessionReplay(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(eventCountName(n), func(b *testing.B) {
			store := NewFileStore(b.TempDir())
			writer := NewJSONLSessionWriter(store)
			ctx := context.Background()
			for i := 0; i < n; i++ {
				if _, err := writer.Append(ctx, "bench-flow", LayerFlow, SessionEvent{
					EventHeader: types.EventHeader{
						Type:          types.EventAgentTurnCompleted,
						FlowSessionID: "bench-flow",
					},
					Payload: map[string]any{"i": i},
				}); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := writer.Replay(ctx, "bench-flow"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkSessionSubscribe measures live event publish throughput to a
// subscribed channel. Subscribe is the SSE hot path: every streaming client
// fans an event out through this publish loop, and slow delivery can drop the
// subscriber (publishLocked closes slow consumers). It is dominated by the
// non-blocking channel send under the writer mutex.
func BenchmarkSessionSubscribe(b *testing.B) {
	store := NewFileStore(b.TempDir())
	writer := NewJSONLSessionWriter(store)
	ctx := context.Background()
	if _, err := writer.Append(ctx, "bench-flow", LayerFlow, SessionEvent{EventHeader: types.EventHeader{Type: types.EventFlowSessionCreated}}); err != nil {
		b.Fatal(err)
	}

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := writer.Subscribe(subCtx, "bench-flow", 0)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Consume previous event while publishing the next so the buffered
		// channel never blocks and we measure the publish path, not the reader.
		if _, err := writer.Append(ctx, "bench-flow", LayerFlow, SessionEvent{EventHeader: types.EventHeader{Type: types.EventAgentTurnCompleted}}); err != nil {
			b.Fatal(err)
		}
		select {
		case <-events:
		default:
		}
	}
}

func eventCountName(n int) string {
	switch n {
	case 100:
		return "100_events"
	case 1000:
		return "1000_events"
	default:
		return "events"
	}
}
