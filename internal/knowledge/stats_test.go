package knowledge

import (
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
)

func TestStatsRecorder_RecordAndAggregate(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	rec := NewStatsRecorder(files, "knowledge")

	if err := rec.RecordHit("entry-a"); err != nil {
		t.Fatalf("record hit: %v", err)
	}
	if err := rec.RecordHit("entry-a"); err != nil {
		t.Fatalf("record hit: %v", err)
	}
	if err := rec.RecordHit("entry-b"); err != nil {
		t.Fatalf("record hit: %v", err)
	}

	counts, err := rec.HitCounts()
	if err != nil {
		t.Fatalf("hit counts: %v", err)
	}
	if counts["entry-a"] != 2 {
		t.Fatalf("expected entry-a count 2, got %d", counts["entry-a"])
	}
	if counts["entry-b"] != 1 {
		t.Fatalf("expected entry-b count 1, got %d", counts["entry-b"])
	}
	if counts["missing"] != 0 {
		t.Fatalf("expected missing count 0, got %d", counts["missing"])
	}
}

func TestStatsRecorder_EmptyWhenNoStatsFile(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	rec := NewStatsRecorder(files, "knowledge")

	counts, err := rec.HitCounts()
	if err != nil {
		t.Fatalf("hit counts: %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("expected empty counts, got %v", counts)
	}
}

func TestStatsRecorder_NilRecorderIsNoop(t *testing.T) {
	var rec *StatsRecorder
	if err := rec.RecordHit("x"); err != nil {
		t.Fatalf("expected nil recorder to be noop, got %v", err)
	}
	counts, err := rec.HitCounts()
	if err != nil {
		t.Fatalf("expected nil recorder to be noop, got %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("expected empty counts, got %v", counts)
	}
}

func TestStatsRecorder_StatsPathWithinRoot(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	rec := NewStatsRecorder(files, "knowledge")
	if err := rec.RecordHit("a"); err != nil {
		t.Fatalf("record hit: %v", err)
	}
	// The stats file should be written under the knowledge root, not the
	// process cwd.
	path := rec.statsPath()
	if !strings.Contains(path, "knowledge") || !strings.HasSuffix(path, StatsPath) {
		t.Fatalf("unexpected stats path %q", path)
	}
	if !files.Exists(path) {
		t.Fatalf("expected stats file to exist at %q", path)
	}
}
