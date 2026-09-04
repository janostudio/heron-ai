package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigWithDefaults(t *testing.T) {
	var c Config
	c = c.WithDefaults()
	if c.Level != "info" {
		t.Errorf("Level = %q, want info", c.Level)
	}
	if c.Dir != ".agents/data/logs" {
		t.Errorf("Dir = %q, want .agents/data/logs", c.Dir)
	}
	if c.MaxFileSize != "50MB" {
		t.Errorf("MaxFileSize = %q, want 50MB", c.MaxFileSize)
	}
	if c.RetentionDays != 7 {
		t.Errorf("RetentionDays = %d, want 7", c.RetentionDays)
	}

	// Existing values are preserved.
	c2 := Config{Level: "debug", Dir: "/tmp/x", MaxFileSize: "1KB", MaxBackups: 3, RetentionDays: 30}
	c2 = c2.WithDefaults()
	if c2.Level != "debug" || c2.Dir != "/tmp/x" || c2.MaxFileSize != "1KB" || c2.MaxBackups != 3 || c2.RetentionDays != 30 {
		t.Errorf("WithDefaults overwrote explicit values: %+v", c2)
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"", 0, false},
		{"500", 500, false},
		{"10B", 10, false},
		{"10b", 10, false},
		{"2KB", 2048, false},
		{"2kb", 2048, false},
		{"5MB", 5 * 1024 * 1024, false},
		{"5mb", 5 * 1024 * 1024, false},
		{"1GB", 1024 * 1024 * 1024, false},
		{"1gb", 1024 * 1024 * 1024, false},
		{"50MB", 50 * 1024 * 1024, false},
		{" 10KB ", 10 * 1024, false},
		{"-5", 0, true},
		{"abc", 0, true},
		{"10XB", 0, true},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if c.err {
			if err == nil {
				t.Errorf("parseSize(%q) expected error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSize(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"debug":   LevelDebug,
		"DEBUG":   LevelDebug,
		"info":    LevelInfo,
		"":        LevelInfo,
		"warn":    LevelWarn,
		"warning": LevelWarn,
		"error":   LevelError,
		"bogus":   LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func newTestLogger(t *testing.T, baseDir string, cfg Config) *RotatingLogger {
	t.Helper()
	l := NewRotatingLogger(baseDir, cfg)
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func readDirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func readAll(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", name, err)
	}
	return string(data)
}

func TestBasicWriteJSONLine(t *testing.T) {
	baseDir := t.TempDir()
	l := newTestLogger(t, baseDir, Config{})
	l.Info("hello world", map[string]any{"user": "alice"})

	logsDir := filepath.Join(baseDir, ".agents", "data", "logs")
	files := readDirFiles(t, logsDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %v", files)
	}

	content := readAll(t, logsDir, files[0])
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %q", len(lines), content)
	}

	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entry["level"] != "info" {
		t.Errorf("level = %v, want info", entry["level"])
	}
	if entry["msg"] != "hello world" {
		t.Errorf("msg = %v, want hello world", entry["msg"])
	}
	if entry["user"] != "alice" {
		t.Errorf("user field = %v, want alice", entry["user"])
	}
	if _, ok := entry["ts"].(string); !ok {
		t.Errorf("ts field missing or not string: %v", entry["ts"])
	}
}

func TestLevelFiltering(t *testing.T) {
	baseDir := t.TempDir()
	l := newTestLogger(t, baseDir, Config{Level: "info"})

	l.Debug("hidden", nil)
	l.Info("shown", nil)
	l.Warn("warn", nil)
	l.Error("err", nil)

	logsDir := filepath.Join(baseDir, ".agents", "data", "logs")
	files := readDirFiles(t, logsDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %v", files)
	}
	content := readAll(t, logsDir, files[0])
	if strings.Contains(content, "hidden") {
		t.Errorf("debug message should be filtered out: %q", content)
	}
	if !strings.Contains(content, "shown") || !strings.Contains(content, "warn") || !strings.Contains(content, "err") {
		t.Errorf("missing expected messages: %q", content)
	}
}

func TestDateAndSequenceRotation(t *testing.T) {
	baseDir := t.TempDir()
	l := newTestLogger(t, baseDir, Config{})
	l.now = func() time.Time {
		return time.Date(2026, 9, 3, 10, 30, 0, 0, time.Local)
	}

	l.Info("day1", nil)

	logsDir := filepath.Join(baseDir, ".agents", "data", "logs")
	if files := readDirFiles(t, logsDir); len(files) != 1 || files[0] != "2026-09-03.log" {
		t.Fatalf("expected [2026-09-03.log], got %v", files)
	}

	// Advance to the next day.
	l.now = func() time.Time {
		return time.Date(2026, 9, 4, 8, 0, 0, 0, time.Local)
	}
	l.Info("day2", nil)

	files := readDirFiles(t, logsDir)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %v", files)
	}
	has := map[string]bool{}
	for _, f := range files {
		has[f] = true
	}
	if !has["2026-09-03.log"] || !has["2026-09-04.log"] {
		t.Errorf("expected both date files, got %v", files)
	}
	if content := readAll(t, logsDir, "2026-09-04.log"); !strings.Contains(content, "day2") {
		t.Errorf("day2 content missing: %q", content)
	}
}

func TestSizeBasedRotation(t *testing.T) {
	baseDir := t.TempDir()
	l := newTestLogger(t, baseDir, Config{MaxFileSize: "100"})
	l.now = func() time.Time {
		return time.Date(2026, 9, 3, 10, 30, 0, 0, time.Local)
	}

	for i := 0; i < 20; i++ {
		l.Info("a message long enough to exceed the cap", map[string]any{"i": i})
	}

	logsDir := filepath.Join(baseDir, ".agents", "data", "logs")
	files := readDirFiles(t, logsDir)
	if len(files) < 2 {
		t.Fatalf("expected rotation into multiple files, got %v", files)
	}

	// The base file (no suffix) must exist.
	hasBase := false
	for _, f := range files {
		if f == "2026-09-03.log" {
			hasBase = true
		}
	}
	if !hasBase {
		t.Errorf("base file 2026-09-03.log missing, got %v", files)
	}

	// A single entry is ~104 bytes, larger than maxSize 100, so the only
	// files that may exceed maxSize are those holding exactly one entry.
	for _, f := range files {
		info, err := os.Stat(filepath.Join(logsDir, f))
		if err != nil {
			t.Fatalf("stat %s: %v", f, err)
		}
		if info.Size() > 200 {
			t.Errorf("file %s size %d unreasonably large", f, info.Size())
		}
	}
}

func TestMaxBackupsPruning(t *testing.T) {
	baseDir := t.TempDir()
	l := newTestLogger(t, baseDir, Config{MaxFileSize: "50", MaxBackups: 2})
	l.now = func() time.Time {
		return time.Date(2026, 9, 3, 10, 30, 0, 0, time.Local)
	}

	for i := 0; i < 50; i++ {
		l.Info("x", nil)
	}

	logsDir := filepath.Join(baseDir, ".agents", "data", "logs")
	files := readDirFiles(t, logsDir)
	// base file + at most MaxBackups rotated files.
	if len(files) > 3 {
		t.Fatalf("expected at most 3 files (base + 2 backups), got %d: %v", len(files), files)
	}
}

func TestGlobalSingleton(t *testing.T) {
	// Reset to noop after test.
	prev := defaultLogger
	defer func() { defaultLogger = prev }()

	baseDir := t.TempDir()
	l := NewRotatingLogger(baseDir, Config{})
	defer l.Close()

	SetDefault(l)
	Info("global info", map[string]any{"k": "v"})
	Debug("global debug", nil) // default level info -> filtered

	logsDir := filepath.Join(baseDir, ".agents", "data", "logs")
	files := readDirFiles(t, logsDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %v", files)
	}
	content := readAll(t, logsDir, files[0])
	if !strings.Contains(content, "global info") {
		t.Errorf("missing global info: %q", content)
	}
	if strings.Contains(content, "global debug") {
		t.Errorf("debug should be filtered at default level: %q", content)
	}
}

func TestRetentionPruning(t *testing.T) {
	baseDir := t.TempDir()
	l := newTestLogger(t, baseDir, Config{RetentionDays: 7})

	// Write a log entry dated today.
	l.now = func() time.Time {
		return time.Date(2026, 9, 10, 10, 0, 0, 0, time.Local)
	}
	l.Info("recent", nil)

	logsDir := filepath.Join(baseDir, ".agents", "data", "logs")

	// Plant a stale log file dated 10 days ago.
	stale := filepath.Join(logsDir, "2026-08-31.log")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(stale, []byte("stale\n"), 0644); err != nil {
		t.Fatalf("WriteFile stale: %v", err)
	}

	// Trigger a date roll to the next day; syncDate should prune the stale
	// file older than 7 days.
	l.now = func() time.Time {
		return time.Date(2026, 9, 11, 8, 0, 0, 0, time.Local)
	}
	l.Info("newday", nil)

	files := readDirFiles(t, logsDir)
	for _, f := range files {
		if f == "2026-08-31.log" {
			t.Errorf("stale file should have been pruned, still present: %v", files)
		}
	}
	// The recent file (2026-09-10) is within 7 days and must remain.
	foundRecent := false
	for _, f := range files {
		if f == "2026-09-10.log" {
			foundRecent = true
		}
	}
	if !foundRecent {
		t.Errorf("recent file 2026-09-10.log missing: %v", files)
	}
}

func TestRetentionDisabled(t *testing.T) {
	baseDir := t.TempDir()
	l := newTestLogger(t, baseDir, Config{RetentionDays: -1})
	l.now = func() time.Time {
		return time.Date(2026, 9, 10, 10, 0, 0, 0, time.Local)
	}
	l.Info("recent", nil)

	logsDir := filepath.Join(baseDir, ".agents", "data", "logs")
	stale := filepath.Join(logsDir, "2026-08-31.log")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(stale, []byte("stale\n"), 0644); err != nil {
		t.Fatalf("WriteFile stale: %v", err)
	}

	l.now = func() time.Time {
		return time.Date(2026, 9, 11, 8, 0, 0, 0, time.Local)
	}
	l.Info("newday", nil)

	// RetentionDays -1 means no pruning; stale file must remain.
	files := readDirFiles(t, logsDir)
	foundStale := false
	for _, f := range files {
		if f == "2026-08-31.log" {
			foundStale = true
		}
	}
	if !foundStale {
		t.Errorf("stale file should remain when retention disabled: %v", files)
	}
}

func TestDateOf(t *testing.T) {
	cases := []struct {
		name string
		date string
		ok   bool
	}{
		{"2026-09-03.log", "2026-09-03", true},
		{"2026-09-03.2.log", "2026-09-03", true},
		{"notadate.log", "", false},
		{"", "", false},
		{"2026-13-99.log", "", false},
	}
	for _, c := range cases {
		date, ok := dateOf(c.name)
		if ok != c.ok || (ok && date != c.date) {
			t.Errorf("dateOf(%q) = (%q, %v), want (%q, %v)", c.name, date, ok, c.date, c.ok)
		}
	}
}

func TestSetDefaultNil(t *testing.T) {
	prev := defaultLogger
	defer func() { defaultLogger = prev }()

	SetDefault(nil)
	// Should not panic.
	Info("x", nil)
	Debug("y", nil)
}
