package types

// RuleItem represents a rule or constraint
type RuleItem struct {
	ID       string `yaml:"id" json:"id"`
	Type     string `yaml:"type" json:"type"` // soft | hard
	Content  string `yaml:"content" json:"content"`
	Scope    Scope  `yaml:"scope" json:"scope"`
	Priority int    `yaml:"priority,omitempty" json:"priority,omitempty"`
}
