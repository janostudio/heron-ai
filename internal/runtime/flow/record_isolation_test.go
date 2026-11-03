package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestSelectFlowRecordsIsolatesTaskContexts is the record-side of instance
// isolation: a record produced inside task context A is invisible to task B,
// while session-wide records (empty context) stay visible to everyone.
func TestSelectFlowRecordsIsolatesTaskContexts(t *testing.T) {
	spec := types.InputSpec{FlowRecords: []string{"Report"}}
	record := func(id, context string) types.SharedRecord {
		return types.SharedRecord{
			RecordID:  id,
			Name:      "Report",
			CreatedAt: time.Now().UTC(),
			Producer:  types.ProducerRef{TaskContextID: context},
		}
	}
	records := []types.SharedRecord{
		record("a", "fs:dev:1"),
		record("b", "fs:dev:2"),
		record("shared", ""),
	}

	first := selectFlowRecordsInContext(records, spec, time.Time{}, "fs:dev:1")
	require.Len(t, first, 2, "own task record + the session-wide one")
	require.Equal(t, "a", first[0].RecordID)
	require.Equal(t, "shared", first[1].RecordID)

	second := selectFlowRecordsInContext(records, spec, time.Time{}, "fs:dev:2")
	require.Len(t, second, 2)
	require.Equal(t, "b", second[0].RecordID)

	// The historical entry point stays session-wide: no task context means only
	// records outside any task.
	wide := selectFlowRecords(records, spec, time.Time{})
	require.Len(t, wide, 1)
	require.Equal(t, "shared", wide[0].RecordID)
}
