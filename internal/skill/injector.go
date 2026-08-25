package skill

import "strings"

type SkillInjector struct {
	registry *SkillRegistry
}

func NewSkillInjector(registry *SkillRegistry) *SkillInjector {
	return &SkillInjector{registry: registry}
}

func (i *SkillInjector) Inject(skillNames []string) (prompts []string, tools []string) {
	for _, name := range skillNames {
		skill, err := i.registry.Lookup(name)
		if err != nil {
			continue
		}
		if skill.Body != "" {
			prompts = append(prompts, skill.Body)
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
	return prompts, tools
}
