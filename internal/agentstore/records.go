package agentstore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// RecordCollector receives records produced by spawned child entities during
// one parent Agent call. The call executor creates the collector with the
// parent call's output.record name and drains it into CallResult.Records, so
// downstream consumers aggregate the children through the existing
// same-name-record mechanism.
type RecordCollector struct {
	name     string
	producer types.ProducerRef
	seq      atomic.Int64
	mu       sync.Mutex
	records  []types.SharedRecord
}

// NewRecordCollector creates a collector publishing under recordName with the
// given producer identity. An empty recordName disables downstream delivery.
func NewRecordCollector(recordName string, producer types.ProducerRef) *RecordCollector {
	return &RecordCollector{name: recordName, producer: producer}
}

// Enabled reports whether the parent call declared output.record. Tools use
// it to reject deliver=downstream when there is no record to publish.
func (c *RecordCollector) Enabled() bool {
	return c != nil && c.name != ""
}

// Add appends one record built from kind/summary/data and returns it.
func (c *RecordCollector) Add(kind, summary string, data map[string]any) types.SharedRecord {
	record := types.SharedRecord{
		RecordID:  fmt.Sprintf("%s-spawn-%d", c.producer.CallID, c.seq.Add(1)),
		Kind:      kind,
		Name:      c.name,
		Scope:     types.RecordScopeTeam,
		Producer:  c.producer,
		Summary:   summary,
		Data:      data,
		Status:    types.RecordActive,
		Revision:  1,
		CreatedAt: time.Now().UTC(),
	}
	c.mu.Lock()
	c.records = append(c.records, record)
	c.mu.Unlock()
	return record
}

// Records returns the collected records in completion order.
func (c *RecordCollector) Records() []types.SharedRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]types.SharedRecord(nil), c.records...)
}

type recordCollectorKey struct{}

// WithRecordCollector attaches a collector to ctx so the Spawn tool running
// inside one Agent call can publish records for the call executor to drain.
func WithRecordCollector(ctx context.Context, collector *RecordCollector) context.Context {
	return context.WithValue(ctx, recordCollectorKey{}, collector)
}

// RecordCollectorFromContext returns the collector attached to ctx, or nil.
func RecordCollectorFromContext(ctx context.Context) *RecordCollector {
	if ctx == nil {
		return nil
	}
	collector, _ := ctx.Value(recordCollectorKey{}).(*RecordCollector)
	return collector
}
