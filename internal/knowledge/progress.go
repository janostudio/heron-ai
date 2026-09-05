package knowledge

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
)

// ProgressPath is the filename (relative to the knowledge root) that records
// how far each session has been learned. Each line is a JSON object mapping a
// session ID to the last event seq that has been distilled into knowledge.
const ProgressPath = "learn-progress.jsonl"

// progressRecord is a single line in the learn-progress file.
type progressRecord struct {
	SessionID string `json:"session_id"`
	LastSeq   int64  `json:"last_seq"`
	UpdatedAt string `json:"updated_at"`
}

// LearnProgress records and reads incremental learning checkpoints per
// session. It is backed by a JSONL file under the knowledge root so learning
// can resume from the last distilled seq after a session is extended.
type LearnProgress struct {
	files storage.FileStore
	root  string
}

// NewLearnProgress constructs a LearnProgress rooted at the knowledge
// directory. The progress file lives at <root>/learn-progress.jsonl.
func NewLearnProgress(files storage.FileStore, root string) *LearnProgress {
	return &LearnProgress{files: files, root: root}
}

// LastSeq returns the last learned seq for a session, or 0 if the session has
// never been learned. A missing or unreadable progress file yields 0 and no
// error so a fresh session always starts from the beginning.
func (p *LearnProgress) LastSeq(sessionID string) (int64, error) {
	if p == nil || p.files == nil {
		return 0, nil
	}
	data, err := p.files.Read(p.progressPath())
	if err != nil {
		if err == storage.ErrNotFound {
			return 0, nil
		}
		return 0, fmt.Errorf("read learn progress: %w", err)
	}
	var lastSeq int64
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec progressRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.SessionID == sessionID {
			lastSeq = rec.LastSeq
			found = true
		}
	}
	if !found {
		return 0, nil
	}
	return lastSeq, nil
}

// Update records the given last_seq checkpoint for a session by appending a
// new progress line. The latest line for a session wins when read back.
func (p *LearnProgress) Update(sessionID string, lastSeq int64) error {
	if p == nil || p.files == nil {
		return nil
	}
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session id is required")
	}
	rec := progressRecord{
		SessionID: sessionID,
		LastSeq:   lastSeq,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal learn progress: %w", err)
	}
	if err := p.files.Append(p.progressPath(), append(data, '\n')); err != nil {
		return fmt.Errorf("append learn progress: %w", err)
	}
	return nil
}

func (p *LearnProgress) progressPath() string {
	if p.root == "" {
		return ProgressPath
	}
	return filepath.Join(p.root, ProgressPath)
}
