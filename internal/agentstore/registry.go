// Package agentstore persists dynamic Agent entities (design doc 20): the
// runtime-created counterparts of statically configured Agents. An entity is
// an identity (template + key) with its own persistent memory, stored under
// .agents/data/agents/<template>/<key>/.
package agentstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
)

const (
	// maxKeyLength bounds one entity key (decision D1).
	maxKeyLength = 128
)

// Entity is the durable identity of one dynamic Agent entity.
type Entity struct {
	Agent      string    `json:"agent"`
	Key        string    `json:"key"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// Registry tracks dynamic Agent entities on top of a FileStore, mirroring the
// memory.Store construction pattern.
type Registry struct {
	files       storage.FileStore
	maxEntities int
	mu          sync.Mutex
}

// Option configures a Registry.
type Option func(*Registry)

// WithMaxEntities bounds how many entities one template may own. Zero (the
// default) means unlimited. Creating past the limit fails loudly; entities
// are official records and are never evicted silently.
func WithMaxEntities(max int) Option {
	return func(r *Registry) {
		r.maxEntities = max
	}
}

// NewRegistry creates a Registry rooted at the FileStore base directory.
func NewRegistry(files storage.FileStore, options ...Option) *Registry {
	registry := &Registry{files: files}
	for _, option := range options {
		option(registry)
	}
	return registry
}

// EnsureEntity returns the entity for agentID+key, creating it when missing
// and refreshing last_used_at when it already exists. An empty key is
// auto-generated; explicit keys are sanitized per the D1 rules.
func (r *Registry) EnsureEntity(ctx context.Context, agentID, key string) (*Entity, error) {
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
		key = r.generateKey(agentID)
	}
	path := entityPath(agentID, key)

	data, err := r.files.Read(path)
	if err == nil {
		var entity Entity
		if jsonErr := json.Unmarshal(data, &entity); jsonErr != nil {
			return nil, fmt.Errorf("agentstore: parse entity %s/%s: %w", agentID, key, jsonErr)
		}
		entity.Agent = agentID
		entity.Key = key
		entity.LastUsedAt = time.Now().UTC()
		if writeErr := r.writeEntity(path, &entity); writeErr != nil {
			return nil, writeErr
		}
		return &entity, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}

	if r.maxEntities > 0 {
		count, countErr := r.countEntities(agentID)
		if countErr != nil {
			return nil, countErr
		}
		if count >= r.maxEntities {
			return nil, fmt.Errorf("agentstore: agent %q reached the max_entities limit of %d; refusing to create entity %q", agentID, r.maxEntities, key)
		}
	}
	now := time.Now().UTC()
	entity := &Entity{Agent: agentID, Key: key, CreatedAt: now, LastUsedAt: now}
	if writeErr := r.writeEntity(path, entity); writeErr != nil {
		return nil, writeErr
	}
	return entity, nil
}

// Get returns one entity without mutating it.
func (r *Registry) Get(ctx context.Context, agentID, key string) (*Entity, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	key = SanitizeKey(key)
	if key == "" {
		return nil, fmt.Errorf("agentstore: entity key is required")
	}
	data, err := r.files.Read(entityPath(agentID, key))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("agentstore: entity %s/%s not found", agentID, key)
	}
	if err != nil {
		return nil, err
	}
	var entity Entity
	if jsonErr := json.Unmarshal(data, &entity); jsonErr != nil {
		return nil, fmt.Errorf("agentstore: parse entity %s/%s: %w", agentID, key, jsonErr)
	}
	return &entity, nil
}

// List returns every entity of one template. Entries without a readable
// entity.json are skipped.
func (r *Registry) List(ctx context.Context, agentID string) ([]*Entity, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	entries, err := r.files.List(templateDir(agentID))
	if err != nil {
		return nil, err
	}
	var entities []*Entity
	for _, entry := range entries {
		data, readErr := r.files.Read(entityPath(agentID, entry))
		if readErr != nil {
			continue
		}
		var entity Entity
		if jsonErr := json.Unmarshal(data, &entity); jsonErr != nil {
			continue
		}
		entities = append(entities, &entity)
	}
	return entities, nil
}

func (r *Registry) writeEntity(path string, entity *Entity) error {
	data, err := json.MarshalIndent(entity, "", "  ")
	if err != nil {
		return err
	}
	return r.files.Write(path, data)
}

func (r *Registry) countEntities(agentID string) (int, error) {
	entries, err := r.files.List(templateDir(agentID))
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if r.files.Exists(entityPath(agentID, entry)) {
			count++
		}
	}
	return count, nil
}

// generateKey produces a fresh key under the registry lock, retrying in the
// unlikely case of a nanosecond collision.
func (r *Registry) generateKey(agentID string) string {
	for attempt := 0; attempt < 16; attempt++ {
		key := fmt.Sprintf("e-%d", time.Now().UnixNano()+int64(attempt))
		if !r.files.Exists(entityPath(agentID, key)) {
			return key
		}
	}
	return fmt.Sprintf("e-%d-x", time.Now().UnixNano())
}

func templateDir(agentID string) string {
	return filepath.Join(".agents", "data", "agents", agentID)
}

func entityPath(agentID, key string) string {
	return filepath.Join(templateDir(agentID), key, "entity.json")
}

// SanitizeKey applies the D1 key rules: keep [A-Za-z0-9._-], replace anything
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
