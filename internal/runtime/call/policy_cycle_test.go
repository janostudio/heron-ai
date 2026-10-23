package call

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestRecordTemplatesRejectSelfReference covers the escaping placeholder: a
// record's Summary is model-generated, so it can contain the very template it
// is substituted into. The old loop replaced it with itself forever, pinning the
// Call's goroutine at 100% CPU with no context able to interrupt it.
func TestRecordTemplatesRejectSelfReference(t *testing.T) {
	records := []types.SharedRecord{{
		RecordID: "r1",
		Name:     "Self",
		Status:   types.RecordActive,
		Revision: 1,
		Summary:  "${records.Self}",
	}}

	_, err := expandRecordTemplates("${records.Self}", records)
	require.Error(t, err)
	require.Contains(t, err.Error(), "resolves to itself")
}

// TestRecordTemplatesRejectCycle covers the two-record version: neither
// placeholder resolves to itself, but expanding one reintroduces the other.
func TestRecordTemplatesRejectCycle(t *testing.T) {
	records := []types.SharedRecord{
		{RecordID: "r1", Name: "A", Status: types.RecordActive, Revision: 1, Summary: "${records.B}"},
		{RecordID: "r2", Name: "B", Status: types.RecordActive, Revision: 1, Summary: "${records.A}"},
	}

	_, err := expandRecordTemplates("${records.A}", records)
	require.Error(t, err, "a cyclic expansion must terminate with an error")
}

// TestRecordTemplatesStillExpandPlainValues keeps the guard from rejecting
// ordinary use: a record that merely mentions text expands in one hop.
func TestRecordTemplatesStillExpandPlainValues(t *testing.T) {
	records := []types.SharedRecord{
		{RecordID: "r1", Name: "Report", Status: types.RecordActive, Revision: 1, Summary: "all green"},
	}

	got, err := expandRecordTemplates("result: ${records.Report}", records)
	require.NoError(t, err)
	require.Equal(t, "result: all green", got)
}
