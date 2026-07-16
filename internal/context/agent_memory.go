package context

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// AgentStateStore manages agent structured state (state.json)
type AgentStateStore struct {
	fileStore storage.FileStore
	mu        sync.RWMutex
}

func NewAgentStateStore(fileStore storage.FileStore) *AgentStateStore {
	return &AgentStateStore{fileStore: fileStore}
}

func (s *AgentStateStore) statePath(runID, teamName, agentName string) string {
	return filepath.Join(".agents", "data", runID, "sessions", teamName+"-"+agentName, "state.json")
}

func (s *AgentStateStore) Read(ctx context.Context, runID, teamName, agentName string) (map[string]any, error) {
	path := s.statePath(runID, teamName, agentName)
	data, err := s.fileStore.Read(path)
	if err != nil {
		return make(map[string]any), nil // return empty state if not found
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return make(map[string]any), nil
	}
	return state, nil
}

func (s *AgentStateStore) Write(ctx context.Context, runID, teamName, agentName string, state map[string]any) error {
	path := s.statePath(runID, teamName, agentName)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return s.fileStore.Write(path, data)
}

// AgentMemoryStore manages agent memory entries (memory.jsonl)
type AgentMemoryStore struct {
	fileStore storage.FileStore
	mu        sync.RWMutex
}

func NewAgentMemoryStore(fileStore storage.FileStore) *AgentMemoryStore {
	return &AgentMemoryStore{fileStore: fileStore}
}

func (s *AgentMemoryStore) memoryPath(runID, teamName, agentName string) string {
	return filepath.Join(".agents", "data", runID, "sessions", teamName+"-"+agentName, "memory.jsonl")
}

func (s *AgentMemoryStore) Append(ctx context.Context, runID, teamName, agentName string, memory types.MemoryEntry) error {
	path := s.memoryPath(runID, teamName, agentName)
	data, err := json.Marshal(memory)
	if err != nil {
		return fmt.Errorf("marshal memory: %w", err)
	}
	data = append(data, '\n')
	return s.fileStore.Append(path, data)
}

func (s *AgentMemoryStore) ListRecent(ctx context.Context, runID, teamName, agentName string, n int) ([]types.MemoryEntry, error) {
	path := s.memoryPath(runID, teamName, agentName)
	data, err := s.fileStore.Read(path)
	if err != nil {
		return nil, nil // no memories yet
	}

	// Parse JSONL
	var entries []types.MemoryEntry
	lines := splitLines(string(data))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var entry types.MemoryEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}

	// Return last N entries
	if n > 0 && n < len(entries) {
		entries = entries[len(entries)-n:]
	}

	return entries, nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
