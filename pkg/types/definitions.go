package types

// Definitions is the normalized global current configuration used by the
// Flow/Team/Member runtime.
type Definitions struct {
	Flow   Flow
	Teams  map[string]Team
	Agents map[string]AgentConfig
}
