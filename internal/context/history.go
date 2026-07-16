package context

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// RunLog manages the global run history (run.jsonl)
type RunLog struct {
	fileStore storage.FileStore
}

func NewRunLog(fileStore storage.FileStore) *RunLog {
	return &RunLog{fileStore: fileStore}
}

func (l *RunLog) runLogPath(runID string) string {
	return filepath.Join(".agents", "data", runID, "run.jsonl")
}

func (l *RunLog) Append(ctx context.Context, runID string, msg types.Message) error {
	path := l.runLogPath(runID)
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')
	return l.fileStore.Append(path, data)
}

func (l *RunLog) List(ctx context.Context, runID string) ([]types.Message, error) {
	path := l.runLogPath(runID)
	data, err := l.fileStore.Read(path)
	if err != nil {
		return nil, nil
	}

	var messages []types.Message
	lines := splitLines(string(data))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var msg types.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		messages = append(messages, msg)
	}

	return messages, nil
}

func (l *RunLog) ListRecent(ctx context.Context, runID string, n int) ([]types.Message, error) {
	messages, err := l.List(ctx, runID)
	if err != nil {
		return nil, err
	}
	if n > 0 && n < len(messages) {
		messages = messages[len(messages)-n:]
	}
	return messages, nil
}
