// Package definitions exposes the built-in definition templates plus, in later
// batches, the machinery that turns them into on-disk Agent/Team config files.
//
// Templates are compiled into the binary with go:embed on purpose: a template
// describes the shape of a valid definition, not the user's environment. If we
// read them from .agents/ instead, a user could break instantiation by editing
// or deleting a file the engine depends on, and a fresh tree would have no
// templates at all.
package definitions

import (
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/pkg/types"
)

//go:embed templates
var templateFS embed.FS

// Template kinds. Callers branch on Kind to decide which config type the
// template decodes into and where the instantiated file belongs.
const (
	KindAgent = "agent"
	KindTeam  = "team"
)

// templateIndex maps the public template name to the embedded filename. The
// public name is what a model writes in a spec, so it must stay stable even if
// the embedded layout changes.
var templateIndex = map[string]string{
	"agent.generic":   "templates/agent.generic.md",
	"team.sequential": "templates/team.sequential.yml",
	"team.fanout":     "templates/team.fanout.yml",
}

// Template is a parsed built-in definition template.
//
// A template carries three things the writer needs: the raw bytes (so the
// instantiated file keeps any comments, which humans edit later), the parsed
// frontmatter or document as a generic map (so a model-supplied spec can be
// merged on top without re-encoding the whole document), and the body for Agent
// templates. Raw is never mutated; merging produces a new document.
//
// Kind is "agent" or "team". For agent templates Frontmatter holds the decoded
// YAML frontmatter and Body the markdown after it; for team templates
// Document holds the whole YAML file and Body is empty.
type Template struct {
	Name        string
	Kind        string
	Raw         []byte
	Frontmatter map[string]any
	Document    map[string]any
	Body        string
}

// TemplateNames returns the public names of all built-in templates, sorted so
// error messages and tool descriptions are deterministic.
func TemplateNames() []string {
	names := make([]string, 0, len(templateIndex))
	for name := range templateIndex {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LoadTemplate reads and decodes a built-in template by its public name.
func LoadTemplate(name string) (Template, error) {
	path, ok := templateIndex[name]
	if !ok {
		return Template{}, fmt.Errorf(
			"unknown template %q: valid templates are %s",
			name,
			strings.Join(TemplateNames(), ", "),
		)
	}

	raw, err := templateFS.ReadFile(path)
	if err != nil {
		// Unreachable unless the embedded file was removed without updating
		// templateIndex, which would be a build-time mistake.
		return Template{}, fmt.Errorf("read embedded template %q: %w", name, err)
	}

	kind := KindTeam
	if strings.HasSuffix(path, ".md") {
		kind = KindAgent
	}

	template := Template{Name: name, Kind: kind, Raw: raw}
	switch kind {
	case KindAgent:
		// Decode exactly like the config loader so a template and a real
		// AGENT.md file are validated against the same structure.
		if err := decodeAgentTemplate(raw, &template); err != nil {
			return Template{}, fmt.Errorf("template %q: %w", name, err)
		}
	default:
		if err := decodeTeamTemplate(raw, &template); err != nil {
			return Template{}, fmt.Errorf("template %q: %w", name, err)
		}
	}

	return template, nil
}

func decodeAgentTemplate(raw []byte, template *Template) error {
	// frontmatter.Parse yields the raw frontmatter separately so we can keep it
	// as a map, but decoding into types.AgentConfig is what makes the template
	// byte-compatible with a real Agent definition.
	var probe types.AgentConfig
	body, err := frontmatter.Parse(strings.NewReader(string(raw)), &probe)
	if err != nil {
		return err
	}

	frontmatterText, err := rawFrontmatter(string(raw))
	if err != nil {
		return err
	}
	decoded := map[string]any{}
	if err := yaml.Unmarshal([]byte(frontmatterText), &decoded); err != nil {
		return fmt.Errorf("decode frontmatter: %w", err)
	}

	template.Frontmatter = decoded
	template.Body = string(body)
	return nil
}

func decodeTeamTemplate(raw []byte, template *Template) error {
	document := map[string]any{}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if len(document) == 0 {
		return errors.New("decode: template is empty")
	}
	template.Document = document
	return nil
}

// rawFrontmatter extracts the YAML between the leading "---" delimiters.
//
// frontmatter.Parse consumes the delimiters and gives back only the body, so
// the map form has to be re-read from the source. The map is only needed for
// merging, never for validation, which is why re-parsing is cheaper than
// re-encoding types.AgentConfig.
func rawFrontmatter(text string) (string, error) {
	const delimiter = "---"

	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(normalized, delimiter) {
		return "", errors.New("frontmatter: template does not start with ---")
	}

	rest := normalized[len(delimiter):]
	end := strings.Index(rest, "\n"+delimiter)
	if end < 0 {
		return "", errors.New("frontmatter: closing --- not found")
	}
	return rest[:end], nil
}
