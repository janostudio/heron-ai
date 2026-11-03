package script

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/heron-ai/heron-engine/internal/storage"
)

// FileStepStore keeps replay records as JSON lines, one file per script.
//
// Line format is append-only so an effectful call can record its intent before
// the effect lands and its result after: a rewrite-in-place store would have to
// seek, and a crash between the two writes is exactly the case this design has
// to survive.
type FileStepStore struct {
	files  storage.FileStore
	prefix string
}

// NewFileStepStore builds a store under prefix (for example "scripts").
func NewFileStepStore(files storage.FileStore, prefix string) *FileStepStore {
	if prefix == "" {
		prefix = "scripts"
	}
	return &FileStepStore{files: files, prefix: prefix}
}

func (s *FileStepStore) filePath(scriptID string) string {
	return path.Join(s.prefix, sanitizeSegment(scriptID)+".jsonl")
}

// Load returns every record for a script, in append order.
func (s *FileStepStore) Load(_ context.Context, scriptID string) ([]StepRecord, error) {
	if s.files == nil || scriptID == "" {
		return nil, nil
	}
	data, err := s.files.Read(s.filePath(scriptID))
	if err != nil {
		if storage.ErrNotFound == err || strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, err
	}
	var out []StepRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec StepRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// A corrupt line means the log is not trustworthy. Refusing to replay
			// is safer than guessing: the alternative is re-running an effectful
			// call whose record we cannot read.
			return nil, fmt.Errorf("corrupt replay record: %w", err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// Append writes one record. The write must be durable before it returns: the
// caller has already (or is about to) perform the effect.
func (s *FileStepStore) Append(_ context.Context, scriptID string, rec StepRecord) error {
	if s.files == nil || scriptID == "" {
		return nil
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.files.Append(s.filePath(scriptID), append(raw, '\n'))
}

// sanitizeSegment keeps a script id from becoming a path. Ids are generated from
// a hash and are already safe, but the store is also usable from tests that pass
// arbitrary strings.
func sanitizeSegment(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
