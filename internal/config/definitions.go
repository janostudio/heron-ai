package config

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// DefinitionsLoadRequest describes loading the new Flow/Team/Call
// configuration model. It intentionally has no per-session override: the
// loader reads the current .agents/ configuration.
type DefinitionsLoadRequest struct {
	FlowPath string
}

// LoadDefinitions loads the new configuration model without changing the
// legacy RunRequest loader. This gives the new runtime a clean boundary while
// old Stage/Task examples are still being migrated.
func (l *ConfigLoader) LoadDefinitions(ctx context.Context, req DefinitionsLoadRequest) (*types.Definitions, error) {
	if strings.TrimSpace(req.FlowPath) == "" {
		return nil, fmt.Errorf("flow path is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	configRoot := l.configRootForFlow(req.FlowPath)

	flow, err := l.loadDefinitionFlow(req.FlowPath)
	if err != nil {
		return nil, fmt.Errorf("load flow: %w", err)
	}
	flow.Normalize()

	teams, err := l.loadDefinitionTeams(filepath.Join(configRoot, "teams"))
	if err != nil {
		return nil, fmt.Errorf("load teams: %w", err)
	}

	agents, err := l.loadAgentDefinitions(filepath.Join(configRoot, "agents"))
	if err != nil {
		return nil, fmt.Errorf("load agents: %w", err)
	}

	skills, err := l.loadSkillDefinitions(filepath.Join(configRoot, "skills"))
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}

	rules, err := l.loadRuleDefinitions(filepath.Join(configRoot, "rules"))
	if err != nil {
		return nil, fmt.Errorf("load rules: %w", err)
	}
	agentRules, err := l.loadAgentRuleDefinitions(filepath.Join(configRoot, "agents"))
	if err != nil {
		return nil, fmt.Errorf("load agent rules: %w", err)
	}
	for name, rule := range agentRules {
		if _, exists := rules[name]; exists {
			return nil, fmt.Errorf("duplicate rule definition %q", name)
		}
		rules[name] = rule
	}

	if err := flow.ValidateWithTeams(teams); err != nil {
		return nil, fmt.Errorf("validate definitions: %w", err)
	}
	if err := validateAgentCallDefinitions(flow, teams, agents); err != nil {
		return nil, fmt.Errorf("validate agents: %w", err)
	}
	if err := validateAgentSupportDefinitions(flow, teams, agents, skills, rules); err != nil {
		return nil, fmt.Errorf("validate agent support: %w", err)
	}

	return &types.Definitions{
		Flow:   flow,
		Teams:  teams,
		Agents: agents,
		Skills: skills,
		Rules:     rules,
		Limits:    l.LoadRuntimeLimits(),
		Knowledge: l.LoadKnowledgeSettings(),
	}, nil
}

func (l *ConfigLoader) configRootForFlow(flowPath string) string {
	configRoot := filepath.Dir(flowPath)
	if filepath.Base(configRoot) != "flows" {
		return configRoot
	}

	parent := filepath.Dir(configRoot)
	if l.fileStore.Exists(filepath.Join(parent, "teams")) ||
		l.fileStore.Exists(filepath.Join(parent, "agents")) {
		return parent
	}
	return configRoot
}

func (l *ConfigLoader) loadDefinitionFlow(path string) (types.Flow, error) {
	data, err := l.fileStore.Read(path)
	if err != nil {
		return types.Flow{}, err
	}

	var flow types.Flow
	if err := yaml.Unmarshal(data, &flow); err != nil {
		return types.Flow{}, err
	}
	return flow, nil
}

func (l *ConfigLoader) loadDefinitionTeams(dir string) (map[string]types.Team, error) {
	teams := make(map[string]types.Team)
	if !l.fileStore.Exists(dir) {
		return teams, nil
	}

	files, err := l.fileStore.List(dir)
	if err != nil {
		return nil, err
	}
	for _, name := range files {
		ext := filepath.Ext(name)
		if ext != ".yml" && ext != ".yaml" {
			continue
		}

		data, err := l.fileStore.Read(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}

		var team types.Team
		if err := yaml.Unmarshal(data, &team); err != nil {
			return nil, fmt.Errorf("decode %s: %w", name, err)
		}
		team.Normalize()
		if strings.TrimSpace(team.ID) == "" {
			team.ID = strings.TrimSuffix(name, ext)
		}
		if _, exists := teams[team.ID]; exists {
			return nil, fmt.Errorf("duplicate team definition %q", team.ID)
		}
		teams[team.ID] = team
	}

	return teams, nil
}

func (l *ConfigLoader) loadAgentDefinitions(dir string) (map[string]types.AgentConfig, error) {
	agents := make(map[string]types.AgentConfig)
	if !l.fileStore.Exists(dir) {
		return agents, nil
	}

	files, err := l.fileStore.List(dir)
	if err != nil {
		return nil, err
	}

	for _, name := range files {
		path := filepath.Join(dir, name)
		// Canonical layout:
		//
		//   .agents/agents/<agent-id>/AGENT.md
		//
		// Flat <agent-id>.md files remain readable for migration
		// compatibility, but new Agent definitions must use the directory
		// form so private knowledge, rules and extensions have one stable
		// home.
		if filepath.Ext(name) == ".md" {
			agent, err := l.loadAgent(path)
			if err != nil {
				return nil, fmt.Errorf("decode %s: %w", name, err)
			}
			if err := addAgentDefinition(agents, *agent); err != nil {
				return nil, err
			}
			continue
		}

		if filepath.Ext(name) != "" {
			continue
		}
		agentPath := filepath.Join(path, "AGENT.md")
		if !l.fileStore.Exists(agentPath) {
			continue
		}
		agent, err := l.loadAgent(agentPath)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", name, err)
		}
		if err := addAgentDefinition(agents, *agent); err != nil {
			return nil, err
		}
	}

	return agents, nil
}

func addAgentDefinition(agents map[string]types.AgentConfig, agent types.AgentConfig) error {
	if strings.TrimSpace(agent.Name) == "" {
		return fmt.Errorf("agent definition name is required")
	}
	if _, exists := agents[agent.Name]; exists {
		return fmt.Errorf("duplicate agent definition %q", agent.Name)
	}
	agents[agent.Name] = agent
	return nil
}

func (l *ConfigLoader) loadSkillDefinitions(dir string) (map[string]types.Skill, error) {
	skills := make(map[string]types.Skill)
	if !l.fileStore.Exists(dir) {
		return skills, nil
	}

	files, err := l.fileStore.List(dir)
	if err != nil {
		return nil, err
	}
	for _, name := range files {
		if filepath.Ext(name) != "" {
			continue
		}
		path := filepath.Join(dir, name, "SKILL.md")
		if !l.fileStore.Exists(path) {
			continue
		}
		skill, err := l.loadSkill(path)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", name, err)
		}
		if strings.TrimSpace(skill.Name) == "" {
			skill.Name = name
		}
		if _, exists := skills[skill.Name]; exists {
			return nil, fmt.Errorf("duplicate skill definition %q", skill.Name)
		}
		skills[skill.Name] = *skill
	}
	return skills, nil
}

func (l *ConfigLoader) loadSkill(path string) (*types.Skill, error) {
	data, err := l.fileStore.Read(path)
	if err != nil {
		return nil, err
	}

	var skill types.Skill
	body, err := frontmatter.Parse(strings.NewReader(string(data)), &skill)
	if err != nil {
		return nil, err
	}
	if err := l.validateSkillScripts(path, skill.Scripts); err != nil {
		return nil, err
	}
	skill.Body = strings.TrimSpace(string(body))
	return &skill, nil
}

// validateSkillScripts keeps a Skill self-contained. A script may be invoked
// by a Team command, but it must live inside the Skill package so copying the
// package does not silently retain a dependency on another directory.
func (l *ConfigLoader) validateSkillScripts(skillPath string, scripts []string) error {
	skillDir := filepath.Dir(skillPath)
	for _, script := range scripts {
		script = strings.TrimSpace(script)
		if script == "" {
			return fmt.Errorf("skill script path must not be empty")
		}
		if filepath.IsAbs(script) {
			return fmt.Errorf("skill script path %q must be relative to the Skill directory", script)
		}

		clean := filepath.Clean(script)
		if clean == "." || clean == ".." ||
			strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("skill script path %q escapes the Skill directory", script)
		}
		if !l.fileStore.Exists(filepath.Join(skillDir, clean)) {
			return fmt.Errorf("skill script %q does not exist", script)
		}
	}
	return nil
}

func (l *ConfigLoader) loadRuleDefinitions(dir string) (map[string]types.RuleItem, error) {
	rules := make(map[string]types.RuleItem)
	if !l.fileStore.Exists(dir) {
		return rules, nil
	}

	files, err := l.fileStore.List(dir)
	if err != nil {
		return nil, err
	}
	for _, name := range files {
		if filepath.Ext(name) != ".md" {
			continue
		}
		rule, err := l.loadRule(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", name, err)
		}
		if strings.TrimSpace(rule.ID) == "" {
			rule.ID = strings.TrimSuffix(name, filepath.Ext(name))
		}
		if _, exists := rules[rule.ID]; exists {
			return nil, fmt.Errorf("duplicate rule definition %q", rule.ID)
		}
		rules[rule.ID] = *rule
	}
	return rules, nil
}

func (l *ConfigLoader) loadAgentRuleDefinitions(agentsDir string) (map[string]types.RuleItem, error) {
	rules := make(map[string]types.RuleItem)
	if !l.fileStore.Exists(agentsDir) {
		return rules, nil
	}

	agents, err := l.fileStore.List(agentsDir)
	if err != nil {
		return nil, err
	}
	for _, agentName := range agents {
		ruleDir := filepath.Join(agentsDir, agentName, "rules")
		nested, err := l.loadRuleDefinitions(ruleDir)
		if err != nil {
			return nil, err
		}
		for name, rule := range nested {
			if _, exists := rules[name]; exists {
				return nil, fmt.Errorf("duplicate rule definition %q", name)
			}
			rules[name] = rule
		}
	}
	// A directory-form Agent may reference a shared rule file from its own
	// sibling rules directory. If a flat Agent file references a rule that is
	// not present in .agents/rules, keep validation permissive because the
	// legacy examples historically used that field as prompt metadata.
	return rules, nil
}

func validateAgentCallDefinitions(flow types.Flow, teams map[string]types.Team, agents map[string]types.AgentConfig) error {
	for flowTeamName, binding := range flow.Teams {
		team, ok := teams[binding.TeamID]
		if !ok {
			return fmt.Errorf("flow team %q references missing definition %q", flowTeamName, binding.TeamID)
		}
		for callName, call := range team.Calls {
			if call.Type != types.CallAgent {
				continue
			}
			if _, ok := agents[call.AgentID]; !ok {
				return fmt.Errorf(
					"team %q call %q references missing agent definition %q",
					binding.TeamID,
					callName,
					call.AgentID,
				)
			}
		}
	}
	return nil
}

func validateAgentSupportDefinitions(
	flow types.Flow,
	teams map[string]types.Team,
	agents map[string]types.AgentConfig,
	skills map[string]types.Skill,
	rules map[string]types.RuleItem,
) error {
	for flowTeamName, binding := range flow.Teams {
		team, ok := teams[binding.TeamID]
		if !ok {
			return fmt.Errorf("flow team %q references missing definition %q", flowTeamName, binding.TeamID)
		}
		for callName, call := range team.Calls {
			if call.Type != types.CallAgent {
				continue
			}
			agent, ok := agents[call.AgentID]
			if !ok {
				return fmt.Errorf("team %q call %q references missing agent %q", binding.TeamID, callName, call.AgentID)
			}
			for _, skillName := range agent.Skills {
				if _, ok := skills[skillName]; !ok {
					return fmt.Errorf("agent %q references missing skill %q", agent.Name, skillName)
				}
			}
			for _, ruleName := range agent.Rules {
				if _, ok := rules[ruleName]; !ok {
					continue
				}
			}
		}
	}
	return nil
}
