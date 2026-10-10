package osenv

import (
	"strings"
	"testing"
)

func lookup(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return entry[len(prefix):], true
		}
	}
	return "", false
}

func TestMergeOverridesExisting(t *testing.T) {
	got := Merge([]string{"AUTO=/old/path", "OTHER=keep"}, map[string]string{"AUTO": "/new/path"})

	if v, ok := lookup(got, "AUTO"); !ok || v != "/new/path" {
		t.Fatalf("AUTO = %q (present=%v), want /new/path", v, ok)
	}
	if v, ok := lookup(got, "OTHER"); !ok || v != "keep" {
		t.Fatalf("OTHER = %q (present=%v), want keep", v, ok)
	}
	count := 0
	for _, entry := range got {
		if strings.HasPrefix(entry, "AUTO=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("AUTO appears %d times, want 1", count)
	}
}

func TestMergeAddsNewKey(t *testing.T) {
	got := Merge([]string{"EXISTING=1"}, map[string]string{"AUTO": "/workspace"})
	if v, ok := lookup(got, "AUTO"); !ok || v != "/workspace" {
		t.Fatalf("AUTO = %q (present=%v), want /workspace", v, ok)
	}
	if v, ok := lookup(got, "EXISTING"); !ok || v != "1" {
		t.Fatalf("EXISTING = %q (present=%v), want 1", v, ok)
	}
}

func TestMergeEmptyOverridesKeepsBase(t *testing.T) {
	base := []string{"A=1", "B=2"}
	got := Merge(base, nil)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if v, _ := lookup(got, "A"); v != "1" {
		t.Fatalf("A = %q, want 1", v)
	}
	got[0] = "A=changed"
	if base[0] != "A=1" {
		t.Fatalf("base was mutated: %q", base[0])
	}
}

func TestMergeEmptyBaseFallsBackToProcessEnv(t *testing.T) {
	const key = "HERON_OSENV_TEST"
	t.Setenv(key, "from-process")

	got := Merge(nil, map[string]string{"AUTO": "/x"})
	if v, ok := lookup(got, key); !ok || v != "from-process" {
		t.Fatalf("%s = %q (present=%v), want from-process", key, v, ok)
	}
	if v, ok := lookup(got, "AUTO"); !ok || v != "/x" {
		t.Fatalf("AUTO = %q (present=%v), want /x", v, ok)
	}
}

func TestMergeIgnoresMalformedBaseEntries(t *testing.T) {
	got := Merge([]string{"NOEQUALS", "=novalue", "GOOD=1"}, map[string]string{"AUTO": "/x"})
	if v, ok := lookup(got, "GOOD"); !ok || v != "1" {
		t.Fatalf("GOOD = %q (present=%v), want 1", v, ok)
	}
	for _, entry := range got {
		if strings.HasPrefix(entry, "=") || entry == "NOEQUALS" {
			t.Fatalf("malformed entry leaked: %q", entry)
		}
	}
}
