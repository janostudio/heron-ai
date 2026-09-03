package types

// Definitions is the normalized global current configuration used by the
// Flow/Team/Call runtime.
type Definitions struct {
	Flow   Flow
	Teams  map[string]Team
	Agents map[string]AgentConfig
	Skills    map[string]Skill
	Rules     map[string]RuleItem
	Limits    RuntimeLimits
	Knowledge KnowledgeConfig
}
