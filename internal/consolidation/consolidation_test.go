package consolidation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/internal/knowledge"
	"github.com/heron-ai/heron-engine/internal/memory"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	text    string
	err     error
	chats   int
	lastMsg string
}

func (m *mockProvider) Chat(_ context.Context, messages []types.Message, _ []types.JSONSchema, _ types.ModelConfig) (*types.ChatResponse, error) {
	m.chats++
	for _, msg := range messages {
		if msg.Role == "user" {
			m.lastMsg = msg.Content
		}
	}
	if m.err != nil {
		return nil, m.err
	}
	return &types.ChatResponse{Text: m.text}, nil
}

func (m *mockProvider) ChatStream(_ context.Context, _ []types.Message, _ []types.JSONSchema, _ types.ModelConfig) (<-chan types.ChatChunk, error) {
	ch := make(chan types.ChatChunk)
	close(ch)
	return ch, nil
}

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

func TestWorkerCuratorWritesProposed(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	memories := memory.NewStore(files, memory.Limits{})
	worker := NewWorker(jobs, memories)

	markdown := "---\nkind: rule\n---\n# test"
	provider := &mockProvider{text: markdown}
	worker.SetCurator(knowledge.NewCurator(provider, ""), files)

	require.NoError(t, worker.Enqueue(context.Background(), types.ContextConsolidation{
		ID:     "dream-curator",
		Status: "queued",
		Records: []types.SharedRecord{
			{Name: "payment-idempotency", Summary: "use idempotency keys"},
		},
	}))
	require.NoError(t, worker.ProcessOnce(context.Background()))

	data, err := files.Read(filepath.Join(".agents", "knowledge", "proposed", "dream-curator.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), markdown)

	require.Equal(t, 1, provider.chats)
	require.Contains(t, provider.lastMsg, "payment-idempotency")
}

func TestWorkerCuratorFailureDegradesGracefully(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	memories := memory.NewStore(files, memory.Limits{})
	worker := NewWorker(jobs, memories)

	provider := &mockProvider{err: errors.New("curator boom")}
	worker.SetCurator(knowledge.NewCurator(provider, ""), files)

	require.NoError(t, memories.SaveTeam(context.Background(), types.MemorySnapshot{
		SessionID: "fs-1", TeamID: "team-1",
	}))
	require.NoError(t, worker.Enqueue(context.Background(), types.ContextConsolidation{
		ID:            "dream-fail",
		FlowSessionID: "fs-1",
		TeamID:        "team-1",
		Status:        "queued",
		Records: []types.SharedRecord{
			{Name: "Diagnosis", Summary: "Root cause found"},
		},
	}))
	require.NoError(t, worker.ProcessOnce(context.Background()))

	job, err := jobs.Load(context.Background(), "dream-fail")
	require.NoError(t, err)
	require.Equal(t, "completed", job.Status)

	_, err = files.Read(filepath.Join(".agents", "knowledge", "proposed", "dream-fail.md"))
	require.Error(t, err)

	snapshot, err := memories.LoadTeam(context.Background(), "fs-1", "team-1")
	require.NoError(t, err)
	require.Contains(t, snapshot.Confirmed, job.ResultSummary)
}

func TestWorkerNoCuratorSkipsPath(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	memories := memory.NewStore(files, memory.Limits{})
	worker := NewWorker(jobs, memories)

	require.NoError(t, worker.Enqueue(context.Background(), types.ContextConsolidation{
		ID:     "dream-nocurator",
		Status: "queued",
		Records: []types.SharedRecord{
			{Name: "Diagnosis", Summary: "Root cause found"},
		},
	}))
	require.NoError(t, worker.ProcessOnce(context.Background()))

	_, err := files.Read(filepath.Join(".agents", "knowledge", "proposed", "dream-nocurator.md"))
	require.Error(t, err)
}

func TestWorkerCuratorEmptyRecordsSkipped(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	memories := memory.NewStore(files, memory.Limits{})
	worker := NewWorker(jobs, memories)

	provider := &mockProvider{text: "---\nkind: rule\n---\n# test"}
	worker.SetCurator(knowledge.NewCurator(provider, ""), files)

	require.NoError(t, worker.Enqueue(context.Background(), types.ContextConsolidation{
		ID:     "dream-empty",
		Status: "queued",
	}))
	require.NoError(t, worker.ProcessOnce(context.Background()))

	require.Equal(t, 0, provider.chats)
	_, err := files.Read(filepath.Join(".agents", "knowledge", "proposed", "dream-empty.md"))
	require.Error(t, err)
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
