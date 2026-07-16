package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// FileStore is the file storage interface
type FileStore interface {
	Read(path string) ([]byte, error)
	Write(path string, data []byte) error
	Append(path string, data []byte) error
	Exists(path string) bool
	List(dir string) ([]string, error)
}

// Errors
var (
	ErrNotFound = errors.New("file not found")
)

// FileStoreImpl implements FileStore
type FileStoreImpl struct {
	baseDir string
	mu      sync.RWMutex
}

func NewFileStore(baseDir string) *FileStoreImpl {
	return &FileStoreImpl{baseDir: baseDir}
}

func (fs *FileStoreImpl) fullPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(fs.baseDir, path)
}

func (fs *FileStoreImpl) Read(path string) ([]byte, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	fullPath := fs.fullPath(path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

func (fs *FileStoreImpl) Write(path string, data []byte) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	fullPath := fs.fullPath(path)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(fullPath, data, 0644)
}

func (fs *FileStoreImpl) Append(path string, data []byte) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	fullPath := fs.fullPath(path)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	f, err := os.OpenFile(fullPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}

func (fs *FileStoreImpl) Exists(path string) bool {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	fullPath := fs.fullPath(path)
	_, err := os.Stat(fullPath)
	return err == nil
}

func (fs *FileStoreImpl) List(dir string) ([]string, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	fullPath := fs.fullPath(dir)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

// RunStateStore persists run state
type RunStateStore struct {
	fileStore FileStore
}

func NewRunStateStore(fileStore FileStore) *RunStateStore {
	return &RunStateStore{fileStore: fileStore}
}

func (s *RunStateStore) Save(ctx context.Context, runID string, state interface{}) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.fileStore.Write(filepath.Join(".agents", "data", runID, "run_state.json"), data)
}

func (s *RunStateStore) Load(ctx context.Context, runID string, target interface{}) error {
	data, err := s.fileStore.Read(filepath.Join(".agents", "data", runID, "run_state.json"))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// RunLog manages append-only run history (run.jsonl)
type RunLog struct {
	fileStore FileStore
}

func NewRunLog(fileStore FileStore) *RunLog {
	return &RunLog{fileStore: fileStore}
}

func (l *RunLog) Append(ctx context.Context, runID string, msg interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return l.fileStore.Append(filepath.Join(".agents", "data", runID, "run.jsonl"), data)
}

func (l *RunLog) Read(ctx context.Context, runID string, target interface{}) error {
	data, err := l.fileStore.Read(filepath.Join(".agents", "data", runID, "run.jsonl"))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// CheckpointManager manages checkpoints
type CheckpointManager struct {
	fileStore FileStore
}

func NewCheckpointManager(fileStore FileStore) *CheckpointManager {
	return &CheckpointManager{fileStore: fileStore}
}

func (m *CheckpointManager) Save(ctx context.Context, cp interface{}) error {
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	return m.fileStore.Write(filepath.Join("checkpoints", "latest.json"), data)
}

func (m *CheckpointManager) Load(ctx context.Context, target interface{}) error {
	data, err := m.fileStore.Read(filepath.Join("checkpoints", "latest.json"))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
