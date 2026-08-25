package config

import (
	"encoding/json"
	"path/filepath"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ConfigLoader loads the current global .agents configuration.
type ConfigLoader struct {
	baseDir   string
	fileStore storage.FileStore
}

func (l *ConfigLoader) LoadRuntimeLimits() types.RuntimeLimits {
	limits := types.RuntimeLimits{}.WithDefaults()
	path := filepath.Join(".agents", "settings.json")
	data, err := l.fileStore.Read(path)
	if err != nil {
		return limits
	}
	var raw struct {
		Runtime types.RuntimeLimits `json:"runtime"`
	}
	if err := json.Unmarshal(data, &raw); err == nil {
		limits = raw.Runtime.WithDefaults()
	}
	return limits
}

func NewConfigLoader(baseDir string) *ConfigLoader {
	return &ConfigLoader{
		baseDir:   baseDir,
		fileStore: storage.NewFileStore(baseDir),
	}
}
