package types

// AgentState represents the structured state of an agent (state.json)
type AgentState struct {
	TeamName  string         `json:"team_name"`
	AgentName string         `json:"agent_name"`
	Data      map[string]any `json:"data"`
	UpdatedAt string         `json:"updated_at"`
}

// MemoryEntry represents a single memory entry (memory.jsonl)
type MemoryEntry struct {
	Content    string `json:"content"`
	Importance string `json:"importance"` // low | medium | high | critical
	Source     string `json:"source"`
	Round      int    `json:"round"`
	Timestamp  string `json:"timestamp"`
}

// TeamMemoryEntry represents a team-level memory entry
type TeamMemoryEntry struct {
	Content    string `json:"content"`
	Importance string `json:"importance"`
	AgentName  string `json:"agent_name"`
	Round      int    `json:"round"`
	Timestamp  string `json:"timestamp"`
}
