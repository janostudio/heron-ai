package config

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// scopesDirName is where installed Team packages live under the config root.
// It is nested inside .agents/ rather than spread across teams/, agents/ and
// skills/, so removing a package is one directory removal.
const scopesDirName = "_teams"

// scopeDefinitions is one directory under _teams/: a self-contained copy of
// the local tree layout (teams/, agents/, skills/, rules/) plus a
// heron-team.yml manifest.
//
// Keys are the names as written inside the package (bare names), not the
// namespaced keys published into types.Definitions. Resolution within a scope
// therefore never has to strip a prefix back off.
type scopeDefinitions struct {
	Scope  string
	Teams  map[string]types.Team
	Agents map[string]types.AgentConfig
	Skills map[string]types.Skill
	Rules  map[string]types.RuleItem
}

// loadScopeDefinitions loads every package under _teams/ and publishes it into
// the same maps as the local tree.
//
// Packages are isolated from each other and from the local tree: a Team is
// published only as "<scope>/<name>", and its Calls are rewritten to the
// package's own "<scope>/<agent>" keys. Nothing can therefore change how a
// local Team or a local Flow binding behaves, which is the point of the
// directory split.
func (l *ConfigLoader) loadScopeDefinitions(
	ctx context.Context,
	scopesDir string,
	teams map[string]types.Team,
	agents map[string]types.AgentConfig,
	skills map[string]types.Skill,
	rules map[string]types.RuleItem,
	flowTeamScopes map[string]string,
) (map[string]scopeDefinitions, error) {
	scopes := make(map[string]scopeDefinitions)
	if !l.fileStore.Exists(scopesDir) {
		return scopes, nil
	}

	entries, err := l.fileStore.List(scopesDir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", scopesDir, err)
	}
	// Sorted so loading is deterministic when two packages disagree about
	// something, instead of depending on directory read order.
	sort.Strings(entries)

	for _, scope := range entries {
		if strings.HasPrefix(scope, ".") || strings.Contains(scope, "/") {
			continue
		}
		scopeDir := filepath.Join(scopesDir, scope)
		if filepath.Ext(scope) != "" {
			continue
		}

		sc, err := l.loadScope(ctx, scope, scopeDir)
		if err != nil {
			return nil, fmt.Errorf("load team package %q: %w", scope, err)
		}
		publishScope(sc, teams, agents, skills, rules, flowTeamScopes)
		scopes[scope] = sc
	}
	return scopes, nil
}

func (l *ConfigLoader) loadScope(ctx context.Context, scope, scopeDir string) (scopeDefinitions, error) {
	if err := ctx.Err(); err != nil {
		return scopeDefinitions{}, err
	}
	sc := scopeDefinitions{Scope: scope}
	var err error
	if sc.Teams, err = l.loadDefinitionTeams(filepath.Join(scopeDir, "teams")); err != nil {
		return sc, fmt.Errorf("load teams: %w", err)
	}
	if sc.Agents, err = l.loadAgentDefinitions(filepath.Join(scopeDir, "agents")); err != nil {
		return sc, fmt.Errorf("load agents: %w", err)
	}
	if sc.Skills, err = l.loadSkillDefinitions(filepath.Join(scopeDir, "skills")); err != nil {
		return sc, fmt.Errorf("load skills: %w", err)
	}
	if sc.Rules, err = l.loadRuleDefinitions(filepath.Join(scopeDir, "rules")); err != nil {
		return sc, fmt.Errorf("load rules: %w", err)
	}
	agentRules, err := l.loadAgentRuleDefinitions(filepath.Join(scopeDir, "agents"))
	if err != nil {
		return sc, fmt.Errorf("load agent rules: %w", err)
	}
	for name, rule := range agentRules {
		if _, exists := sc.Rules[name]; exists {
			return sc, fmt.Errorf("duplicate rule definition %q", name)
		}
		sc.Rules[name] = rule
	}

	// A scope directory without a manifest is a valid leftover: `teams remove
	// --keep-agents` deletes teams/ and heron-team.yml but keeps the asset
	// directories. Treating it as broken would make the whole config fail to
	// load after a supported command, so it is published like any other scope.
	manifestPath := filepath.Join(scopeDir, "heron-team.yml")
	if !l.fileStore.Exists(manifestPath) {
		return sc, nil
	}
	data, err := l.fileStore.Read(manifestPath)
	if err != nil {
		return sc, fmt.Errorf("read heron-team.yml: %w", err)
	}
	var manifest teamManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return sc, fmt.Errorf("decode heron-team.yml: %w", err)
	}

	provided := make(map[string]struct{}, len(manifest.Provides.Teams))
	for _, name := range manifest.Provides.Teams {
		provided[name] = struct{}{}
		if _, ok := sc.Teams[name]; !ok {
			return sc, fmt.Errorf("heron-team.yml lists team %q but %s does not define it", name, filepath.Join("teams", name+".yml"))
		}
	}
	// An empty provides.teams means "the package does not curate its entry
	// points", which is what heron-team.yml in the wild looks like before the
	// manifest carries the block. Offer everything rather than nothing.
	if len(provided) == 0 {
		return sc, nil
	}
	for name := range sc.Teams {
		if _, ok := provided[name]; !ok {
			delete(sc.Teams, name)
		}
	}
	return sc, nil
}

// publishScope writes one package into the shared maps under "<scope>/<name>"
// keys. A Call that names a package Agent is rewritten to that key, so the
// runtime -- which resolves ids against the published maps -- reads the
// package's Agent and never a local one of the same name.
func publishScope(
	sc scopeDefinitions,
	teams map[string]types.Team,
	agents map[string]types.AgentConfig,
	skills map[string]types.Skill,
	rules map[string]types.RuleItem,
	flowTeamScopes map[string]string,
) {
	for name, team := range sc.Teams {
		key := sc.Scope + "/" + name
		teams[key] = resolveScopeTeam(sc, team)
		flowTeamScopes[key] = sc.Scope
	}
	for name, agent := range sc.Agents {
		agents[sc.Scope+"/"+name] = agent
	}
	// A package Team's Agent references its Skills and Rules by bare name and
	// those references are not rewritten, so the published copies have to be
	// reachable by the same bare name they were written with.
	for name, skill := range sc.Skills {
		skills[name] = skill
	}
	for name, rule := range sc.Rules {
		rules[name] = rule
	}
}

// missingScopeAgent is a Call that names an Agent its package does not ship.
// It is reported by name so the error can read as a reference, not a lookup.
type missingScopeAgent struct {
	Scope   string
	Team    string
	Call    string
	AgentID string
}

func (e missingScopeAgent) Error() string {
	return fmt.Sprintf("team %q call %q references missing agent %q", e.Scope+"/"+e.Team, e.Call, e.AgentID)
}

// teamManifest is the subset of heron-team.yml the loader reads. It is
// deliberately small: the loader only needs to know which Teams the package
// offers; sandbox, scripts and requirements are install-time concerns.
type teamManifest struct {
	Name     string `yaml:"name"`
	Version  string `yaml:"version"`
	Provides struct {
		Teams []string `yaml:"teams"`
	} `yaml:"provides"`
}

// validateScopeDefinitions validates only the packages the Flow actually binds,
// and validates each of them against its own assets. Two rules follow from the
// package contract and are the reason this is separate from the local
// validators:
//
//   - resolution is scope-only. A package Team naming an Agent/skill/rule it
//     does not ship is an error even when the local tree has that name; falling
//     back would make the package look installed while behaving differently.
//   - a name that resolves to the package's own definition is rewritten to its
//     published key, so the runtime, which resolves ids against the published
//     maps, sees the package version rather than a shadowed local one.
func validateScopeDefinitions(
	flow types.Flow,
	scopes map[string]scopeDefinitions,
	flowTeamScopes map[string]string,
	skills map[string]types.Skill,
	rules map[string]types.RuleItem,
) error {
	// Sorted so multiple broken packages report in a stable order.
	bound := make([]string, 0, len(flow.Teams))
	for _, binding := range flow.Teams {
		if _, ok := flowTeamScopes[binding.TeamID]; ok {
			bound = append(bound, binding.TeamID)
		}
	}
	sort.Strings(bound)

	for _, teamKey := range bound {
		sc, ok := scopes[flowTeamScopes[teamKey]]
		if !ok {
			return fmt.Errorf("flow team definition %q not found", teamKey)
		}
		team, ok := sc.Teams[strings.TrimPrefix(teamKey, sc.Scope+"/")]
		if !ok {
			return fmt.Errorf("flow team definition %q not found in team package %q", teamKey, sc.Scope)
		}
		if err := validateScopeAgentSupport(sc, team, skills, rules); err != nil {
			return err
		}
		if err := team.Validate(); err != nil {
			return fmt.Errorf("team %q: %w", teamKey, err)
		}
	}
	return nil
}

// resolveScopeTeam is resolution for a package Team: each agent Call must name
// an Agent the same package ships, and is rewritten to the key that Agent was
// published under. Package resolution never consults the local tree, so a
// package that does not ship what it references is reported instead of
// silently running the local definition.
func resolveScopeTeam(sc scopeDefinitions, team types.Team) types.Team {
	if len(team.Calls) == 0 {
		return team
	}
	calls := make(map[string]types.Call, len(team.Calls))
	for name, call := range team.Calls {
		if call.Type == types.CallAgent {
			call.AgentID = sc.Scope + "/" + strings.TrimPrefix(call.AgentID, sc.Scope+"/")
		}
		calls[name] = call
	}
	team.Calls = calls
	return team
}

// validateScopeAgentSupport checks a package Team's references against the
// package that ships them. Package definitions are not part of the Flow graph,
// so the local support validation never sees them; a package must be complete
// on its own or its behaviour depends on what happens to be installed locally.
func validateScopeAgentSupport(sc scopeDefinitions, team types.Team, skills map[string]types.Skill, rules map[string]types.RuleItem) error {
	for _, call := range team.Calls {
		if call.Type != types.CallAgent {
			continue
		}
		agentID := strings.TrimPrefix(call.AgentID, sc.Scope+"/")
		agent, ok := sc.Agents[agentID]
		if !ok {
			return missingScopeAgent{Scope: sc.Scope, Team: team.ID, Call: call.ID, AgentID: agentID}
		}
		for _, skillName := range agent.Skills {
			if _, ok := sc.Skills[skillName]; !ok {
				return fmt.Errorf("team package %q: agent %q references missing skill %q", sc.Scope, agent.Name, skillName)
			}
		}
		for _, ruleName := range agent.Rules {
			if _, ok := sc.Rules[ruleName]; !ok {
				return fmt.Errorf("team package %q: agent %q references missing rule %q", sc.Scope, agent.Name, ruleName)
			}
		}
	}
	return nil
}
