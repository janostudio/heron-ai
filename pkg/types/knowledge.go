package types

// KnowledgeEntry represents a knowledge base entry
type KnowledgeEntry struct {
	ID         string   `json:"id"`
	Content    string   `json:"content"`
	Keys       []string `json:"keys"`
	Scope      Scope    `json:"scope"`
	Confidence string   `json:"confidence,omitempty"`
	Source     string   `json:"source,omitempty"`
	RoundNum   int      `json:"round_num,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
}

// Scope defines which agents can see a knowledge entry
type Scope struct {
	Type   string   `json:"type"` // all | team | agents
	Teams  []string `json:"teams,omitempty"`
	Agents []string `json:"agents,omitempty"`
}
