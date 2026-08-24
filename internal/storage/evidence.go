package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// EvidenceStore persists Flow-scope SharedRecords in evidence.jsonl.
// Tool output and internal execution events do not belong here unless they
// have been promoted to a SharedRecord by the runtime.
type EvidenceStore interface {
	Publish(ctx context.Context, sessionID string, record types.SharedRecord) error
	Get(ctx context.Context, sessionID string, recordID string) (*types.SharedRecord, error)
	List(ctx context.Context, sessionID string, scope types.RecordScope) ([]types.SharedRecord, error)
}

// JSONLEvidenceStore is an append-only evidence store. It keeps the complete
// record history, including superseded and invalidated records; callers can
// decide which status is active.
type JSONLEvidenceStore struct {
	fileStore FileStore
	mu        sync.Mutex
}

func NewJSONLEvidenceStore(fileStore FileStore) *JSONLEvidenceStore {
	return &JSONLEvidenceStore{fileStore: fileStore}
}

func (s *JSONLEvidenceStore) Publish(ctx context.Context, sessionID string, record types.SharedRecord) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session id is required")
	}
	if strings.TrimSpace(record.RecordID) == "" {
		return errors.New("record id is required")
	}
	if strings.TrimSpace(record.Name) == "" {
		return errors.New("record name is required")
	}
	if record.Status == "" {
		record.Status = types.RecordActive
	}
	if record.Revision <= 0 {
		record.Revision = 1
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now()
	}

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal evidence record: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(".agents", "data", "sessions", sessionID, "evidence.jsonl")
	if err := s.fileStore.Append(path, data); err != nil {
		return fmt.Errorf("append evidence record: %w", err)
	}
	return nil
}

func (s *JSONLEvidenceStore) Get(ctx context.Context, sessionID string, recordID string) (*types.SharedRecord, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(recordID) == "" {
		return nil, errors.New("record id is required")
	}

	records, err := s.listAll(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].RecordID == recordID {
			record := records[i]
			return &record, nil
		}
	}
	return nil, ErrNotFound
}

func (s *JSONLEvidenceStore) List(ctx context.Context, sessionID string, scope types.RecordScope) ([]types.SharedRecord, error) {
	records, err := s.listAll(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if scope == "" {
		return records, nil
	}

	filtered := make([]types.SharedRecord, 0, len(records))
	for _, record := range records {
		if record.Scope == scope {
			filtered = append(filtered, record)
		}
	}
	return filtered, nil
}

func (s *JSONLEvidenceStore) listAll(ctx context.Context, sessionID string) ([]types.SharedRecord, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("session id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(".agents", "data", "sessions", sessionID, "evidence.jsonl")
	data, err := s.fileStore.Read(path)
	if err != nil {
		return nil, err
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}

	records := make([]types.SharedRecord, 0)
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		var record types.SharedRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("decode evidence record: %w", err)
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, ErrNotFound
	}
	return records, nil
}
