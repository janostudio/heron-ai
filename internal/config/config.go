package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// webAPIKeyEnv 是 web 工具 API Key 的环境变量回退来源，与具体厂商无关。
const webAPIKeyEnv = "WEB_SEARCH_API_KEY"

// ConfigLoader loads the current global .agents configuration.
type ConfigLoader struct {
	baseDir   string
	fileStore storage.FileStore
}

// settingsCache keeps the parsed-once bytes of .agents/settings.json per base
// directory.
//
// Every Load* method used to read and unmarshal the same file independently —
// seven reads and seven parses for one startup, and one per call site because
// ConfigLoader is constructed per caller. The settings file is read once when the
// runtime is built and is not re-read while the process lives (a changed runtime
// needs a restart either way), so a package-level cache changes no behaviour
// while removing the repeated IO.
var (
	settingsCacheMu sync.Mutex
	settingsCache   = map[string]settingsEntry{}
)

type settingsEntry struct {
	data    []byte
	present bool
}

// settingsBytes returns the settings file contents, reading it at most once per
// base directory. present is false when the file does not exist, which is a
// legal state (every section falls back to its defaults).
func (l *ConfigLoader) settingsBytes() (data []byte, present bool) {
	key := l.baseDir
	settingsCacheMu.Lock()
	defer settingsCacheMu.Unlock()
	if entry, ok := settingsCache[key]; ok {
		return entry.data, entry.present
	}
	raw, err := l.fileStore.Read(filepath.Join(".agents", "settings.json"))
	if err != nil {
		settingsCache[key] = settingsEntry{}
		return nil, false
	}
	settingsCache[key] = settingsEntry{data: raw, present: true}
	return raw, true
}

// InvalidateSettingsCache drops the cached settings so the next Load* re-reads
// them. Used by long-lived hosts that reload configuration in place.
func InvalidateSettingsCache() {
	settingsCacheMu.Lock()
	defer settingsCacheMu.Unlock()
	settingsCache = map[string]settingsEntry{}
}

func (l *ConfigLoader) LoadRuntimeLimits() types.RuntimeLimits {
	limits := types.RuntimeLimits{}.WithDefaults()
	data, present := l.settingsBytes()
	if !present {
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
	data, present := l.settingsBytes()
	if !present {
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

// LoadToolPolicyPhrases 读取 .agents/settings.json 的 tools.restricted_bash_phrases。
// 这些短语会与内置安全底线合并，只增不减；文件缺失时返回 nil。
func (l *ConfigLoader) LoadToolPolicyPhrases() []string {
	data, present := l.settingsBytes()
	if !present {
		return nil
	}
	var raw struct {
		Tools struct {
			RestrictedBashPhrases []string `json:"restricted_bash_phrases"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return raw.Tools.RestrictedBashPhrases
}

// LoadKnowledgeSettings 读取 .agents/settings.json 的 knowledge 段。
// 文件缺失/字段缺失时返回零值 KnowledgeConfig（SummaryModel=""）。
func (l *ConfigLoader) LoadKnowledgeSettings() types.KnowledgeConfig {
	var cfg types.KnowledgeConfig
	data, present := l.settingsBytes()
	if !present {
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

// LoadWebSettings 读取 .agents/settings.json 的 web 段，返回已过默认值填充的
// WebConfig（provider 空 = duckduckgo，endpoint / 超时 / format 都已补齐）。
//
// 文件缺失或 web 段缺失是合法状态：返回零值过 WithDefaults，即默认
// duckduckgo——免 key，零配置可用，不 fail-closed。
//
// APIKey 有两级来源，优先级：web.api_key 明文 > 环境变量 WEB_SEARCH_API_KEY。
// 环境变量名刻意与具体厂商无关——provider 和 key 是配套整段换的，换成 brave
// 时同一个变量装 brave 的 key。这里不读 settings.json 的 env 段：那是注入子
// 进程的语义，由 app 层处理。
func (l *ConfigLoader) LoadWebSettings() types.WebConfig {
	var cfg types.WebConfig
	if data, present := l.settingsBytes(); present {
		var raw struct {
			Web types.WebConfig `json:"web"`
		}
		if json.Unmarshal(data, &raw) == nil {
			cfg = raw.Web
		}
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv(webAPIKeyEnv)
	}
	return cfg.WithDefaults()
}

// LoadGlobalEnv 读取 .agents/settings.json 的 env 段，返回注入所有子进程的
// 全局环境变量。
//
// 值按「路径」解析（对齐 internal/logging 的 dir 语义）：绝对路径原样，相对
// 路径相对 baseDir（配置目录）展开为绝对路径。这样配置里写 `"AUTO": ".."`
// 就得到配置目录的上一级绝对路径（工作区根），子进程无论 cwd 在哪都能命中。
//
// 文件或 env 段缺失是合法状态，返回 nil。
func (l *ConfigLoader) LoadGlobalEnv() map[string]string {
	data, present := l.settingsBytes()
	if !present {
		return nil
	}
	var raw struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || len(raw.Env) == 0 {
		return nil
	}
	resolved := make(map[string]string, len(raw.Env))
	for name, value := range raw.Env {
		if value == "" {
			resolved[name] = value
			continue
		}
		if filepath.IsAbs(value) {
			resolved[name] = filepath.Clean(value)
			continue
		}
		joined := filepath.Join(l.baseDir, value)
		if absolute, absErr := filepath.Abs(joined); absErr == nil {
			resolved[name] = absolute
		} else {
			resolved[name] = filepath.Clean(joined)
		}
	}
	return resolved
}

// LoadMCPServers 读取 .agents/settings.json 的 mcp 段。
//
// 与 runtime / logging / knowledge 三个段不同，这里返回 error：前三者是"缺了
// 就用默认值"，缺了 MCP 配置本身就是默认值（空列表），而 mcp 段**存在但
// JSON 不合法**说明配置文件被写坏了——那和"没配 MCP"是两回事，静默当成
// 没配会让用户以为工具不可用是别的原因。
// 文件不存在（err != nil）不算错误：没有 .agents/settings.json 是合法状态。
func (l *ConfigLoader) LoadMCPServers() ([]types.MCPServerConfig, error) {
	data, present := l.settingsBytes()
	if !present {
		return nil, nil
	}
	var raw struct {
		MCP []types.MCPServerConfig `json:"mcp"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse .agents/settings.json: %w", err)
	}
	return raw.MCP, nil
}

func NewConfigLoader(baseDir string) *ConfigLoader {
	return &ConfigLoader{
		baseDir:   baseDir,
		fileStore: storage.NewFileStore(baseDir),
	}
}
