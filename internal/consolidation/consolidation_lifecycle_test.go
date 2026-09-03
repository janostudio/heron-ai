package consolidation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/memory"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ---------------------------------------------------------------------------
// Start / Stop / loop lifecycle
// ---------------------------------------------------------------------------

func TestWorkerStartStopGracefulShutdown(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	memories := memory.NewStore(files, memory.Limits{})
	worker := NewWorker(jobs, memories)

	require.NoError(t, worker.Start(context.Background()))

	// Stop must block until loop exits and done is closed.
	worker.Stop()

	// After Stop, cancel/done are nil so a second Stop is a no-op.
	worker.Stop()

	// Start again should succeed (state was reset).
	require.NoError(t, worker.Start(context.Background()))
	worker.Stop()
}

func TestWorkerStartIdempotent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	memories := memory.NewStore(files, memory.Limits{})
	worker := NewWorker(jobs, memories)

	require.NoError(t, worker.Start(context.Background()))
	// Second Start while running returns nil and does not restart.
	require.NoError(t, worker.Start(context.Background()))
	worker.Stop()
}

func TestWorkerStartRecoverFailureCleansUp(t *testing.T) {
	// A JobStore whose List always fails forces Recover to error inside Start.
	failing := &failingJobStore{listErr: errors.New("list boom")}
	worker := NewWorker(failing, nil)

	err := worker.Start(context.Background())
	require.Error(t, err)

	// cancel must be cleared and done closed; a subsequent Start must be allowed.
	worker.mu.Lock()
	require.Nil(t, worker.cancel)
	done := worker.done
	worker.mu.Unlock()
	select {
	case <-done:
	default:
		t.Fatal("done channel was not closed after Recover failure")
	}

	// Recover to nil so the next Start succeeds and we can Stop cleanly.
	failing.listErr = nil
	require.NoError(t, worker.Start(context.Background()))
	worker.Stop()
}

func TestWorkerLoopExitsOnContextCancel(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	memories := memory.NewStore(files, memory.Limits{})
	worker := NewWorker(jobs, memories)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, worker.Start(ctx))

	worker.mu.Lock()
	done := worker.done
	worker.mu.Unlock()

	cancel()

	// done must close without a long sleep.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit after context cancellation")
	}
}

// ---------------------------------------------------------------------------
// ProcessOnce
// ---------------------------------------------------------------------------

func TestProcessOnceNoQueuedJobs(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	worker := NewWorker(jobs, nil)
	require.NoError(t, worker.ProcessOnce(context.Background()))
}

func TestProcessOnceQueuedJobCompleted(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	memories := memory.NewStore(files, memory.Limits{})
	worker := NewWorker(jobs, memories)

	require.NoError(t, worker.Enqueue(context.Background(), types.ContextConsolidation{
		ID:     "dream-p1",
		Status: "queued",
		Records: []types.SharedRecord{
			{Name: "A", Summary: "summary-a"},
		},
	}))
	require.NoError(t, worker.ProcessOnce(context.Background()))

	job, err := jobs.Load(context.Background(), "dream-p1")
	require.NoError(t, err)
	require.Equal(t, "completed", job.Status)
}

func TestProcessOnceInternalError(t *testing.T) {
	failing := &failingJobStore{listErr: errors.New("list boom")}
	worker := NewWorker(failing, nil)
	require.Error(t, worker.ProcessOnce(context.Background()))
}

func TestProcessOnceProcessSaveError(t *testing.T) {
	// List returns a queued job, but Save fails so process() returns an error.
	failing := &failingJobStore{
		saveErr: errors.New("save boom"),
		listJobs: []types.ContextConsolidation{
			{ID: "dream-saveerr", Status: "queued"},
		},
	}
	worker := NewWorker(failing, nil)
	require.Error(t, worker.ProcessOnce(context.Background()))
}

// ---------------------------------------------------------------------------
// Recover cleanup of terminal jobs
// ---------------------------------------------------------------------------

func TestRecoverDeletesFailedJob(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	worker := NewWorker(jobs, nil)

	require.NoError(t, jobs.Save(context.Background(), types.ContextConsolidation{
		ID: "dream-failed", Status: "failed",
	}))
	require.NoError(t, worker.Recover(context.Background()))

	_, err := jobs.Load(context.Background(), "dream-failed")
	require.ErrorIs(t, err, ErrConsolidationJobNotFound)
}

func TestRecoverDeletesCompletedJob(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	worker := NewWorker(jobs, nil)

	require.NoError(t, jobs.Save(context.Background(), types.ContextConsolidation{
		ID: "dream-completed", Status: "completed",
	}))
	require.NoError(t, worker.Recover(context.Background()))

	_, err := jobs.Load(context.Background(), "dream-completed")
	require.ErrorIs(t, err, ErrConsolidationJobNotFound)
}

func TestRecoverKeepsQueuedJob(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	worker := NewWorker(jobs, nil)

	require.NoError(t, jobs.Save(context.Background(), types.ContextConsolidation{
		ID: "dream-queued", Status: "queued",
	}))
	require.NoError(t, worker.Recover(context.Background()))

	job, err := jobs.Load(context.Background(), "dream-queued")
	require.NoError(t, err)
	require.Equal(t, "queued", job.Status)
}

// ---------------------------------------------------------------------------
// FileJobStore.Delete
// ---------------------------------------------------------------------------

func TestFileJobStoreDelete(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)

	require.NoError(t, jobs.Save(context.Background(), types.ContextConsolidation{ID: "dream-del"}))
	require.NoError(t, jobs.Delete(context.Background(), "dream-del"))
	_, err := jobs.Load(context.Background(), "dream-del")
	require.ErrorIs(t, err, ErrConsolidationJobNotFound)

	// Deleting a non-existent id is a no-op (storage.Delete ignores not-found).
	require.NoError(t, jobs.Delete(context.Background(), "dream-missing"))
}

func TestFileJobStoreDeleteNilConfig(t *testing.T) {
	jobs := NewFileJobStore(nil)
	require.Error(t, jobs.Delete(context.Background(), "dream-x"))
}

func TestFileJobStoreDeleteEmptyID(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	require.Error(t, jobs.Delete(context.Background(), "  "))
}

// ---------------------------------------------------------------------------
// FileJobStore error branches
// ---------------------------------------------------------------------------

func TestFileJobStoreSaveNilConfig(t *testing.T) {
	jobs := NewFileJobStore(nil)
	require.Error(t, jobs.Save(context.Background(), types.ContextConsolidation{ID: "x"}))
}

func TestFileJobStoreSaveEmptyID(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	require.Error(t, jobs.Save(context.Background(), types.ContextConsolidation{}))
}

func TestFileJobStoreLoadNilConfig(t *testing.T) {
	jobs := NewFileJobStore(nil)
	_, err := jobs.Load(context.Background(), "x")
	require.Error(t, err)
}

func TestFileJobStoreLoadEmptyID(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	_, err := jobs.Load(context.Background(), "  ")
	require.Error(t, err)
}

func TestFileJobStoreLoadNotFound(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	_, err := jobs.Load(context.Background(), "dream-nope")
	require.ErrorIs(t, err, ErrConsolidationJobNotFound)
}

func TestFileJobStoreLoadUnmarshalError(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	// Write garbage into the job file.
	require.NoError(t, files.Write(consolidationPath("dream-bad"), []byte("{not-json")))
	_, err := jobs.Load(context.Background(), "dream-bad")
	require.Error(t, err)
}

func TestFileJobStoreListSkipsNonJSONAndReadErr(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)

	// A valid json job.
	require.NoError(t, jobs.Save(context.Background(), types.ContextConsolidation{ID: "dream-ok"}))

	// A non-json file in the same dir.
	require.NoError(t, files.Write(filepath.Join(filepath.Dir(consolidationPath("x")), "notes.txt"), []byte("hello")))

	got, err := jobs.List(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "dream-ok", got[0].ID)
}

func TestFileJobStoreListUnmarshalError(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	require.NoError(t, files.Write(consolidationPath("dream-bad"), []byte("{bad")))
	_, err := jobs.List(context.Background())
	require.Error(t, err)
}

func TestFileJobStoreListSortByCreatedAt(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)

	older := time.Now().UTC().Add(-time.Hour)
	newer := time.Now().UTC()
	require.NoError(t, jobs.Save(context.Background(), types.ContextConsolidation{ID: "newer", Status: "queued", CreatedAt: newer}))
	require.NoError(t, jobs.Save(context.Background(), types.ContextConsolidation{ID: "older", Status: "queued", CreatedAt: older}))

	got, err := jobs.List(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "older", got[0].ID)
	require.Equal(t, "newer", got[1].ID)
}

func TestFileJobStoreListNilConfig(t *testing.T) {
	jobs := NewFileJobStore(nil)
	_, err := jobs.List(context.Background())
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// updateMemory CallID path + SaveAgent error path
// ---------------------------------------------------------------------------

func TestUpdateMemoryWithCallID(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	memories := memory.NewStore(files, memory.Limits{})
	worker := NewWorker(jobs, memories)

	// Pre-seed agent and team snapshots so both load succeed.
	require.NoError(t, memories.SaveAgent(context.Background(), types.MemorySnapshot{
		SessionID: "fs-call", TeamID: "team-call", CallID: "call-1",
	}))
	require.NoError(t, memories.SaveTeam(context.Background(), types.MemorySnapshot{
		SessionID: "fs-call", TeamID: "team-call",
	}))

	require.NoError(t, worker.Enqueue(context.Background(), types.ContextConsolidation{
		ID:            "dream-call",
		FlowSessionID: "fs-call",
		TeamID:        "team-call",
		CallID:        "call-1",
		Status:        "queued",
		Records: []types.SharedRecord{
			{Name: "A", Summary: "call summary"},
		},
	}))
	require.NoError(t, worker.ProcessOnce(context.Background()))

	job, err := jobs.Load(context.Background(), "dream-call")
	require.NoError(t, err)
	require.Equal(t, "completed", job.Status)

	agentSnap, err := memories.LoadAgent(context.Background(), "fs-call", "team-call", "call-1")
	require.NoError(t, err)
	require.Contains(t, agentSnap.Confirmed, job.ResultSummary)
}

func TestUpdateMemoryLoadTeamErrorMarksFailed(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)
	// memories nil would skip updateMemory; instead use a Store over a store
	// whose reads fail to force LoadTeam error. Use a failing FileStore.
	memories := memory.NewStore(&errFileStore{err: errors.New("read boom")}, memory.Limits{})
	worker := NewWorker(jobs, memories)

	require.NoError(t, worker.Enqueue(context.Background(), types.ContextConsolidation{
		ID:            "dream-loadteam-err",
		FlowSessionID: "fs-x",
		TeamID:        "team-x",
		Status:        "queued",
		Records: []types.SharedRecord{
			{Name: "A", Summary: "summary"},
		},
	}))
	// ProcessOnce returns the error from process (updateMemory failure).
	err := worker.ProcessOnce(context.Background())
	require.Error(t, err)

	job, loadErr := jobs.Load(context.Background(), "dream-loadteam-err")
	require.NoError(t, loadErr)
	require.Equal(t, "failed", job.Status)
	require.NotEmpty(t, job.Error)
}

func TestUpdateMemorySaveAgentErrorMarksFailed(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	jobs := NewFileJobStore(files)

	// Use the errFileStore for memories so the CallID branch's LoadAgent errors
	// deterministically, marking the job failed via the SaveAgent error path.
	memBad := memory.NewStore(&errFileStore{err: errors.New("load agent boom")}, memory.Limits{})
	workerBad := NewWorker(jobs, memBad)

	require.NoError(t, workerBad.Enqueue(context.Background(), types.ContextConsolidation{
		ID:            "dream-saveagent-err",
		FlowSessionID: "fs-y",
		TeamID:        "team-y",
		CallID:        "call-y",
		Status:        "queued",
		Records: []types.SharedRecord{
			{Name: "A", Summary: "summary"},
		},
	}))
	err := workerBad.ProcessOnce(context.Background())
	require.Error(t, err)

	job, loadErr := jobs.Load(context.Background(), "dream-saveagent-err")
	require.NoError(t, loadErr)
	require.Equal(t, "failed", job.Status)
	require.NotEmpty(t, job.Error)
}

// ---------------------------------------------------------------------------
// Enqueue / context error branches
// ---------------------------------------------------------------------------

func TestWorkerEnqueueNil(t *testing.T) {
	worker := NewWorker(nil, nil)
	require.Error(t, worker.Enqueue(context.Background(), types.ContextConsolidation{}))
}

func TestWorkerRecoverNil(t *testing.T) {
	worker := NewWorker(nil, nil)
	require.Error(t, worker.Recover(context.Background()))
}

func TestWorkerStartNil(t *testing.T) {
	worker := NewWorker(nil, nil)
	require.Error(t, worker.Start(context.Background()))
}

func TestWorkerStopNil(t *testing.T) {
	var w *Worker
	w.Stop() // must not panic
}

func TestSetCuratorNilWorker(t *testing.T) {
	var w *Worker
	w.SetCurator(nil, nil) // must not panic
}

func TestContextErr(t *testing.T) {
	require.Nil(t, contextErr(nil))
	require.Nil(t, contextErr(context.Background()))

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, contextErr(canceled))
}

// ---------------------------------------------------------------------------
// helpers / mock stores
// ---------------------------------------------------------------------------

// errFileStore implements storage.FileStore, failing Read with a fixed error.
type errFileStore struct {
	err error
}

func (m *errFileStore) Read(path string) ([]byte, error) { return nil, m.err }
func (m *errFileStore) Write(path string, data []byte) error {
	return os.MkdirAll(filepath.Dir(path), 0755)
}
func (m *errFileStore) Append(path string, data []byte) error { return m.err }
func (m *errFileStore) Delete(path string) error              { return m.err }
func (m *errFileStore) Exists(path string) bool               { return false }
func (m *errFileStore) List(dir string) ([]string, error)     { return nil, m.err }

// failingJobStore implements JobStore with configurable failures.
type failingJobStore struct {
	mu       sync.Mutex
	listErr  error
	saveErr  error
	listJobs []types.ContextConsolidation
}

func (m *failingJobStore) Save(ctx context.Context, job types.ContextConsolidation) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	return nil
}
func (m *failingJobStore) Load(ctx context.Context, id string) (*types.ContextConsolidation, error) {
	return nil, ErrConsolidationJobNotFound
}
func (m *failingJobStore) List(ctx context.Context) ([]types.ContextConsolidation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listJobs, nil
}
func (m *failingJobStore) Delete(ctx context.Context, id string) error { return nil }
