package types

// Skill is an optional prompt/tool bundle loaded by the Skill extension.
type Skill struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description"`
	Tools       []string `yaml:"tools" json:"tools"`
	Knowledge   []string `yaml:"knowledge,omitempty" json:"knowledge,omitempty"`
	Prompt      string   `yaml:"-" json:"prompt"`
	Body        string   `yaml:"-" json:"body"`
}

type SkillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
