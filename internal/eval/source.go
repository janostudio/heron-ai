package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// FactSource reads the persisted fact sources of one FlowSession.
//
// Phase 1 of the eval capability (see docs/generic-engine/15 §10) needs
// exactly two of them: the merged event timeline that the runtime writes to
// flow.jsonl / team.jsonl / agent.jsonl, and the Flow-scope SharedRecord
// history in evidence.jsonl. Everything eval derives comes from these; eval
// never asks the runtime for in-memory state, so it works equally well on a
// live session and on an archived one.
type FactSource interface {
	// Events returns the FlowSession timeline, ordered by the globally
	// monotonic Seq the storage layer allocated across the three layer files.
	// A session with no persisted events yet yields an empty slice, not an
	// error.
	Events(ctx context.Context, flowSessionID string) ([]storage.SessionEvent, error)
	// Records returns the complete evidence.jsonl history, including
	// superseded and invalidated revisions.
	Records(ctx context.Context, flowSessionID string) ([]types.SharedRecord, error)
}

// FileFactSource is the on-disk FactSource. It delegates to the same storage
// primitives the runtime writes through, so eval reads the exact bytes the
// runtime produced instead of a parallel parser.
type FileFactSource struct {
	writer   *storage.JSONLSessionWriter
	evidence *storage.JSONLEvidenceStore
}

// NewFileFactSource builds a FileFactSource over a FileStore whose base
// directory is the configuration directory containing .agents/data.
func NewFileFactSource(store storage.FileStore) *FileFactSource {
	return &FileFactSource{
		writer:   storage.NewJSONLSessionWriter(store),
		evidence: storage.NewJSONLEvidenceStore(store),
	}
}

// Events replays the three per-layer files into one timeline.
func (s *FileFactSource) Events(ctx context.Context, flowSessionID string) ([]storage.SessionEvent, error) {
	replay, err := s.writer.Replay(ctx, flowSessionID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return replay.Events, nil
}

// Records reads evidence.jsonl. A session that never published a Flow-scope
// SharedRecord has no such file; that is an empty history, not an error.
func (s *FileFactSource) Records(ctx context.Context, flowSessionID string) ([]types.SharedRecord, error) {
	records, err := s.evidence.List(ctx, flowSessionID, "")
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	return records, err
}

// Observer derives eval observations from a FlowSession's fact sources.
type Observer struct {
	source FactSource
}

// NewObserver builds an Observer over a FactSource.
func NewObserver(source FactSource) *Observer {
	return &Observer{source: source}
}

// Observe loads one FlowSession and derives both the correlation graph and
// the per-turn observations from it.
func (o *Observer) Observe(ctx context.Context, flowSessionID string) (*SessionFacts, error) {
	if strings.TrimSpace(flowSessionID) == "" {
		return nil, errors.New("flow session id is required")
	}

	events, err := o.source.Events(ctx, flowSessionID)
	if err != nil {
		return nil, fmt.Errorf("read session events: %w", err)
	}
	records, err := o.source.Records(ctx, flowSessionID)
	if err != nil {
		return nil, fmt.Errorf("read evidence: %w", err)
	}

	graph := NewGraph(events)
	facts := &SessionFacts{
		FlowSessionID: flowSessionID,
		Events:        events,
		Graph:         graph,
		Evidence:      records,
	}
	facts.FlowTurns = deriveFlowTurns(graph, events)
	return facts, nil
}
