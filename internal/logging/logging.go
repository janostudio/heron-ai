// Package logging provides a rotating file logger for global diagnostic
// output across the engine. It writes one JSON object per line, splits files
// by date and sequence number, and enforces a configurable per-file size cap.
package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Level is the severity of a log entry.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "unknown"
	}
}

// parseLevel maps a case-insensitive level name to a Level. An empty string
// or unknown name returns the default level (info).
func parseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	case "info", "":
		return LevelInfo
	default:
		return LevelInfo
	}
}

// parseSize converts a human-readable size string into bytes. It accepts a
// bare integer (bytes) or an integer followed by a case-insensitive unit
// suffix: B, KB, MB, GB. Units are binary (1KB = 1024 bytes). An empty
// string yields 0. Unknown units return an error.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	upper := strings.ToUpper(s)
	units := []struct {
		suffix string
		mult   int64
	}{
		{"GB", 1024 * 1024 * 1024},
		{"MB", 1024 * 1024},
		{"KB", 1024},
		{"B", 1},
	}

	for _, u := range units {
		if strings.HasSuffix(upper, u.suffix) {
			numStr := strings.TrimSpace(s[:len(s)-len(u.suffix)])
			n, err := strconv.ParseInt(numStr, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid size %q: %w", s, err)
			}
			if n < 0 {
				return 0, fmt.Errorf("invalid size %q: negative value", s)
			}
			return n * u.mult, nil
		}
	}

	// No suffix: treat as raw bytes.
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid size %q: negative value", s)
	}
	return n, nil
}

// Config holds the configuration for a RotatingLogger.
type Config struct {
	// Level is the minimum severity to emit (debug/info/warn/error).
	Level string
	// Dir is the log directory, relative to baseDir or absolute.
	Dir string
	// MaxFileSize is a human-readable per-file size cap (e.g. "50MB").
	MaxFileSize string
	// MaxBackups is the maximum number of rotated files to retain per day.
	// 0 means unlimited.
	MaxBackups int
	// RetentionDays is the number of days to keep log files. Files older than
	// this are removed. Defaults to 7. A negative value disables cleanup.
	RetentionDays int
}

// WithDefaults returns cfg with zero-valued fields filled in.
func (c Config) WithDefaults() Config {
	if c.Level == "" {
		c.Level = "info"
	}
	if c.Dir == "" {
		c.Dir = ".agents/data/logs"
	}
	if c.MaxFileSize == "" {
		c.MaxFileSize = "50MB"
	}
	if c.RetentionDays == 0 {
		c.RetentionDays = 7
	}
	return c
}

// Logger is the minimal logging interface exposed to the rest of the engine.
type Logger interface {
	Debug(msg string, fields map[string]any)
	Info(msg string, fields map[string]any)
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
}

// RotatingLogger writes JSON log lines to date-stamped files that rotate by
// sequence number once they exceed a size cap.
type RotatingLogger struct {
	baseDir string
	dir     string
	level   Level
	maxSize int64
	backups int
	retain  int // days to keep log files; <0 disables cleanup

	now func() time.Time

	mu       sync.Mutex
	curDate  string // "2006-01-02" of the current file
	curSeq   int    // current sequence number (0 = no suffix)
	curFile  *os.File
	curSize  int64
	fileName string // base name of the open file, e.g. "2026-09-03.log"
}

// NewRotatingLogger constructs a RotatingLogger rooted at baseDir. cfg is
// normalized via WithDefaults; an invalid level falls back to info, and an
// unparseable MaxFileSize falls back to 50MB.
func NewRotatingLogger(baseDir string, cfg Config) *RotatingLogger {
	cfg = cfg.WithDefaults()

	maxSize, err := parseSize(cfg.MaxFileSize)
	if err != nil || maxSize <= 0 {
		maxSize = 50 * 1024 * 1024
	}

	return &RotatingLogger{
		baseDir: baseDir,
		dir:     cfg.Dir,
		level:   parseLevel(cfg.Level),
		maxSize: maxSize,
		backups: cfg.MaxBackups,
		retain:  cfg.RetentionDays,
		now:     time.Now,
	}
}

// fullDir returns the absolute directory path for log files.
func (l *RotatingLogger) fullDir() string {
	if filepath.IsAbs(l.dir) {
		return l.dir
	}
	return filepath.Join(l.baseDir, l.dir)
}

// fileNameFor builds a file name for the given date and sequence. Seq 0 has
// no suffix; seq >= 1 appends ".N" before the extension.
func fileNameFor(date string, seq int) string {
	if seq == 0 {
		return date + ".log"
	}
	return fmt.Sprintf("%s.%d.log", date, seq)
}

// openFile opens (or reopens) the current file for appending. Caller holds
// the lock. It also closes any previously open file.
func (l *RotatingLogger) openFile() error {
	fullPath := filepath.Join(l.fullDir(), l.fileName)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}

	f, err := os.OpenFile(fullPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}

	if l.curFile != nil {
		_ = l.curFile.Close()
	}
	l.curFile = f
	l.curSize = info.Size()
	return nil
}

// syncDate rolls the file state forward if the current date changed,
// resetting the sequence to 0. Caller holds the lock.
func (l *RotatingLogger) syncDate() error {
	date := l.now().Format("2006-01-02")
	if date == l.curDate {
		return nil
	}
	l.curDate = date
	l.curSeq = 0
	l.fileName = fileNameFor(l.curDate, l.curSeq)
	if err := l.openFile(); err != nil {
		return err
	}
	l.pruneOld()
	return nil
}

// rotate advances to the next sequence number and opens a fresh file,
// then prunes files older than the retention limit. Caller holds the lock.
func (l *RotatingLogger) rotate() error {
	l.curSeq++
	l.fileName = fileNameFor(l.curDate, l.curSeq)
	if err := l.openFile(); err != nil {
		return err
	}
	l.prune()
	return nil
}

// prune removes rotated files (seq >= 1) beyond maxBackups, oldest first.
// Caller holds the lock. It is best-effort: errors are ignored.
func (l *RotatingLogger) prune() {
	if l.backups <= 0 {
		return
	}
	dir := l.fullDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	prefix := l.curDate + "."
	var rotated []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".log") {
			rotated = append(rotated, name)
		}
	}

	if len(rotated) <= l.backups {
		return
	}

	// Sort by sequence number ascending so oldest (smallest) come first.
	sort.Slice(rotated, func(i, j int) bool {
		return seqOf(rotated[i]) < seqOf(rotated[j])
	})

	for _, name := range rotated[:len(rotated)-l.backups] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// seqOf extracts the sequence number from a rotated file name such as
// "2026-09-03.2.log". It returns -1 for unparseable names.
func seqOf(name string) int {
	core := strings.TrimSuffix(name, ".log")
	idx := strings.LastIndex(core, ".")
	if idx < 0 {
		return -1
	}
	n, err := strconv.Atoi(core[idx+1:])
	if err != nil {
		return -1
	}
	return n
}

// pruneOld removes log files whose date stamp is older than the retention
// window. Caller holds the lock. It is best-effort: errors are ignored.
func (l *RotatingLogger) pruneOld() {
	if l.retain < 0 {
		return
	}
	dir := l.fullDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	cutoff := l.now().AddDate(0, 0, -l.retain).Format("2006-01-02")
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		date, ok := dateOf(name)
		if !ok {
			continue
		}
		// Lexicographic comparison works because dates are zero-padded
		// YYYY-MM-DD strings.
		if date < cutoff {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// dateOf extracts the leading "YYYY-MM-DD" from a log file name such as
// "2026-09-03.log" or "2026-09-03.2.log". It reports whether a valid date
// prefix was found.
func dateOf(name string) (string, bool) {
	if len(name) < len("2006-01-02") {
		return "", false
	}
	prefix := name[:len("2006-01-02")]
	if _, err := time.Parse("2006-01-02", prefix); err != nil {
		return "", false
	}
	return prefix, true
}

// write emits one log line at the given level. If the entry would push the
// current file over maxSize, it rotates first. Caller holds the lock.
func (l *RotatingLogger) write(level Level, msg string, fields map[string]any) {
	if level < l.level {
		return
	}
	if err := l.syncDate(); err != nil {
		return
	}

	entry := make(map[string]any, len(fields)+3)
	entry["ts"] = l.now().Format(time.RFC3339)
	entry["level"] = level.String()
	entry["msg"] = msg
	for k, v := range fields {
		entry[k] = v
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')

	if l.curSize+int64(len(data)) > l.maxSize {
		if err := l.rotate(); err != nil {
			return
		}
	}

	n, err := l.curFile.Write(data)
	if err != nil {
		return
	}
	l.curSize += int64(n)
}

// Debug logs at debug level.
func (l *RotatingLogger) Debug(msg string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.write(LevelDebug, msg, fields)
}

// Info logs at info level.
func (l *RotatingLogger) Info(msg string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.write(LevelInfo, msg, fields)
}

// Warn logs at warn level.
func (l *RotatingLogger) Warn(msg string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.write(LevelWarn, msg, fields)
}

// Error logs at error level.
func (l *RotatingLogger) Error(msg string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.write(LevelError, msg, fields)
}

// Close flushes and closes the underlying file.
func (l *RotatingLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.curFile == nil {
		return nil
	}
	err := l.curFile.Close()
	l.curFile = nil
	return err
}

// noopLogger is a silent Logger used before SetDefault is called.
type noopLogger struct{}

func (noopLogger) Debug(string, map[string]any) {}
func (noopLogger) Info(string, map[string]any)  {}
func (noopLogger) Warn(string, map[string]any)  {}
func (noopLogger) Error(string, map[string]any) {}

var defaultLogger Logger = noopLogger{}

// SetDefault replaces the process-wide logger used by the package-level
// Debug/Info/Warn/Error functions.
func SetDefault(l Logger) {
	if l == nil {
		defaultLogger = noopLogger{}
		return
	}
	defaultLogger = l
}

// Debug logs a debug message via the default logger.
func Debug(msg string, fields map[string]any) { defaultLogger.Debug(msg, fields) }

// Info logs an info message via the default logger.
func Info(msg string, fields map[string]any) { defaultLogger.Info(msg, fields) }

// Warn logs a warn message via the default logger.
func Warn(msg string, fields map[string]any) { defaultLogger.Warn(msg, fields) }

// Error logs an error message via the default logger.
func Error(msg string, fields map[string]any) { defaultLogger.Error(msg, fields) }
