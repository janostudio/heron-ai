package types

import (
	"context"
	"sync"
)

// DefinitionsReloadFunc loads a fresh Definitions tree from the sources the
// store was built for. It is a plain function type rather than an interface on
// a config package type on purpose: pkg/types is imported by internal/config,
// so naming config.ConfigLoader or config.DefinitionsLoadRequest here would be
// an import cycle. The caller closes over its concrete loader instead, which
// also avoids duplicating the request struct.
type DefinitionsReloadFunc func(ctx context.Context) (*Definitions, error)

// DefinitionStore publishes the active Definitions tree behind a swappable
// pointer.
//
// The engine used to load .agents/ exactly once at process start and hand the
// resulting *Definitions to every consumer as a plain pointer. That made an
// agent able to create new Agent/Team definitions mid-conversation a dead
// feature: consumers kept resolving against the map they captured at build
// time. Consumers now hold a *DefinitionStore and call Snapshot() per turn, so
// a swap becomes visible on the next turn without a restart.
//
// Reads vastly outnumber writes (at least one Snapshot per turn and per team
// Run, one reload per definition change), so the store publishes whole trees by
// pointer swap and never copies on the read path.
type DefinitionStore struct {
	mu  sync.RWMutex
	cur *Definitions

	// root and flow are frozen at construction: they describe where this
	// store's definitions come from, and a reload must read the same
	// environment the running engine was started against.
	root string
	flow string
}

// NewDefinitionStore builds a store over an already-loaded tree. root and flow
// are the absolute config root and flow file path that a later Reload will
// re-read; pass empty strings when the store is only used as a holder (tests,
// embedded flows) and Reload will not be called.
func NewDefinitionStore(d *Definitions, root, flow string) *DefinitionStore {
	return &DefinitionStore{cur: d, root: root, flow: flow}
}

// Snapshot returns the currently published tree.
//
// The result is READ-ONLY: it is the live tree, not a copy, and a concurrent
// Swap replaces the pointer without touching what has already been handed out,
// so a caller that holds a snapshot keeps a coherent view for as long as it
// needs it. Callers must never mutate the returned value, nor any map or
// pointer reachable from it — mutating shared definitions in place would race
// with every other turn reading the same tree. To publish a change, build a
// new *Definitions and pass it to Swap.
//
// No defensive copy is made: this sits on the hot path (once per turn, once per
// team Run) and a deep copy of the whole Flow/Team/Agent graph per call would
// dominate the turn cost for a value that is immutable by contract.
func (s *DefinitionStore) Snapshot() *Definitions {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// ConfigRoot returns the absolute config root frozen at construction.
func (s *DefinitionStore) ConfigRoot() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.root
}

// FlowPath returns the absolute flow file path frozen at construction.
func (s *DefinitionStore) FlowPath() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.flow
}

// Swap publishes a new tree. A nil tree is a no-op rather than a publish:
// consumers treat a nil Snapshot as "no definitions", and letting a stray nil
// through would brick a running engine that has a perfectly good tree in hand.
func (s *DefinitionStore) Swap(d *Definitions) {
	if s == nil || d == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = d
}

// Reload re-reads definitions through load and publishes the result. The load
// function is supplied by the caller (which knows the concrete config loader);
// this package only needs "give me a fresh tree".
//
// On failure the previous tree stays published and the error is returned: a
// reload that cannot complete — a definition edited into an invalid state, a
// transient read error — must leave the engine exactly as it was. Swapping in a
// partial or nil tree here would turn a recoverable config typo into a dead
// process.
//
// The write lock is held across the load so concurrent reloads serialize. Two
// racing reloads would otherwise both read and publish, and the loser could win
// the race and undo the winner's publication with its older read.
func (s *DefinitionStore) Reload(ctx context.Context, load DefinitionsReloadFunc) error {
	if s == nil {
		return nil
	}
	if load == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := load(ctx)
	if err != nil {
		return err
	}
	// A loader that reports success while returning nothing would leave the
	// store pointing at nil; that is a loader bug, so fail loudly and keep the
	// old tree instead of silently clearing the engine's definitions.
	if d == nil {
		return &DefinitionsReloadError{Reason: "loader returned nil definitions"}
	}
	s.cur = d
	return nil
}

// DefinitionsReloadError reports a reload that was rejected without a
// lower-level error to wrap.
type DefinitionsReloadError struct {
	Reason string
}

func (e *DefinitionsReloadError) Error() string {
	if e == nil || e.Reason == "" {
		return "definition reload failed"
	}
	return "definition reload failed: " + e.Reason
}
