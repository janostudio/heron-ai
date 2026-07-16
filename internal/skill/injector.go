package skill

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
	}
	return prompts, tools
}
