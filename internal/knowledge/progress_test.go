package knowledge

import (
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
)

func TestLearnProgress_LastSeqDefaultsToZero(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	p := NewLearnProgress(files, "knowledge")

	seq, err := p.LastSeq("fs_unknown")
	if err != nil {
		t.Fatalf("LastSeq: %v", err)
	}
	if seq != 0 {
		t.Fatalf("expected 0 for unlearned session, got %d", seq)
	}
}

func TestLearnProgress_UpdateAndReadBack(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	p := NewLearnProgress(files, "knowledge")

	if err := p.Update("fs_a", 42); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := p.Update("fs_b", 7); err != nil {
		t.Fatalf("Update: %v", err)
	}

	gotA, err := p.LastSeq("fs_a")
	if err != nil {
		t.Fatalf("LastSeq fs_a: %v", err)
	}
	if gotA != 42 {
		t.Fatalf("expected 42, got %d", gotA)
	}
	gotB, err := p.LastSeq("fs_b")
	if err != nil {
		t.Fatalf("LastSeq fs_b: %v", err)
	}
	if gotB != 7 {
		t.Fatalf("expected 7, got %d", gotB)
	}
}

func TestLearnProgress_LastUpdateWins(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	p := NewLearnProgress(files, "knowledge")

	if err := p.Update("fs_a", 10); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := p.Update("fs_a", 99); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := p.LastSeq("fs_a")
	if err != nil {
		t.Fatalf("LastSeq: %v", err)
	}
	if got != 99 {
		t.Fatalf("expected latest 99, got %d", got)
	}
}

func TestLearnProgress_RejectsEmptySessionID(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	p := NewLearnProgress(files, "knowledge")

	if err := p.Update("", 1); err == nil {
		t.Fatal("expected error on empty session id")
	}
}
