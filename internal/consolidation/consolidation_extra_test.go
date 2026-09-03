package consolidation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestRecordConsolidator_Consolidate(t *testing.T) {
	tests := []struct {
		name    string
		records []types.SharedRecord
		want    []string
		notWant []string
	}{
		{
			name:    "zero records",
			records: nil,
			want:    []string{},
		},
		{
			name: "single record returns summary only",
			records: []types.SharedRecord{
				{Name: "Diagnosis", Summary: "Root cause found"},
			},
			want: []string{"Root cause found"},
			// single-record path must not add headers
			notWant: []string{"## Shared Records", "### Diagnosis"},
		},
		{
			name: "multiple records sorted by name",
			records: []types.SharedRecord{
				{Name: "Zebra", Summary: "z summary"},
				{Name: "Apple", Summary: "a summary"},
			},
			want: []string{"## Shared Records", "### Apple", "a summary", "### Zebra", "z summary"},
		},
		{
			name: "empty name gets generated",
			records: []types.SharedRecord{
				{Summary: "nameless summary"},
			},
			// single-record path returns summary only; no generated name
			want:    []string{"nameless summary"},
			notWant: []string{"## Shared Records", "record-1"},
		},
		{
			name: "mixed empty and named",
			records: []types.SharedRecord{
				{Name: "B", Summary: "b"},
				{Summary: "no name"},
			},
			want: []string{"## Shared Records", "record-", "### B"},
		},
		{
			name: "multiple records with empty name generated",
			records: []types.SharedRecord{
				{Summary: "nameless"},
				{Name: "A", Summary: "a"},
			},
			want: []string{"## Shared Records", "record-1", "nameless", "### A"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NewRecordConsolidator().Consolidate(context.Background(), tt.records)
			for _, w := range tt.want {
				require.Contains(t, result, w)
			}
			for _, w := range tt.notWant {
				require.NotContains(t, result, w)
			}
		})
	}
}

func TestRecordConsolidator_SortOrderDeterministic(t *testing.T) {
	c := NewRecordConsolidator()
	records := []types.SharedRecord{
		{Name: "c", Summary: "three"},
		{Name: "a", Summary: "one"},
		{Name: "b", Summary: "two"},
	}
	result := c.Consolidate(context.Background(), records)
	idxA := indexOf(result, "### a")
	idxB := indexOf(result, "### b")
	idxC := indexOf(result, "### c")
	require.True(t, idxA < idxB && idxB < idxC, "records must be sorted by name, got %q", result)
}

func TestBuildCuratorSources(t *testing.T) {
	tests := []struct {
		name    string
		records []types.SharedRecord
		want    []string
	}{
		{
			name:    "empty",
			records: nil,
			want:    []string{},
		},
		{
			name: "both empty skipped",
			records: []types.SharedRecord{
				{Name: "  ", Summary: ""},
			},
			want: []string{},
		},
		{
			name: "name only",
			records: []types.SharedRecord{
				{Name: "Diagnosis", Summary: ""},
			},
			want: []string{"Diagnosis"},
		},
		{
			name: "summary only",
			records: []types.SharedRecord{
				{Name: "", Summary: "Root cause found"},
			},
			want: []string{"Root cause found"},
		},
		{
			name: "both present formatted",
			records: []types.SharedRecord{
				{Name: "Diagnosis", Summary: "Root cause found"},
			},
			want: []string{"[Diagnosis] Root cause found"},
		},
		{
			name: "trims whitespace",
			records: []types.SharedRecord{
				{Name: "  Diagnosis  ", Summary: "  Root cause  "},
			},
			want: []string{"[Diagnosis] Root cause"},
		},
		{
			name: "mixed skip and keep",
			records: []types.SharedRecord{
				{Name: "", Summary: ""},
				{Name: "A", Summary: "keep"},
			},
			want: []string{"[A] keep"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCuratorSources(tt.records)
			require.Equal(t, tt.want, got)
		})
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
