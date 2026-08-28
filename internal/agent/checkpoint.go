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

var ErrCheckpointNotFound = errors.New("agent checkpoint not found")

type FileCheckpointStore struct {
	files storage.FileStore
	mu    sync.Mutex
}

func NewFileCheckpointStore(files storage.FileStore) *FileCheckpointStore {
	return &FileCheckpointStore{files: files}
}

func (s *FileCheckpointStore) Save(ctx context.Context, checkpoint types.AgentCheckpoint) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil || s.files == nil {
		return errors.New("checkpoint file store is not configured")
	}
	if checkpoint.ID == "" {
		return errors.New("checkpoint id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agent checkpoint: %w", err)
	}
	return s.files.Write(checkpointPath(checkpoint.ID), data)
}

func (s *FileCheckpointStore) Load(ctx context.Context, id string) (*types.AgentCheckpoint, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.files == nil {
		return nil, errors.New("checkpoint file store is not configured")
	}
	if id == "" {
		return nil, errors.New("checkpoint id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.files.Read(checkpointPath(id))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, ErrCheckpointNotFound
		}
		return nil, err
	}
	var checkpoint types.AgentCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("decode agent checkpoint: %w", err)
	}
	return &checkpoint, nil
}

func (s *FileCheckpointStore) List(ctx context.Context) ([]types.AgentCheckpoint, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.files == nil {
		return nil, errors.New("checkpoint file store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := s.files.List(filepath.Dir(checkpointPath("placeholder")))
	if err != nil {
		return nil, err
	}
	checkpoints := make([]types.AgentCheckpoint, 0, len(names))
	for _, name := range names {
		if filepath.Ext(name) != ".json" {
			continue
		}
		id := strings.TrimSuffix(name, filepath.Ext(name))
		data, readErr := s.files.Read(checkpointPath(id))
		if readErr != nil {
			continue
		}
		var checkpoint types.AgentCheckpoint
		if err := json.Unmarshal(data, &checkpoint); err != nil {
			return nil, fmt.Errorf("decode agent checkpoint %q: %w", id, err)
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	sort.SliceStable(checkpoints, func(i, j int) bool {
		return checkpoints[i].UpdatedAt.Before(checkpoints[j].UpdatedAt)
	})
	return checkpoints, nil
}

func (s *FileCheckpointStore) Delete(ctx context.Context, id string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil || s.files == nil {
		return errors.New("checkpoint file store is not configured")
	}
	if id == "" {
		return errors.New("checkpoint id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files.Delete(checkpointPath(id))
}

func checkpointPath(id string) string {
	return filepath.Join(".agents", "data", "agent-checkpoints", id+".json")
}

func newCheckpointID(req types.AgentRequest) string {
	if req.AgentTurnID != "" {
		return req.AgentTurnID
	}
	if req.CallID != "" {
		return req.CallID
	}
	return fmt.Sprintf("agent-%d", time.Now().UnixNano())
}
