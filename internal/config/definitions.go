package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

	// Loaded Team packages live beside the local tree, so a broken package must
	// not block a Flow that does not use it: scope validation is deferred until
	// a Flow binding actually names that scope.
	flowTeamScopes := make(map[string]string, len(flow.Teams))
	scopes, err := l.loadScopeDefinitions(ctx, filepath.Join(configRoot, scopesDirName), teams, agents, skills, rules, flowTeamScopes)
	if err != nil {
		return nil, err
	}

	// Packages are validated before the local chain so a package Team's Calls
	// are resolved against its own tree before Flow.ValidateWithTeams checks
	// them, and so a broken package names its own Team rather than surfacing
	// as a Flow-level "missing agent".
	if err := validateScopeDefinitions(flow, scopes, flowTeamScopes, skills, rules); err != nil {
		return nil, fmt.Errorf("validate team packages: %w", err)
	}
	if err := flow.ValidateWithTeams(teams); err != nil {
		return nil, fmt.Errorf("validate definitions: %w", err)
	}
	if err := validateAgentCallDefinitions(flow, teams, agents, flowTeamScopes); err != nil {
		return nil, fmt.Errorf("validate agents: %w", err)
	}
	if err := validateAgentSupportDefinitions(flow, teams, agents, skills, flowTeamScopes); err != nil {
		return nil, fmt.Errorf("validate agent support: %w", err)
	}
	if err := validateRecordReferences(flow, teams, flowTeamScopes); err != nil {
		return nil, fmt.Errorf("validate record references: %w", err)
	}

	workspace, err := resolveWorkspace(flow, teams, agents)
	if err != nil {
		return nil, err
	}
	return &types.Definitions{
		Flow:      flow,
		Teams:     teams,
		Agents:    agents,
		Skills:    skills,
		Rules:     rules,
		Limits:    l.LoadRuntimeLimits(),
		Knowledge: l.LoadKnowledgeSettings(),
		Web:       l.LoadWebSettings(),
		Workspace: workspace,
		Env:       l.LoadGlobalEnv(),
	}, nil
}

// resolveWorkspace resolves the runtime's single execution backend from
// three-level inheritance: agent > team > flow, falling back to local (nil)
// when nothing is configured.
//
// Within one level, every declaration must agree. The runtime has exactly one
// backend (the Workspace interface is process-wide), so "the first agent that
// declares one" was decided by Go's map iteration order: the same configuration
// could pick a different backend on the next start. Disagreement is now a
// configuration error that names the candidates, not a coin flip.
func resolveWorkspace(flow types.Flow, teams map[string]types.Team, agents map[string]types.AgentConfig) (*types.WorkspaceConfig, error) {
	agentWorkspaces := make(map[string]*types.WorkspaceConfig)
	for id, agent := range agents {
		if agent.Workspace != nil && agent.Workspace.Type != "" {
			agentWorkspaces[id] = agent.Workspace
		}
	}
	if resolved, err := singleWorkspace("agent", agentWorkspaces); resolved != nil || err != nil {
		return resolved, err
	}

	teamWorkspaces := make(map[string]*types.WorkspaceConfig)
	for id, team := range teams {
		if team.Workspace != nil && team.Workspace.Type != "" {
			teamWorkspaces[id] = team.Workspace
		}
	}
	if resolved, err := singleWorkspace("team", teamWorkspaces); resolved != nil || err != nil {
		return resolved, err
	}

	if flow.Workspace != nil && flow.Workspace.Type != "" {
		return flow.Workspace, nil
	}
	return nil, nil
}

// singleWorkspace returns the one distinct workspace declared at a level, nil
// when nothing is declared, or an error when the declarations disagree.
func singleWorkspace(level string, declared map[string]*types.WorkspaceConfig) (*types.WorkspaceConfig, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	type candidate struct {
		owners    []string
		workspace *types.WorkspaceConfig
		signature string
	}
	bySignature := make(map[string]*candidate, len(declared))
	for _, owner := range sortedKeys(declared) {
		workspace := declared[owner]
		signature := workspaceSignature(workspace)
		if existing, ok := bySignature[signature]; ok {
			existing.owners = append(existing.owners, owner)
			continue
		}
		bySignature[signature] = &candidate{owners: []string{owner}, workspace: workspace, signature: signature}
	}
	if len(bySignature) == 1 {
		for _, only := range bySignature {
			return only.workspace, nil
		}
	}
	descriptions := make([]string, 0, len(bySignature))
	for _, signature := range sortedKeys(bySignature) {
		entry := bySignature[signature]
		descriptions = append(descriptions, fmt.Sprintf("%s (%s)", entry.workspace.Type, strings.Join(entry.owners, ", ")))
	}
	return nil, fmt.Errorf("conflicting %s-level workspaces: %s; the runtime has one execution backend, so declare it once or set it at flow level",
		level, strings.Join(descriptions, "; "))
}

// workspaceSignature identifies a backend by every field that changes where
// commands run, so two declarations that differ only by SSH host or root are
// treated as a conflict rather than as agreement.
func workspaceSignature(workspace *types.WorkspaceConfig) string {
	if workspace == nil {
		return ""
	}
	if workspace.SSH == nil {
		return workspace.Type
	}
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%s",
		workspace.Type, workspace.SSH.Host, workspace.SSH.Port, workspace.SSH.User,
		workspace.SSH.Root, workspace.SSH.KeyPath)
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ConfigRootForFlow derives the config root from a flow file path: the
// directory holding the flow, unless that directory is a "flows" subdirectory
// with a sibling teams/ or agents/ directory, in which case the shared root
// above it is the config root.
//
// This mirrors ConfigLoader.configRootForFlow and exists so callers outside
// this package — the definition store, which is built before any loader exists
// — derive the root the same way instead of growing a second, drifting
// implementation. It stats the candidates directly, so a caller that only knows
// the flow path gets the same answer as a configured loader.
func ConfigRootForFlow(flowPath string) string {
	configRoot := filepath.Dir(flowPath)
	if filepath.Base(configRoot) != "flows" {
		return configRoot
	}

	parent := filepath.Dir(configRoot)
	if dirExists(filepath.Join(parent, "teams")) ||
		dirExists(filepath.Join(parent, "agents")) {
		return parent
	}
	return configRoot
}

// configRootForFlow keeps the method form for callers that hold a loader. It
// shares the shape with ConfigRootForFlow but not the existence check: the
// loader asks its FileStore, which resolves paths against the loader's own base
// directory, whereas the package-level helper can only stat the filesystem.
// Keeping the two in step matters — the definition store derives a root from
// the flow path alone and must land on the same directory a loader would.
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

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
		rule, err := l.loadRuleMeta(filepath.Join(dir, name))
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

// validateAgentCallDefinitions checks local Teams only: a Team from a package
// must resolve its Calls inside its own scope, which validateScopeDefinitions
// enforces, and must never fall back to a local Agent of the same name.
func validateAgentCallDefinitions(flow types.Flow, teams map[string]types.Team, agents map[string]types.AgentConfig, flowTeamScopes map[string]string) error {
	for flowTeamName, binding := range flow.Teams {
		team, ok := teams[binding.TeamID]
		if !ok {
			return fmt.Errorf("flow team %q references missing definition %q", flowTeamName, binding.TeamID)
		}
		if _, ok := flowTeamScopes[binding.TeamID]; ok {
			continue
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

// builtinRecordProducers are records the engine publishes itself. They have no
// definition entry to point at, so they are exempt from the producer check.
var builtinRecordProducers = map[string]struct{}{
	"TeamFailureReport": {},
}

// recordPlaceholder matches ${records.<Name>...} in a command call.
var recordPlaceholder = regexp.MustCompile(`\$\{records\.([A-Za-z0-9_.-]+)`)

// validateRecordReferences closes two gaps that used to surface only at
// runtime, as an empty input or a failed command:
//
//  1. An input naming a record no Team or Call publishes. The turn then starts
//     without that record and the agent reports "no evidence" instead of the
//     configuration error it actually is.
//  2. A command call using ${records.X...} without declaring X in its inputs.
//     The command fails on the missing field, which reads like a runtime bug.
//
// Only local Teams are checked; package-scoped Teams resolve their own calls.
func validateRecordReferences(flow types.Flow, teams map[string]types.Team, flowTeamScopes map[string]string) error {
	producers := map[string]string{}
	register := func(name, where string) {
		if strings.TrimSpace(name) == "" {
			return
		}
		if _, exists := producers[name]; !exists {
			producers[name] = where
		}
	}

	for flowTeamName, binding := range flow.Teams {
		team, ok := teams[binding.TeamID]
		if !ok {
			continue
		}
		if _, scoped := flowTeamScopes[binding.TeamID]; scoped {
			continue
		}
		_ = flowTeamName
		register(team.Output.Record, "team "+team.ID)
		register(team.Outputs.Record, "team "+team.ID)
		for _, output := range team.Output.Records {
			register(output.Record, "team "+team.ID)
		}
		for _, output := range team.Outputs.Records {
			register(output.Record, "team "+team.ID)
		}
		for callName, call := range team.Calls {
			where := "team " + team.ID + " call " + callName
			register(call.Output.Record, where)
			for _, output := range call.Output.Records {
				register(output.Record, where)
			}
		}
	}

	checkInputs := func(owner string, spec types.InputSpec) error {
		for _, name := range spec.FlowRecords {
			if err := requireProducer(owner, name, producers); err != nil {
				return err
			}
		}
		for _, name := range spec.TeamRecords {
			if err := requireProducer(owner, name, producers); err != nil {
				return err
			}
		}
		for _, input := range spec.Records {
			if err := requireProducer(owner, input.Record, producers); err != nil {
				return err
			}
		}
		return nil
	}

	for flowTeamName, binding := range flow.Teams {
		team, ok := teams[binding.TeamID]
		if !ok {
			continue
		}
		if _, scoped := flowTeamScopes[binding.TeamID]; scoped {
			continue
		}
		if err := checkInputs("flow team "+flowTeamName, binding.Inputs); err != nil {
			return err
		}
		if err := checkInputs("team "+team.ID, team.Inputs); err != nil {
			return err
		}
		for callName, call := range team.Calls {
			owner := "team " + team.ID + " call " + callName
			if err := checkInputs(owner, call.Inputs); err != nil {
				return err
			}
			if err := validateCommandPlaceholders(owner, call); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireProducer(owner, record string, producers map[string]string) error {
	if strings.TrimSpace(record) == "" {
		return nil
	}
	if _, builtin := builtinRecordProducers[record]; builtin {
		return nil
	}
	if _, exists := producers[record]; exists {
		return nil
	}
	return fmt.Errorf("%s inputs reference record %q, but no team or call publishes it", owner, record)
}

// validateCommandPlaceholders enforces the documented rule that a command may
// only interpolate records its inputs declare. Failing here beats failing at
// runtime with "missing field".
func validateCommandPlaceholders(owner string, call types.Call) error {
	if call.Command == nil {
		return nil
	}
	declared := map[string]struct{}{}
	for _, name := range call.Inputs.FlowRecords {
		declared[name] = struct{}{}
	}
	for _, name := range call.Inputs.TeamRecords {
		declared[name] = struct{}{}
	}
	for _, input := range call.Inputs.Records {
		if input.Record != "" {
			declared[input.Record] = struct{}{}
		}
		if input.As != "" {
			declared[input.As] = struct{}{}
		}
	}

	texts := []string{call.Command.Command}
	texts = append(texts, call.Command.Args...)
	for _, value := range call.Command.Env {
		texts = append(texts, value)
	}
	for _, text := range texts {
		for _, match := range recordPlaceholder.FindAllStringSubmatch(text, -1) {
			name := match[1]
			// ${records.X.Summary} / ${records.X.Data.y}: the record is the
			// first segment.
			if dot := strings.Index(name, "."); dot >= 0 {
				name = name[:dot]
			}
			if _, ok := declared[name]; ok {
				continue
			}
			return fmt.Errorf("%s command references ${records.%s}, but its inputs do not declare that record", owner, name)
		}
	}
	return nil
}

func validateAgentSupportDefinitions(
	flow types.Flow,
	teams map[string]types.Team,
	agents map[string]types.AgentConfig,
	skills map[string]types.Skill,
	flowTeamScopes map[string]string,
) error {
	for flowTeamName, binding := range flow.Teams {
		team, ok := teams[binding.TeamID]
		if !ok {
			return fmt.Errorf("flow team %q references missing definition %q", flowTeamName, binding.TeamID)
		}
		if _, ok := flowTeamScopes[binding.TeamID]; ok {
			continue
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
		}
	}
	return nil
}
