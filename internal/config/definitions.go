package config

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// DefinitionsLoadRequest describes loading the new Flow/Team/Member
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

	if err := flow.ValidateWithTeams(teams); err != nil {
		return nil, fmt.Errorf("validate definitions: %w", err)
	}
	if err := validateSubagentDefinitions(flow, teams, agents); err != nil {
		return nil, fmt.Errorf("validate subagents: %w", err)
	}

	return &types.Definitions{
		Flow:   flow,
		Teams:  teams,
		Agents: agents,
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

func validateSubagentDefinitions(flow types.Flow, teams map[string]types.Team, agents map[string]types.AgentConfig) error {
	for flowTeamName, binding := range flow.Teams {
		team, ok := teams[binding.TeamID]
		if !ok {
			return fmt.Errorf("flow team %q references missing definition %q", flowTeamName, binding.TeamID)
		}
		for memberName, member := range team.Members {
			if member.Type != types.MemberSubagent {
				continue
			}
			if _, ok := agents[member.AgentID]; !ok {
				return fmt.Errorf(
					"team %q member %q references missing agent definition %q",
					binding.TeamID,
					memberName,
					member.AgentID,
				)
			}
		}
	}
	return nil
}
