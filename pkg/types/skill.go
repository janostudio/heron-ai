package types

// Skill is an optional, self-contained capability package loaded by the Skill
// extension. Scripts are package-relative paths (for example
// "scripts/verify.sh"). The core never executes them implicitly.
type Skill struct {
	Name         string   `yaml:"name" json:"name"`
	Description  string   `yaml:"description" json:"description"`
	Tools        []string `yaml:"tools" json:"tools"`
	AllowedTools string   `yaml:"allowed-tools,omitempty" json:"allowed_tools,omitempty"`
	Knowledge    []string `yaml:"knowledge,omitempty" json:"knowledge,omitempty"`
	// Scripts lists deterministic helpers shipped in the Skill directory.
	// Keep paths relative to the Skill directory so the whole directory can
	// be copied without changing the script itself.
	Scripts []string `yaml:"scripts,omitempty" json:"scripts,omitempty"`
	Prompt  string   `yaml:"-" json:"prompt"`
	Body    string   `yaml:"-" json:"body"`
}

type SkillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
