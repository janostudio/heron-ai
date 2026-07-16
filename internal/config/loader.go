package config

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ConfigLoader loads .yml and .md configuration files from .agents/ directories.
type ConfigLoader struct {
	baseDir   string
	fileStore storage.FileStore
}

// NewConfigLoader creates a new ConfigLoader with the given base directory.
func NewConfigLoader(baseDir string) *ConfigLoader {
	return &ConfigLoader{
		baseDir:   baseDir,
		fileStore: storage.NewFileStore(baseDir),
	}
}

// LoadRequest contains parameters for loading a run configuration.
type LoadRequest struct {
	FlowPath  string
	Overrides *RunOverrides
	Variables map[string]string
}

// RunOverrides allows overriding loaded configuration values.
type RunOverrides struct {
	Flow   *types.FlowConfig
	Teams  map[string]*types.TeamConfig
	Agents map[string]*types.AgentConfig
}

// Load reads all configuration files for a run and returns a RunRequest.
func (l *ConfigLoader) Load(ctx context.Context, req LoadRequest) (*types.RunRequest, error) {
	// Determine config root: if flow is inside .agents/flows/, use .agents/ as root
	configRoot := filepath.Dir(req.FlowPath)
	if filepath.Base(configRoot) == "flows" {
		// Flow is in .agents/flows/, config root is .agents/
		parent := filepath.Dir(configRoot)
		if l.fileStore.Exists(filepath.Join(parent, "teams")) || l.fileStore.Exists(filepath.Join(parent, "agents")) {
			configRoot = parent
		}
	}

	// Load flow config
	flow, err := l.loadFlow(req.FlowPath)
	if err != nil {
		return nil, fmt.Errorf("load flow: %w", err)
	}

	// Load teams
	teams := make(map[string]types.TeamConfig)
	teamsDir := filepath.Join(configRoot, "teams")
	if l.fileStore.Exists(teamsDir) {
		teamFiles, err := l.fileStore.List(teamsDir)
		if err != nil {
			return nil, fmt.Errorf("list teams: %w", err)
		}
		for _, f := range teamFiles {
			if filepath.Ext(f) == ".yml" || filepath.Ext(f) == ".yaml" {
				team, err := l.loadTeam(filepath.Join(teamsDir, f))
				if err != nil {
					return nil, fmt.Errorf("load team %s: %w", f, err)
				}
				teams[team.Name] = *team
			}
		}
	}

	// Initialize rules and knowledge (populated from global dirs + agent subdirs)
	var rules []types.RuleItem
	var knowledge []types.KnowledgeEntry

	// Load agents
	agents := make(map[string]types.AgentConfig)
	agentsDir := filepath.Join(configRoot, "agents")
	if l.fileStore.Exists(agentsDir) {
		agentFiles, err := l.fileStore.List(agentsDir)
		if err != nil {
			return nil, fmt.Errorf("list agents: %w", err)
		}
		for _, f := range agentFiles {
			if filepath.Ext(f) == ".md" {
				agent, err := l.loadAgent(filepath.Join(agentsDir, f))
				if err != nil {
					return nil, fmt.Errorf("load agent %s: %w", f, err)
				}
				agents[agent.Name] = *agent
			}
		}
		// Also check for subdirectories (agent dirs with AGENT.md)
		for _, f := range agentFiles {
			agentDir := filepath.Join(agentsDir, f)
			agentFile := filepath.Join(agentDir, "AGENT.md")
			if !l.fileStore.Exists(agentFile) {
				continue
			}
			agent, err := l.loadAgent(agentFile)
			if err != nil {
				return nil, fmt.Errorf("load agent %s: %w", f, err)
			}
			agents[agent.Name] = *agent

			// Load agent-private knowledge from subdirectory
			agentKnowledgeDir := filepath.Join(agentDir, "knowledge")
			if l.fileStore.Exists(agentKnowledgeDir) {
				kFiles, err := l.fileStore.List(agentKnowledgeDir)
				if err == nil {
					for _, kf := range kFiles {
						if filepath.Ext(kf) == ".md" {
							entry, err := l.loadKnowledge(filepath.Join(agentKnowledgeDir, kf))
							if err == nil {
								knowledge = append(knowledge, *entry)
							}
						}
					}
				}
			}

			// Load agent-private rules from subdirectory
			agentRulesDir := filepath.Join(agentDir, "rules")
			if l.fileStore.Exists(agentRulesDir) {
				rFiles, err := l.fileStore.List(agentRulesDir)
				if err == nil {
					for _, rf := range rFiles {
						if filepath.Ext(rf) == ".md" {
							rule, err := l.loadRule(filepath.Join(agentRulesDir, rf))
							if err == nil {
								rules = append(rules, *rule)
							}
						}
					}
				}
			}
		}
	}

	// Load rules from global rules directory
	rulesDir := filepath.Join(configRoot, "rules")
	if l.fileStore.Exists(rulesDir) {
		ruleFiles, err := l.fileStore.List(rulesDir)
		if err != nil {
			return nil, fmt.Errorf("list rules: %w", err)
		}
		for _, f := range ruleFiles {
			if filepath.Ext(f) == ".md" {
				rule, err := l.loadRule(filepath.Join(rulesDir, f))
				if err != nil {
					return nil, fmt.Errorf("load rule %s: %w", f, err)
				}
				rules = append(rules, *rule)
			}
		}
	}

	// Load knowledge from global knowledge directory
	knowledgeDir := filepath.Join(configRoot, "knowledge")
	if l.fileStore.Exists(knowledgeDir) {
		kFiles, err := l.fileStore.List(knowledgeDir)
		if err != nil {
			return nil, fmt.Errorf("list knowledge: %w", err)
		}
		for _, f := range kFiles {
			if filepath.Ext(f) == ".md" {
				entry, err := l.loadKnowledge(filepath.Join(knowledgeDir, f))
				if err != nil {
					return nil, fmt.Errorf("load knowledge %s: %w", f, err)
				}
				knowledge = append(knowledge, *entry)
			}
		}
	}

	// Apply overrides
	if req.Overrides != nil {
		if req.Overrides.Flow != nil {
			flow = *req.Overrides.Flow
		}
		for name, team := range req.Overrides.Teams {
			teams[name] = *team
		}
		for name, agent := range req.Overrides.Agents {
			agents[name] = *agent
		}
	}

	// Load prompts
	prompts := make(map[string]string)
	promptsDir := filepath.Join(configRoot, "prompts")
	if l.fileStore.Exists(promptsDir) {
		promptFiles, err := l.fileStore.List(promptsDir)
		if err != nil {
			return nil, fmt.Errorf("list prompts: %w", err)
		}
		for _, f := range promptFiles {
			if filepath.Ext(f) == ".md" {
				data, err := l.fileStore.Read(filepath.Join(promptsDir, f))
				if err != nil {
					return nil, fmt.Errorf("read prompt %s: %w", f, err)
				}
				name := f[:len(f)-len(filepath.Ext(f))]
				prompts[name] = string(data)
			}
		}
	}

	variables := make(map[string]string)
	if req.Variables != nil {
		for k, v := range req.Variables {
			variables[k] = v
		}
	}

	runReq := &types.RunRequest{
		Flow:      flow,
		Teams:     teams,
		Agents:    agents,
		Rules:     rules,
		Knowledge: knowledge,
		Prompts:   prompts,
		Variables: variables,
	}

	if err := l.Validate(runReq); err != nil {
		return nil, fmt.Errorf("validation: %w", err)
	}

	return runReq, nil
}

// Validate checks that a RunRequest is complete and consistent.
func (l *ConfigLoader) Validate(req *types.RunRequest) error {
	if req.Flow.Name == "" {
		return fmt.Errorf("flow name is required")
	}
	if len(req.Flow.Stages) == 0 {
		return fmt.Errorf("flow must have at least one stage")
	}

	for i, stage := range req.Flow.Stages {
		if stage.Name == "" {
			return fmt.Errorf("flow stage %d: name is required", i)
		}
		if stage.Team == "" {
			return fmt.Errorf("flow stage %q: team is required", stage.Name)
		}
		if _, ok := req.Teams[stage.Team]; !ok {
			return fmt.Errorf("flow stage %q: team %q not found", stage.Name, stage.Team)
		}
	}

	for name, team := range req.Teams {
		if team.Name == "" {
			return fmt.Errorf("team %q: name is required", name)
		}
		for i, stage := range team.Stages {
			for j, task := range stage.Tasks {
				if task.Name == "" {
					return fmt.Errorf("team %q stage %d task %d: name is required", name, i, j)
				}
				if task.Agent == "" {
					return fmt.Errorf("team %q stage %d task %q: agent is required", name, i, task.Name)
				}
				if _, ok := req.Agents[task.Agent]; !ok {
					return fmt.Errorf("team %q stage %d task %q: agent %q not found", name, i, task.Name, task.Agent)
				}
			}
		}
	}

	return nil
}
