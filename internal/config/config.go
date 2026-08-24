package config

import "github.com/heron-ai/heron-engine/internal/storage"

// ConfigLoader loads the current global .agents configuration.
type ConfigLoader struct {
	baseDir   string
	fileStore storage.FileStore
}

func NewConfigLoader(baseDir string) *ConfigLoader {
	return &ConfigLoader{
		baseDir:   baseDir,
		fileStore: storage.NewFileStore(baseDir),
	}
}
