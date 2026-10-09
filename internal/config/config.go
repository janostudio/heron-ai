package config

import (
	"encoding/json"
	"fmt"
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

// LoadLoggingSettings 读取 .agents/settings.json 的 logging 段。
// 文件缺失/字段缺失时返回零值 LoggingConfig（各字段为空字符串/0）。
func (l *ConfigLoader) LoadLoggingSettings() types.LoggingConfig {
	var cfg types.LoggingConfig
	path := filepath.Join(".agents", "settings.json")
	data, err := l.fileStore.Read(path)
	if err != nil {
		return cfg
	}
	var raw struct {
		Logging types.LoggingConfig `json:"logging"`
	}
	if err := json.Unmarshal(data, &raw); err == nil {
		cfg = raw.Logging
	}
	return cfg
}

// LoadKnowledgeSettings 读取 .agents/settings.json 的 knowledge 段。
// 文件缺失/字段缺失时返回零值 KnowledgeConfig（SummaryModel=""）。
func (l *ConfigLoader) LoadKnowledgeSettings() types.KnowledgeConfig {
	var cfg types.KnowledgeConfig
	path := filepath.Join(".agents", "settings.json")
	data, err := l.fileStore.Read(path)
	if err != nil {
		return cfg
	}
	var raw struct {
		Knowledge types.KnowledgeConfig `json:"knowledge"`
	}
	if err := json.Unmarshal(data, &raw); err == nil {
		cfg = raw.Knowledge
	}
	return cfg
}

// LoadMCPServers 读取 .agents/settings.json 的 mcp 段。
//
// 与 runtime / logging / knowledge 三个段不同，这里返回 error：前三者是"缺了
// 就用默认值"，缺了 MCP 配置本身就是默认值（空列表），而 mcp 段**存在但
// JSON 不合法**说明配置文件被写坏了——那和"没配 MCP"是两回事，静默当成
// 没配会让用户以为工具不可用是别的原因。
// 文件不存在（err != nil）不算错误：没有 .agents/settings.json 是合法状态。
func (l *ConfigLoader) LoadMCPServers() ([]types.MCPServerConfig, error) {
	path := filepath.Join(".agents", "settings.json")
	data, err := l.fileStore.Read(path)
	if err != nil {
		return nil, nil
	}
	var raw struct {
		MCP []types.MCPServerConfig `json:"mcp"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return raw.MCP, nil
}

func NewConfigLoader(baseDir string) *ConfigLoader {
	return &ConfigLoader{
		baseDir:   baseDir,
		fileStore: storage.NewFileStore(baseDir),
	}
}
