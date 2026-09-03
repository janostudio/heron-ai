package agentstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestRecordCollectorDisabledWithoutName(t *testing.T) {
	collector := NewRecordCollector("", types.ProducerRef{CallID: "call-1"})
	assert.False(t, collector.Enabled())
}

func TestRecordCollectorEnabled(t *testing.T) {
	collector := NewRecordCollector("FixReport", types.ProducerRef{CallID: "call-1"})
	assert.True(t, collector.Enabled())
}

func TestRecordCollectorAddBuildsRecords(t *testing.T) {
	producer := types.ProducerRef{
		FlowSessionID: "fs-1",
		FlowTurnID:    "ft-1",
		TeamID:        "team-1",
		TeamTurnID:    "tt-1",
		CallID:        "call-1",
		CallTurnID:    "ct-1",
	}
	collector := NewRecordCollector("FixReport", producer)

	first := collector.Add("spawn_result", "fixed a.go", map[string]any{"key": "e-1"})
	second := collector.Add("spawn_result", "fixed b.go", map[string]any{"key": "e-2"})

	records := collector.Records()
	require.Len(t, records, 2)
	for _, record := range records {
		assert.Equal(t, "FixReport", record.Name)
		assert.Equal(t, "spawn_result", record.Kind)
		assert.Equal(t, types.RecordScopeTeam, record.Scope)
		assert.Equal(t, types.RecordActive, record.Status)
		assert.Equal(t, producer, record.Producer)
	}
	assert.Equal(t, "fixed a.go", first.Summary)
	assert.Equal(t, "e-1", first.Data["key"])
	assert.Equal(t, "e-2", second.Data["key"])
	assert.NotEqual(t, first.RecordID, second.RecordID)
}

func TestRecordCollectorContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	assert.Nil(t, RecordCollectorFromContext(ctx))

	collector := NewRecordCollector("Out", types.ProducerRef{})
	ctx = WithRecordCollector(ctx, collector)
	assert.Same(t, collector, RecordCollectorFromContext(ctx))
}
