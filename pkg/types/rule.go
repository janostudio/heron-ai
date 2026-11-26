package types

// RuleItem represents a rule or constraint
type RuleItem struct {
	ID      string `yaml:"id" json:"id"`
	Content string `yaml:"content" json:"content"`
	Scope   Scope  `yaml:"scope" json:"scope"`

	Path string `yaml:"-" json:"-"` // 源文件相对路径，供延迟读取
	// Paths makes the rule conditional: it is injected when the turn touches one
	// of these paths, instead of sitting in the system prompt every turn. The
	// patterns are matched against the paths tools actually operate on (the
	// resolved workspace paths, and the write targets named in a command), not
	// against the user's text. Empty means the rule is always resident.
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
}

// ConditionalRule is one rule whose applicability depends on the paths the turn
// touches, handed to the Agent loop so it can match them where the paths exist:
// at tool execution. The body travels with it because the Agent cannot read the
// rules directory itself — the Flow resolved both scope and file already.
type ConditionalRule struct {
	ID    string
	Paths []string
	Body  string
}
