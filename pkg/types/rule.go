package types

// RuleItem represents a rule or constraint
type RuleItem struct {
	ID       string `yaml:"id" json:"id"`
	Type     string `yaml:"type" json:"type"` // soft | hard
	Content  string `yaml:"content" json:"content"`
	Scope    Scope  `yaml:"scope" json:"scope"`
	Priority int    `yaml:"priority,omitempty" json:"priority,omitempty"`

	Path  string   `yaml:"-" json:"-"`                             // 源文件相对路径，供延迟读取
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"` // 条件触发 glob；空=常驻
}
