package knowledge

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
)

// StatsPath is the filename (relative to the knowledge root) that records
// per-hit events as JSONL. Each injected knowledge entry appends one line
// here; hit counts are aggregated on demand rather than rewritten into the
// entry's frontmatter.
const StatsPath = "stats.jsonl"

// StatsRecorder appends hit events to a JSONL stats file and aggregates
// per-entry hit counts. It is optional: the injector works without it, but
// when present every successful knowledge match is recorded.
type StatsRecorder struct {
	files storage.FileStore
	root  string
}

// NewStatsRecorder constructs a StatsRecorder rooted at the knowledge
// directory. The stats file lives at <root>/stats.jsonl.
func NewStatsRecorder(files storage.FileStore, root string) *StatsRecorder {
	return &StatsRecorder{files: files, root: root}
}

// RecordHit appends one {id, ts} line for a matched knowledge entry.
func (r *StatsRecorder) RecordHit(id string) error {
	if r == nil || r.files == nil {
		return nil
	}
	if strings.TrimSpace(id) == "" {
		return nil
	}
	line := map[string]string{
		"id": id,
		"ts": time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("marshal stats hit: %w", err)
	}
	path := r.statsPath()
	if err := r.files.Append(path, append(data, '\n')); err != nil {
		return fmt.Errorf("append stats %s: %w", path, err)
	}
	return nil
}

// HitCounts aggregates the stats file into a map of entry ID -> hit count.
// A missing or unreadable stats file yields an empty map and no error so
// callers can render zero counts without special handling.
func (r *StatsRecorder) HitCounts() (map[string]int, error) {
	if r == nil || r.files == nil {
		return map[string]int{}, nil
	}
	path := r.statsPath()
	data, err := r.files.Read(path)
	if err != nil {
		if err == storage.ErrNotFound {
			return map[string]int{}, nil
		}
		return nil, fmt.Errorf("read stats %s: %w", path, err)
	}

	counts := make(map[string]int)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.ID != "" {
			counts[rec.ID]++
		}
	}
	return counts, nil
}

func (r *StatsRecorder) statsPath() string {
	if r.root == "" {
		return StatsPath
	}
	return filepath.Join(r.root, StatsPath)
}
