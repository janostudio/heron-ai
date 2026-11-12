package skill

import (
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type SkillInjector struct {
	registry *SkillRegistry
}

func NewSkillInjector(registry *SkillRegistry) *SkillInjector {
	return &SkillInjector{registry: registry}
}

// Inject returns the Skill index and the tools each Skill grants.
//
// The index names each Skill, says what it is for, and where its text lives.
// The body is not inlined: a body in the system prompt is paid for on every
// call, including the calls that never use it, and a coding agent can read the
// file when the task matches. This is the same progressive disclosure Anthropic
// Agent Skills and Claude Code use — metadata always, instructions on demand.
//
// A Skill with no known path keeps its body inline: without a path there is
// nowhere for the model to read it from, and dropping the text would silently
// remove the capability instead of deferring it.
func (i *SkillInjector) Inject(skillNames []string) (prompts []string, tools []string) {
	if len(skillNames) == 0 {
		return nil, nil
	}
	index := make([]string, 0, len(skillNames)+1)
	index = append(index, "What exists, where it lives. Read a Skill's file when the task matches it; the body is not inlined here.")
	for _, name := range skillNames {
		skill, err := i.registry.Lookup(name)
		if err != nil {
			continue
		}
		if skill.Body != "" {
			if strings.TrimSpace(skill.Path) == "" {
				prompts = append(prompts, skill.Body)
			} else {
				index = append(index, skillIndexEntry(skill))
			}
		}
		tools = append(tools, skill.Tools...)
		if skill.AllowedTools != "" {
			for _, tool := range strings.Split(skill.AllowedTools, ",") {
				tool = strings.TrimSpace(tool)
				if tool != "" {
					tools = append(tools, tool)
				}
			}
		}
	}
	if len(index) > 1 {
		prompts = append(prompts, strings.Join(index, "\n"))
	}
	return prompts, tools
}

// skillIndexEntry is one line of the index: what the Skill is for and where to
// read it.
func skillIndexEntry(skill types.Skill) string {
	description := strings.TrimSpace(skill.Description)
	if description == "" {
		description = "no description"
	}
	return fmt.Sprintf("- %s — %s\n  read: %s", skill.Name, description, strings.TrimSpace(skill.Path))
}
