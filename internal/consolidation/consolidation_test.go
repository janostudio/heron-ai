package consolidation

import (
	"context"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestRecordConsolidator_ConsolidatesSharedRecords(t *testing.T) {
	consolidator := NewRecordConsolidator()
	result := consolidator.Consolidate(context.Background(), []types.SharedRecord{
		{Name: "Diagnosis", Summary: "Root cause found"},
		{Name: "Verification", Summary: "Tests passed"},
	})
	if !contains(result, "Root cause found") || !contains(result, "Tests passed") {
		t.Fatalf("expected both record summaries, got %q", result)
	}
}

func TestRecordConsolidator_Empty(t *testing.T) {
	if result := NewRecordConsolidator().Consolidate(context.Background(), nil); result != "" {
		t.Fatalf("expected empty result, got %q", result)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
