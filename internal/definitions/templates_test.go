package definitions

import (
	"strings"
	"testing"

	"github.com/adrg/frontmatter"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// validBuiltinTools mirrors the tools the engine actually registers. It is
// hardcoded rather than derived from internal/tool so that adding a tool to the
// registry does not silently bless it inside a template.
var validBuiltinTools = map[string]struct{}{
	"Read": {}, "Write": {}, "Bash": {}, "Grep": {}, "Glob": {},
	"WebSearch": {}, "WebFetch": {}, "CodeNav": {}, "AskUserQuestion": {},
	"TodoWrite": {}, "TodoRead": {}, "Spawn": {}, "State": {}, "Collect": {},
}

func TestEmbeddedTemplatesParse(t *testing.T) {
	names := TemplateNames()
	require.NotEmpty(t, names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			template, err := LoadTemplate(name)
			require.NoError(t, err)
			require.Equal(t, name, template.Name)
			require.NotEmpty(t, template.Raw)

			switch template.Kind {
			case KindAgent:
				require.NotEmpty(t, template.Frontmatter, "agent template needs frontmatter")
				require.NotEmpty(t, strings.TrimSpace(template.Body), "agent template needs a body")
			case KindTeam:
				require.NotEmpty(t, template.Document, "team template needs a document")
				require.Empty(t, template.Body)
			default:
				t.Fatalf("unexpected template kind %q", template.Kind)
			}
		})
	}
}

func TestAgentTemplateIsValidConfig(t *testing.T) {
	template, err := LoadTemplate("agent.generic")
	require.NoError(t, err)
	require.Equal(t, KindAgent, template.Kind)

	// Decode the same way the config loader does, proving the template would be
	// accepted as a real .agents/agents/<name>/AGENT.md.
	var agent types.AgentConfig
	body, err := frontmatter.Parse(strings.NewReader(string(template.Raw)), &agent)
	require.NoError(t, err)
	agent.Body = string(body)

	require.NotEmpty(t, strings.TrimSpace(agent.Name))
	require.NotEmpty(t, strings.TrimSpace(agent.Persona.Role))
	require.NotEmpty(t, strings.TrimSpace(agent.Persona.Goal))
	require.NotEmpty(t, strings.TrimSpace(agent.Body))
	require.Positive(t, agent.Loop.MaxRounds)
	require.NotEmpty(t, strings.TrimSpace(agent.Loop.Timeout))
	require.NotNil(t, agent.HITL)

	require.NotEmpty(t, agent.Tools.Builtin)
	for _, name := range agent.Tools.Builtin {
		_, ok := validBuiltinTools[name]
		require.Truef(t, ok, "template references unknown builtin tool %q", name)
	}

	// Skills/knowledge/rules point at user-specific definitions that do not
	// exist in a fresh tree, so the generic template must not reference any.
	require.Empty(t, agent.Skills)
	require.Empty(t, agent.Knowledge)
	require.Empty(t, agent.Rules)
}

func TestTeamTemplatesValidate(t *testing.T) {
	for _, name := range []string{"team.sequential", "team.fanout"} {
		t.Run(name, func(t *testing.T) {
			template, err := LoadTemplate(name)
			require.NoError(t, err)
			require.Equal(t, KindTeam, template.Kind)

			var team types.Team
			require.NoError(t, yaml.Unmarshal(template.Raw, &team))
			team.Normalize()
			require.NoError(t, team.Validate())

			require.NotEmpty(t, team.Calls)
			for key, call := range team.Calls {
				require.Equalf(t, key, call.ID, "call key %q must equal its id", key)
				require.NotEmpty(t, strings.TrimSpace(call.AgentID))
			}
		})
	}
}

func TestLoadTemplateUnknownName(t *testing.T) {
	_, err := LoadTemplate("agent.does-not-exist")
	require.Error(t, err)

	message := err.Error()
	require.Contains(t, message, "agent.does-not-exist")
	for _, name := range TemplateNames() {
		require.Containsf(t, message, name, "error should list %q", name)
	}
}
