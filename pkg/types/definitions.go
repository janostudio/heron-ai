package types

// Definitions is the normalized global current configuration used by the
// Flow/Team/Call runtime.
type Definitions struct {
	Flow      Flow
	Teams     map[string]Team
	Agents    map[string]AgentConfig
	Skills    map[string]Skill
	Rules     map[string]RuleItem
	Limits    RuntimeLimits
	Knowledge KnowledgeConfig
	Workspace *WorkspaceConfig
	// Env 是解析后的全局环境变量（settings.json 的 env 段，相对路径已按配置
	// 目录展开为绝对路径）。运行时把它注入所有子进程。
	Env map[string]string
}
