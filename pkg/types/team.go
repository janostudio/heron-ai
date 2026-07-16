package types

type TeamConfig struct {
	Name   string        `yaml:"name" json:"name"`
	Stages []StageConfig `yaml:"stages" json:"stages"`
}

type StageConfig struct {
	Process string       `yaml:"process" json:"process"` // sequential | parallel
	Tasks   []TaskConfig `yaml:"tasks" json:"tasks"`
}

type TaskConfig struct {
	Name        string   `yaml:"name" json:"name"`
	Agent       string   `yaml:"agent" json:"agent"`
	Description string   `yaml:"description" json:"description"`
	Context     []string `yaml:"context,omitempty" json:"context,omitempty"`
	Tools       []string `yaml:"tools,omitempty" json:"tools,omitempty"`
}
