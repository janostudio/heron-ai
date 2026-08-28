package agent

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

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

var ErrToolTaskNotFound = errors.New("tool task not found")

type FileToolTaskStore struct {
	files   storage.FileStore
	mu      sync.Mutex
	subs    map[string]map[int]chan types.ToolTask
	nextSub int
}

func NewFileToolTaskStore(files storage.FileStore) *FileToolTaskStore {
	return &FileToolTaskStore{files: files, subs: make(map[string]map[int]chan types.ToolTask)}
}

func (s *FileToolTaskStore) Save(ctx context.Context, task types.ToolTask) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil || s.files == nil {
		return errors.New("tool task store is not configured")
	}
	if task.ID == "" {
		return errors.New("tool task id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	if err := s.files.Write(toolTaskPath(task.ID), data); err != nil {
		return err
	}
	s.publishLocked(task)
	return nil
}

func (s *FileToolTaskStore) Load(ctx context.Context, id string) (*types.ToolTask, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.files == nil {
		return nil, errors.New("tool task store is not configured")
	}
	if id == "" {
		return nil, errors.New("tool task id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.files.Read(toolTaskPath(id))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, ErrToolTaskNotFound
		}
		return nil, err
	}
	var task types.ToolTask
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *FileToolTaskStore) List(ctx context.Context) ([]types.ToolTask, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.files == nil {
		return nil, errors.New("tool task store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := s.files.List(filepath.Dir(toolTaskPath("placeholder")))
	if err != nil {
		return nil, err
	}
	tasks := make([]types.ToolTask, 0, len(names))
	for _, name := range names {
		if filepath.Ext(name) != ".json" {
			continue
		}
		id := strings.TrimSuffix(name, filepath.Ext(name))
		data, readErr := s.files.Read(toolTaskPath(id))
		if readErr != nil {
			continue
		}
		var task types.ToolTask
		if err := json.Unmarshal(data, &task); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].UpdatedAt.Before(tasks[j].UpdatedAt)
	})
	return tasks, nil
}

func (s *FileToolTaskStore) Delete(ctx context.Context, id string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil || s.files == nil {
		return errors.New("tool task store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files.Delete(toolTaskPath(id))
}

func (s *FileToolTaskStore) Subscribe(ctx context.Context, id string) (<-chan types.ToolTask, error) {
	if s == nil || s.files == nil {
		return nil, errors.New("tool task store is not configured")
	}
	if id == "" {
		return nil, errors.New("tool task id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	task, err := s.loadLocked(id)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.nextSub++
	subID := s.nextSub
	ch := make(chan types.ToolTask, 32)
	if s.subs[id] == nil {
		s.subs[id] = make(map[int]chan types.ToolTask)
	}
	s.subs[id][subID] = ch
	ch <- *task
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		if subscribers := s.subs[id]; subscribers != nil {
			if current, ok := subscribers[subID]; ok {
				delete(subscribers, subID)
				close(current)
			}
			if len(subscribers) == 0 {
				delete(s.subs, id)
			}
		}
		s.mu.Unlock()
	}()
	return ch, nil
}

func (s *FileToolTaskStore) UpdateProgress(ctx context.Context, id string, progress float64, message string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil || s.files == nil {
		return errors.New("tool task store is not configured")
	}
	if id == "" {
		return errors.New("tool task id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.loadLocked(id)
	if err != nil {
		return err
	}
	if task.Status == types.ToolTaskCompleted ||
		task.Status == types.ToolTaskFailed ||
		task.Status == types.ToolTaskCancelled {
		return nil
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	task.Progress = progress
	task.Message = message
	task.UpdatedAt = time.Now().UTC()
	return s.saveLocked(*task)
}

func (s *FileToolTaskStore) loadLocked(id string) (*types.ToolTask, error) {
	data, err := s.files.Read(toolTaskPath(id))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, ErrToolTaskNotFound
		}
		return nil, err
	}
	var task types.ToolTask
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *FileToolTaskStore) saveLocked(task types.ToolTask) error {
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	if err := s.files.Write(toolTaskPath(task.ID), data); err != nil {
		return err
	}
	s.publishLocked(task)
	return nil
}

func (s *FileToolTaskStore) publishLocked(task types.ToolTask) {
	for subID, ch := range s.subs[task.ID] {
		select {
		case ch <- task:
		default:
			// A slow subscriber can reconnect and read the durable current
			// value. Do not let progress reporting block the worker.
			delete(s.subs[task.ID], subID)
			close(ch)
		}
	}
	if len(s.subs[task.ID]) == 0 {
		delete(s.subs, task.ID)
	}
}

func toolTaskPath(id string) string {
	return filepath.Join(".agents", "data", "tool-tasks", id+".json")
}

type AsyncToolExecutor struct {
	tasks    types.ToolTaskStore
	executor ToolExecutor
	mu       sync.Mutex
	running  map[string]context.CancelFunc
	starting map[string]bool
	notified map[string]bool
	onDone   func(context.Context, types.ToolTask)
}

// ProgressToolExecutor is an optional extension for Tools that can report
// meaningful intermediate progress. Ordinary Tools continue to use Execute.
type ProgressToolExecutor interface {
	ExecuteWithProgress(
		ctx context.Context,
		name string,
		args map[string]any,
		report func(progress float64, message string),
	) (*types.ToolResult, error)
}

func (e *AsyncToolExecutor) TaskStore() types.ToolTaskStore {
	if e == nil {
		return nil
	}
	return e.tasks
}

func (e *AsyncToolExecutor) SetCompletionHandler(handler func(context.Context, types.ToolTask)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.onDone = handler
	e.mu.Unlock()
}

func (e *AsyncToolExecutor) Subscribe(ctx context.Context, id string) (<-chan types.ToolTask, error) {
	if e == nil || e.tasks == nil {
		return nil, errors.New("async tool executor is not configured")
	}
	subscriber, ok := e.tasks.(types.ToolTaskSubscriber)
	if !ok {
		return nil, errors.New("tool task store does not support subscriptions")
	}
	return subscriber.Subscribe(ctx, id)
}

func (e *AsyncToolExecutor) UpdateProgress(ctx context.Context, id string, progress float64, message string) error {
	if e == nil || e.tasks == nil {
		return errors.New("async tool executor is not configured")
	}
	updater, ok := e.tasks.(types.ToolTaskProgressUpdater)
	if !ok {
		return errors.New("tool task store does not support progress updates")
	}
	return updater.UpdateProgress(ctx, id, progress, message)
}

type DurableToolTaskRunner interface {
	Start(ctx context.Context, task types.ToolTask) error
	Load(ctx context.Context, id string) (*types.ToolTask, error)
	Recover(ctx context.Context) error
}

type toolExecutionInspector interface {
	ExecutionSpec(name string) types.ToolExecutionSpec
}

func NewAsyncToolExecutor(tasks types.ToolTaskStore, executor ToolExecutor) *AsyncToolExecutor {
	return &AsyncToolExecutor{
		tasks:    tasks,
		executor: executor,
		running:  make(map[string]context.CancelFunc),
		starting: make(map[string]bool),
	}
}

func (e *AsyncToolExecutor) Start(ctx context.Context, task types.ToolTask) error {
	return e.start(ctx, task, false)
}

func (e *AsyncToolExecutor) start(ctx context.Context, task types.ToolTask, forceExisting bool) error {
	if e == nil || e.tasks == nil || e.executor == nil {
		return errors.New("async tool executor is not configured")
	}
	e.mu.Lock()
	if _, ok := e.running[task.ID]; ok {
		e.mu.Unlock()
		return nil
	}
	if e.starting[task.ID] {
		e.mu.Unlock()
		return nil
	}
	if task.ID == "" {
		task.ID = fmt.Sprintf("task-%d", time.Now().UTC().UnixNano())
	}
	if !task.RestartSafe {
		if inspector, ok := e.executor.(toolExecutionInspector); ok {
			task.RestartSafe = inspector.ExecutionSpec(task.ToolName).RestartSafe
		}
	}
	if _, ok := e.running[task.ID]; ok || e.starting[task.ID] {
		e.mu.Unlock()
		return nil
	}
	e.starting[task.ID] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.starting, task.ID)
		e.mu.Unlock()
	}()
	if existing, err := e.tasks.Load(ctx, task.ID); err == nil {
		if existing.Status == types.ToolTaskCompleted ||
			existing.Status == types.ToolTaskFailed ||
			existing.Status == types.ToolTaskCancelled {
			return nil
		}
		if !forceExisting && (existing.Status == types.ToolTaskQueued ||
			existing.Status == types.ToolTaskRunning) {
			// Durable task IDs are idempotency keys. A retry or recovery
			// request must observe the existing task rather than enqueue a
			// second execution with the same side effects.
			return nil
		}
		if forceExisting && existing.Status == types.ToolTaskRunning {
			// A running record left by a previous process is an execution
			// lease, not proof that a live worker still exists.
			task.Status = types.ToolTaskQueued
		}
		if task.CreatedAt.IsZero() {
			task.CreatedAt = existing.CreatedAt
		}
	}
	now := time.Now().UTC()
	task.Version = 1
	task.Status = types.ToolTaskQueued
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	task.UpdatedAt = now
	if err := e.tasks.Save(ctx, task); err != nil {
		return err
	}
	go e.run(task)
	return nil
}

func (e *AsyncToolExecutor) Load(ctx context.Context, id string) (*types.ToolTask, error) {
	if e == nil || e.tasks == nil {
		return nil, errors.New("async tool executor is not configured")
	}
	return e.tasks.Load(ctx, id)
}

func (e *AsyncToolExecutor) List(ctx context.Context) ([]types.ToolTask, error) {
	if e == nil || e.tasks == nil {
		return nil, errors.New("async tool executor is not configured")
	}
	return e.tasks.List(ctx)
}

func (e *AsyncToolExecutor) run(task types.ToolTask) {
	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	delete(e.starting, task.ID)
	e.running[task.ID] = cancel
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, task.ID)
		e.mu.Unlock()
	}()

	now := time.Now().UTC()
	if current, loadErr := e.tasks.Load(context.Background(), task.ID); loadErr == nil {
		if current.Status == types.ToolTaskCancelled {
			return
		}
		task.Progress = current.Progress
		task.Message = current.Message
	}
	task.Status = types.ToolTaskRunning
	task.StartedAt = &now
	task.UpdatedAt = now
	_ = e.tasks.Save(context.Background(), task)

	var result *types.ToolResult
	var err error
	if progressExecutor, ok := e.executor.(ProgressToolExecutor); ok {
		result, err = progressExecutor.ExecuteWithProgress(ctx, task.ToolName, task.Arguments,
			func(progress float64, message string) {
				_ = e.UpdateProgress(context.Background(), task.ID, progress, message)
			})
	} else {
		result, err = e.executor.Execute(ctx, task.ToolName, task.Arguments)
	}
	finished := time.Now().UTC()
	if current, loadErr := e.tasks.Load(context.Background(), task.ID); loadErr == nil {
		task.Progress = current.Progress
		task.Message = current.Message
		if current.Status == types.ToolTaskCancelled {
			task.Status = types.ToolTaskCancelled
			if task.Error == "" {
				task.Error = current.Error
			}
		}
	}
	if task.Status != types.ToolTaskCancelled {
		task.Progress = 1
	}
	task.FinishedAt = &finished
	task.UpdatedAt = finished
	if ctx.Err() != nil {
		task.Status = types.ToolTaskCancelled
		task.Error = ctx.Err().Error()
	} else if err != nil {
		task.Status = types.ToolTaskFailed
		task.Error = err.Error()
	} else {
		task.Status = types.ToolTaskCompleted
		task.Result = result
	}
	_ = e.tasks.Save(context.Background(), task)
	e.notifyDone(task)
}

func (e *AsyncToolExecutor) Cancel(ctx context.Context, id string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	cancel, ok := e.running[id]
	e.mu.Unlock()
	if ok {
		cancel()
		task, err := e.tasks.Load(ctx, id)
		if err != nil {
			return err
		}
		if task.Status == types.ToolTaskQueued || task.Status == types.ToolTaskRunning {
			task.Status = types.ToolTaskCancelled
			task.Error = "task cancelled"
			task.UpdatedAt = time.Now().UTC()
			if err := e.tasks.Save(ctx, *task); err != nil {
				return err
			}
			e.notifyDone(*task)
		}
		return nil
	}
	task, err := e.tasks.Load(ctx, id)
	if err != nil {
		return err
	}
	if task.Status == types.ToolTaskQueued || task.Status == types.ToolTaskRunning {
		task.Status = types.ToolTaskCancelled
		task.Error = "task cancelled"
		task.UpdatedAt = time.Now().UTC()
		if err := e.tasks.Save(ctx, *task); err != nil {
			return err
		}
		e.notifyDone(*task)
		return nil
	}
	return nil
}

func (e *AsyncToolExecutor) notifyDone(task types.ToolTask) {
	if task.Status != types.ToolTaskCompleted &&
		task.Status != types.ToolTaskFailed &&
		task.Status != types.ToolTaskCancelled {
		return
	}
	e.mu.Lock()
	if e.notified == nil {
		e.notified = make(map[string]bool)
	}
	if e.notified[task.ID] {
		e.mu.Unlock()
		return
	}
	e.notified[task.ID] = true
	handler := e.onDone
	e.mu.Unlock()
	if handler != nil {
		go handler(context.Background(), task)
	}
}

func (e *AsyncToolExecutor) Recover(ctx context.Context) error {
	tasks, err := e.tasks.List(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.Status != types.ToolTaskQueued && task.Status != types.ToolTaskRunning {
			continue
		}
		if task.Status == types.ToolTaskRunning && !task.RestartSafe {
			task.Status = types.ToolTaskFailed
			task.Error = "task was interrupted by process restart; explicit retry is required"
			task.UpdatedAt = time.Now().UTC()
			if err := e.tasks.Save(ctx, task); err != nil {
				return err
			}
			e.notifyDone(task)
			continue
		}
		if err := e.start(ctx, task, true); err != nil {
			return err
		}
	}
	return nil
}
