package teamspkg

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIVersionV1 is the only manifest API version this engine understands. A
// different value means the package was written for a newer engine, and
// guessing at its meaning would install something that silently misbehaves.
const APIVersionV1 = "heron/v1"

// KindTeam is the only supported manifest kind: packages distribute Teams, and
// an Agent is only shareable as part of one.
const KindTeam = "team"

// Script policies for Sandbox.Scripts.
const (
	ScriptsAllow  = "allow"
	ScriptsDeny   = "deny"
	ScriptsReview = "review"
)

// Manifest is the contents of heron-team.yml.
//
// It declares identity, provided assets and external requirements. It
// deliberately carries no runtime environment: models, keys and endpoints
// belong to the receiving side.
type Manifest struct {
	APIVersion  string   `yaml:"apiVersion"`
	Kind        string   `yaml:"kind"`
	Name        string   `yaml:"name"`
	Version     string   `yaml:"version"`
	Description string   `yaml:"description,omitempty"`
	Authors     []string `yaml:"authors,omitempty"`
	License     string   `yaml:"license,omitempty"`
	Provides    Provides `yaml:"provides"`
	Requires    Requires `yaml:"requires,omitempty"`
	Sandbox     Sandbox  `yaml:"sandbox,omitempty"`
}

// Provides lists the assets shipped in the package. Every name must exist in
// the asset tree; that is what makes the package self-contained.
type Provides struct {
	Teams  []string `yaml:"teams"`
	Agents []string `yaml:"agents,omitempty"`
	Skills []string `yaml:"skills,omitempty"`
	Rules  []string `yaml:"rules,omitempty"`
}

// Requires lists assets the receiving side must supply. Version ranges are
// matched exactly for now (no solver), so the field is only echoed back.
type Requires struct {
	Skills []SkillRequirement `yaml:"skills,omitempty"`
}

// SkillRequirement names an external Skill.
type SkillRequirement struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version,omitempty"`
}

// Sandbox bounds what the package's Agents may do on the receiving side.
type Sandbox struct {
	// Tools is the allow-list applied to every Agent in the package. Empty
	// means no restriction, which is why Manifest.Validate rejects an empty
	// list on a package that ships Agents (see check.go).
	Tools   []string `yaml:"tools,omitempty"`
	Scripts string   `yaml:"scripts,omitempty"`
}

// namePattern is the install-scope grammar: the name becomes a directory
// under _teams/, so anything a filesystem or shell could reinterpret is out.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidName reports whether name is legal for a package name or install scope.
//
// It is exported because --as lets the CLI choose a scope that never appears in
// a manifest, and both end up as a directory name under _teams/.
func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

// LoadManifest parses and validates a heron-team.yml.
func LoadManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	if m.Sandbox.Scripts == "" {
		m.Sandbox.Scripts = ScriptsReview
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	if strings.TrimSpace(m.APIVersion) == "" {
		return fmt.Errorf("manifest: apiVersion is required (expected %q)", APIVersionV1)
	}
	if m.APIVersion != APIVersionV1 {
		return fmt.Errorf("manifest: unsupported apiVersion %q (expected %q)", m.APIVersion, APIVersionV1)
	}
	if strings.TrimSpace(m.Kind) == "" {
		return fmt.Errorf("manifest: kind is required (expected %q)", KindTeam)
	}
	if m.Kind != KindTeam {
		return fmt.Errorf("manifest: unsupported kind %q (expected %q)", m.Kind, KindTeam)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("manifest: name is required")
	}
	if !namePattern.MatchString(m.Name) {
		return fmt.Errorf("manifest: name %q must match %s", m.Name, namePattern.String())
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("manifest %q: version is required", m.Name)
	}
	switch m.Sandbox.Scripts {
	case "", ScriptsAllow, ScriptsDeny, ScriptsReview:
	default:
		return fmt.Errorf(
			"manifest %q: sandbox.scripts %q is not supported (allow|deny|review)",
			m.Name, m.Sandbox.Scripts,
		)
	}
	return nil
}
