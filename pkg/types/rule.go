package types

// RuleItem represents a rule or constraint
type RuleItem struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // soft | hard
	Content  string `json:"content"`
	Scope    Scope  `json:"scope"`
	Priority int    `json:"priority,omitempty"`
}
