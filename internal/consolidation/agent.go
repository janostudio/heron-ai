// Package consolidation contains optional helpers for combining explicit
// SharedRecord values. Core TeamRuntime already performs configured output
// selection and does not require a special Aggregator concept.
package consolidation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/internal/knowledge"
	"github.com/heron-ai/heron-engine/internal/memory"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type RecordConsolidator struct{}

func NewRecordConsolidator() *RecordConsolidator {
	return &RecordConsolidator{}
}

func (c *RecordConsolidator) Consolidate(_ context.Context, records []types.SharedRecord) string {
	if len(records) == 0 {
		return ""
	}
	if len(records) == 1 {
		return records[0].Summary
	}

	ordered := append([]types.SharedRecord(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Name < ordered[j].Name
	})

	var parts []string
	parts = append(parts, "## Shared Records")
	for i, record := range ordered {
		name := record.Name
		if name == "" {
			name = fmt.Sprintf("record-%d", i+1)
		}
		parts = append(parts, fmt.Sprintf("### %s\n%s", name, record.Summary))
	}
	return strings.Join(parts, "\n\n")
}

var ErrConsolidationJobNotFound = errors.New("consolidation job not found")

type JobStore interface {
	Save(ctx context.Context, job types.ContextConsolidation) error
	Load(ctx context.Context, id string) (*types.ContextConsolidation, error)
	List(ctx context.Context) ([]types.ContextConsolidation, error)
	Delete(ctx context.Context, id string) error
}

// FileJobStore is the durable queue for Dream / Memory maintenance. Jobs are
// separate from session.jsonl so a maintenance failure cannot corrupt the
// user-facing execution timeline.
type FileJobStore struct {
	files storage.FileStore
	mu    sync.Mutex
}

func NewFileJobStore(files storage.FileStore) *FileJobStore {
	return &FileJobStore{files: files}
}

func (s *FileJobStore) Save(ctx context.Context, job types.ContextConsolidation) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil || s.files == nil {
		return errors.New("consolidation job store is not configured")
	}
	if strings.TrimSpace(job.ID) == "" {
		return errors.New("consolidation job id is required")
	}
	if job.Status == "" {
		job.Status = "queued"
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	job.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files.Write(consolidationPath(job.ID), data)
}

func (s *FileJobStore) Load(ctx context.Context, id string) (*types.ContextConsolidation, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.files == nil {
		return nil, errors.New("consolidation job store is not configured")
	}
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("consolidation job id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.files.Read(consolidationPath(id))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrConsolidationJobNotFound
	}
	if err != nil {
		return nil, err
	}
	var job types.ContextConsolidation
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *FileJobStore) List(ctx context.Context) ([]types.ContextConsolidation, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.files == nil {
		return nil, errors.New("consolidation job store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := s.files.List(filepath.Dir(consolidationPath("placeholder")))
	if err != nil {
		return nil, err
	}
	jobs := make([]types.ContextConsolidation, 0, len(names))
	for _, name := range names {
		if filepath.Ext(name) != ".json" {
			continue
		}
		data, readErr := s.files.Read(filepath.Join(filepath.Dir(consolidationPath("placeholder")), name))
		if readErr != nil {
			continue
		}
		var job types.ContextConsolidation
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
	return jobs, nil
}

// Delete removes a terminal job record from the durable queue. It is used by
// Recover to garbage-collect failed/completed jobs whose summary has already
// been written to memory, preventing unbounded disk growth.
func (s *FileJobStore) Delete(ctx context.Context, id string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil || s.files == nil {
		return errors.New("consolidation job store is not configured")
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("consolidation job id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files.Delete(consolidationPath(id))
}

func consolidationPath(id string) string {
	return filepath.Join(".agents", "data", "consolidations", id+".json")
}

// Worker performs deterministic Dream maintenance in the background. It
// intentionally does not call an LLM: long summaries remain a separate,
// explicit future capability and cannot block or alter the Agent answer.
type Worker struct {
	jobs           JobStore
	memories       *memory.Store
	consolidator   *RecordConsolidator
	curator        *knowledge.Curator
	knowledgeStore storage.FileStore
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
}

func NewWorker(jobs JobStore, memories *memory.Store) *Worker {
	return &Worker{
		jobs:         jobs,
		memories:     memories,
		consolidator: NewRecordConsolidator(),
	}
}

// SetCurator 注入可选的知识提炼路径。curator 为 nil 时关闭该路径。
// knowledgeFiles 用于把 Curator 产出写到 .agents/knowledge/proposed/。
func (w *Worker) SetCurator(curator *knowledge.Curator, knowledgeFiles storage.FileStore) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.curator = curator
	w.knowledgeStore = knowledgeFiles
}

func (w *Worker) Enqueue(ctx context.Context, job types.ContextConsolidation) error {
	if w == nil || w.jobs == nil {
		return errors.New("consolidation worker is not configured")
	}
	if job.Status == "" {
		job.Status = "queued"
	}
	if job.ID == "" {
		job.ID = fmt.Sprintf("dream-%d", time.Now().UTC().UnixNano())
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	return w.jobs.Save(ctx, job)
}

func (w *Worker) Recover(ctx context.Context) error {
	if w == nil || w.jobs == nil {
		return errors.New("consolidation worker is not configured")
	}
	jobs, err := w.jobs.List(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		switch job.Status {
		case "failed", "completed":
			// Terminal jobs: their summary has already been written to memory
			// (or the failure is recorded in the job itself). They will never
			// be re-consumed by ProcessOnce, so remove them to avoid unbounded
			// disk growth in .agents/data/consolidations/.
			if err := w.jobs.Delete(ctx, job.ID); err != nil {
				return err
			}
		case "running":
			job.Status = "queued"
			if err := w.jobs.Save(ctx, job); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *Worker) Start(ctx context.Context) error {
	if w == nil || w.jobs == nil {
		return errors.New("consolidation worker is not configured")
	}
	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return nil
	}
	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})
	done := w.done
	w.mu.Unlock()
	if err := w.Recover(workerCtx); err != nil {
		cancel()
		w.mu.Lock()
		w.cancel = nil
		close(done)
		w.mu.Unlock()
		return err
	}
	go w.loop(workerCtx, done)
	return nil
}

func (w *Worker) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	cancel := w.cancel
	done := w.done
	w.cancel = nil
	w.done = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (w *Worker) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := w.ProcessOnce(ctx); err != nil && ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) ProcessOnce(ctx context.Context) error {
	jobs, err := w.jobs.List(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.Status != "queued" {
			continue
		}
		if err := w.process(ctx, job); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) process(ctx context.Context, job types.ContextConsolidation) error {
	job.Status = "running"
	if err := w.jobs.Save(ctx, job); err != nil {
		return err
	}
	summary := w.consolidator.Consolidate(ctx, job.Records)
	job.ResultSummary = summary
	if w.curator != nil && len(job.Records) > 0 {
		_ = w.curateProposed(ctx, job) // 失败静默降级，不回写 job.Error
	}
	if strings.TrimSpace(summary) != "" && w.memories != nil && job.TeamID != "" {
		if err := w.updateMemory(ctx, job, summary); err != nil {
			job.Status = "failed"
			job.Error = err.Error()
			_ = w.jobs.Save(context.Background(), job)
			return err
		}
	}
	job.Status = "completed"
	return w.jobs.Save(ctx, job)
}

func (w *Worker) updateMemory(ctx context.Context, job types.ContextConsolidation, summary string) error {
	if job.CallID != "" {
		snapshot, err := w.memories.LoadAgent(ctx, job.FlowSessionID, job.TeamID, job.CallID)
		if err != nil {
			return err
		}
		snapshot.Confirmed = append(snapshot.Confirmed, summary)
		if err := w.memories.SaveAgent(ctx, snapshot); err != nil {
			return err
		}
	}
	snapshot, err := w.memories.LoadTeam(ctx, job.FlowSessionID, job.TeamID)
	if err != nil {
		return err
	}
	snapshot.Confirmed = append(snapshot.Confirmed, summary)
	return w.memories.SaveTeam(ctx, snapshot)
}

func (w *Worker) curateProposed(ctx context.Context, job types.ContextConsolidation) error {
	sources := buildCuratorSources(job.Records)
	md, err := w.curator.Curate(ctx, sources)
	if err != nil {
		return err
	}
	if strings.TrimSpace(md) == "" {
		return errors.New("curator returned empty markdown")
	}
	path := filepath.Join(".agents", "knowledge", "proposed", job.ID+".md")
	return w.knowledgeStore.Write(path, []byte(md+"\n"))
}

func buildCuratorSources(records []types.SharedRecord) []string {
	sources := make([]string, 0, len(records))
	for _, r := range records {
		name := strings.TrimSpace(r.Name)
		summary := strings.TrimSpace(r.Summary)
		switch {
		case name == "" && summary == "":
			continue
		case name == "":
			sources = append(sources, summary)
		case summary == "":
			sources = append(sources, name)
		default:
			sources = append(sources, fmt.Sprintf("[%s] %s", name, summary))
		}
	}
	return sources
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
