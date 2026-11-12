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
	// Path is where the Skill's SKILL.md lives, relative to the workspace. The
	// system prompt carries this path and the description instead of the body:
	// a Skill whose instructions are not needed costs one index line per call
	// rather than its whole text, and the model reads the file when the task
	// matches it.
	Path string `yaml:"-" json:"path,omitempty"`
}

type SkillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
