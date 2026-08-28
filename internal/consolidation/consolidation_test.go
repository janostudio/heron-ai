package consolidation

import (
	"context"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/internal/memory"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
	"github.com/stretchr/testify/require"
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

func TestWorkerProcessesDurableDreamJobAndUpdatesMemory(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	memories := memory.NewStore(files, memory.Limits{})
	worker := NewWorker(jobs, memories)

	require.NoError(t, memories.SaveTeam(context.Background(), types.MemorySnapshot{
		SessionID: "fs-1", TeamID: "team-1",
	}))
	require.NoError(t, worker.Enqueue(context.Background(), types.ContextConsolidation{
		ID:            "dream-1",
		FlowSessionID: "fs-1",
		TeamID:        "team-1",
		Status:        "queued",
		Records: []types.SharedRecord{
			{Name: "Diagnosis", Summary: "Root cause found"},
			{Name: "Verification", Summary: "Tests passed"},
		},
	}))
	require.NoError(t, worker.ProcessOnce(context.Background()))

	job, err := jobs.Load(context.Background(), "dream-1")
	require.NoError(t, err)
	require.Equal(t, "completed", job.Status)
	require.Contains(t, job.ResultSummary, "Root cause found")

	snapshot, err := memories.LoadTeam(context.Background(), "fs-1", "team-1")
	require.NoError(t, err)
	require.Contains(t, snapshot.Confirmed, job.ResultSummary)
}

func TestWorkerRecoversRunningJobsAfterRestart(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	now := time.Now().UTC()
	require.NoError(t, jobs.Save(context.Background(), types.ContextConsolidation{
		ID: "dream-running", Status: "running", CreatedAt: now,
	}))
	worker := NewWorker(jobs, nil)
	require.NoError(t, worker.Recover(context.Background()))
	job, err := jobs.Load(context.Background(), "dream-running")
	require.NoError(t, err)
	require.Equal(t, "queued", job.Status)
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
