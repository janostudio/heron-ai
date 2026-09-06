// Package agentstore no longer persists dynamic Agent instances as separate
// identity files. Since design doc 26, the instance layer has been folded
// into the agent layer: state is one workbench per agent (see internal/state).
// What remains here is the key machinery for concurrent instances — a key is
// now a transient instance number used to distinguish concurrent turns (and
// their call IDs / session events), not a durable identity.
package agentstore

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
)

const (
	// maxKeyLength bounds one instance key.
	maxKeyLength = 128
)

// Instance is the transient identity of one concurrent Agent instance. The
// key is an instance number used for call/event disambiguation only; no file
// is persisted for it.
type Instance struct {
	Agent string `json:"agent"`
	Key   string `json:"key"`
	// CreatedAt and LastUsedAt are kept for call-site compatibility but carry
	// no persistence semantics anymore.
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// Registry produces instance keys for concurrent Agent instances. It no
// longer reads or writes an identity file.
type Registry struct {
	mu  sync.Mutex
	seq atomic.Uint64
}

// Option configures a Registry. Retained for API compatibility; the former
// WithMaxInstances bound is no longer meaningful since instances are not
// persisted.
type Option func(*Registry)

// WithMaxInstances is a no-op kept for backward compatibility with callers.
func WithMaxInstances(max int) Option {
	return func(r *Registry) {}
}

// NewRegistry creates a Registry. The files argument is accepted for
// signature compatibility but no longer used (instances are not persisted).
func NewRegistry(files storage.FileStore, options ...Option) *Registry {
	registry := &Registry{}
	for _, option := range options {
		option(registry)
	}
	return registry
}

// NextInstanceKey returns the instance key for agentID+key. An empty key is
// auto-generated; explicit keys are sanitized. This is a pure in-memory
// operation — no identity file is created.
func (r *Registry) NextInstanceKey(ctx context.Context, agentID, key string) (*Instance, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agentstore: agent id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	key = SanitizeKey(key)
	if key == "" {
		key = r.generateKey()
	}
	now := time.Now().UTC()
	return &Instance{Agent: agentID, Key: key, CreatedAt: now, LastUsedAt: now}, nil
}

// generateKey produces a fresh instance key under the registry lock. Instance
// keys need not be durable, but must be unique within the process lifetime so
// concurrent turns of the same agent stay distinguishable. A monotonic atomic
// counter guarantees uniqueness, unlike time.Now().UnixNano() which can return
// the same value on consecutive calls.
func (r *Registry) generateKey() string {
	return fmt.Sprintf("e-%d", r.seq.Add(1))
}

// SanitizeKey applies the key rules: keep [A-Za-z0-9._-], replace anything
// else with "_", and bound the length with a short hash suffix. Empty input
// returns empty so the caller can generate a fresh key.
func SanitizeKey(key string) string {
	if key == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(key))
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	sanitized := builder.String()
	if len(sanitized) > maxKeyLength {
		sanitized = sanitized[:maxKeyLength-hashSuffixLen-1] + "-" + shortHash(sanitized)
	}
	return sanitized
}

const hashSuffixLen = 8

func shortHash(value string) string {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(value))
	return fmt.Sprintf("%08x", hasher.Sum32())
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
