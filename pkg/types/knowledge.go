package types

// KnowledgeEntry represents a knowledge base entry
type KnowledgeEntry struct {
	ID         string     `yaml:"id" json:"id"`
	Title      string     `yaml:"title,omitempty" json:"title,omitempty"`
	Summary    string     `yaml:"summary,omitempty" json:"summary,omitempty"`
	Content    string     `yaml:"content,omitempty" json:"content"`
	Keys       []string   `yaml:"keys,omitempty" json:"keys"`
	Scope      Scope      `yaml:"scope" json:"scope"`
	Status     string     `yaml:"status,omitempty" json:"status,omitempty"` // active | proposed | deprecated
	Path       string     `yaml:"path,omitempty" json:"path,omitempty"`
	Basis      []BasisRef `yaml:"basis,omitempty" json:"basis,omitempty"`
	Version    int        `yaml:"version,omitempty" json:"version,omitempty"`
	Confidence string     `yaml:"confidence,omitempty" json:"confidence,omitempty"`
	Source     string     `yaml:"source,omitempty" json:"source,omitempty"`
	RoundNum   int        `yaml:"round_num,omitempty" json:"round_num,omitempty"`
	CreatedAt  string     `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	ExpiresAt  string     `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
}

// Scope defines which agents can see a knowledge entry
type Scope struct {
	Type   string   `yaml:"type" json:"type"` // all | team | agents
	Teams  []string `yaml:"teams,omitempty" json:"teams,omitempty"`
	Agents []string `yaml:"agents,omitempty" json:"agents,omitempty"`
}
