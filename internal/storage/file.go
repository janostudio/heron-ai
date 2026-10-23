package storage

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// FileStore is the file storage interface
type FileStore interface {
	Read(path string) ([]byte, error)
	Write(path string, data []byte) error
	Append(path string, data []byte) error
	Delete(path string) error
	Exists(path string) bool
	List(dir string) ([]string, error)
}

// Errors
var (
	ErrNotFound = errors.New("file not found")
)

// FileStoreImpl implements FileStore
type FileStoreImpl struct {
	baseDir string
	mu      sync.RWMutex
}

func NewFileStore(baseDir string) *FileStoreImpl {
	return &FileStoreImpl{baseDir: baseDir}
}

func (fs *FileStoreImpl) fullPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(fs.baseDir, path)
}

func (fs *FileStoreImpl) Read(path string) ([]byte, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	fullPath := fs.fullPath(path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

// Write replaces a file atomically.
//
// os.WriteFile truncates first and writes second, so a crash (or a concurrent
// reader) can observe an empty or half-written file. That matters most for the
// documents the engine overwrites in place: state.md is parsed on every read, and
// a torn write made the parse fail permanently — every later state operation
// returned "parse state ..." for that Agent. Temp file + rename makes the swap
// atomic on POSIX, so a reader sees either the old file or the new one.
func (fs *FileStoreImpl) Write(path string, data []byte) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	fullPath := fs.fullPath(path)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return writeFileAtomic(fullPath, data, 0644)
}

// writeFileAtomic writes data to a sibling temp file and renames it over path.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".heron-write-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	// Best effort: after a successful rename this removes nothing.
	defer func() { _ = os.Remove(tempName) }()

	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func (fs *FileStoreImpl) Append(path string, data []byte) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	fullPath := fs.fullPath(path)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	f, err := os.OpenFile(fullPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, err = f.Write(data)
	return err
}

func (fs *FileStoreImpl) Delete(path string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	err := os.Remove(fs.fullPath(path))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (fs *FileStoreImpl) Exists(path string) bool {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	fullPath := fs.fullPath(path)
	_, err := os.Stat(fullPath)
	return err == nil
}

func (fs *FileStoreImpl) List(dir string) ([]string, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	fullPath := fs.fullPath(dir)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}
