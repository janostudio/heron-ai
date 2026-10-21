package teamspkg

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Tree is the asset snapshot read from a package directory. Subdirectory
// names mirror .agents/ so an installed package maps one-to-one onto the
// loader's existing layout.
type Tree struct {
	Teams   map[string]types.Team
	Agents  map[string]types.AgentConfig
	Skills  map[string]types.Skill
	Rules   []string
	Scripts map[string][]string // skill name -> script paths, relative to the skill directory
}

// Collect reads teams/, agents/, skills/ and rules/ below root. A missing
// directory is not an error: a package may legitimately ship only some asset
// kinds. Knowledge directories are never read (see design §9.2).
func Collect(files storage.FileStore, root string) (*Tree, error) {
	t := &Tree{
		Teams:   map[string]types.Team{},
		Agents:  map[string]types.AgentConfig{},
		Skills:  map[string]types.Skill{},
		Scripts: map[string][]string{},
	}

	teams, err := collectTeams(files, path.Join(root, "teams"))
	if err != nil {
		return nil, err
	}
	t.Teams = teams

	agents, err := collectAgents(files, path.Join(root, "agents"))
	if err != nil {
		return nil, err
	}
	t.Agents = agents

	skills, scripts, err := collectSkills(files, path.Join(root, "skills"))
	if err != nil {
		return nil, err
	}
	t.Skills = skills
	t.Scripts = scripts

	rules, err := collectRules(files, path.Join(root, "rules"))
	if err != nil {
		return nil, err
	}
	t.Rules = rules

	return t, nil
}

func collectTeams(files storage.FileStore, dir string) (map[string]types.Team, error) {
	out := map[string]types.Team{}
	names, err := files.List(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	sort.Strings(names)
	for _, name := range names {
		ext := filepath.Ext(name)
		if ext != ".yml" && ext != ".yaml" {
			continue
		}
		data, err := files.Read(path.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path.Join(dir, name), err)
		}
		var team types.Team
		if err := yaml.Unmarshal(data, &team); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path.Join(dir, name), err)
		}
		team.Normalize()
		if strings.TrimSpace(team.ID) == "" {
			team.ID = strings.TrimSuffix(name, ext)
		}
		if _, exists := out[team.ID]; exists {
			return nil, fmt.Errorf("duplicate team definition %q", team.ID)
		}
		out[team.ID] = team
	}
	return out, nil
}

func collectAgents(files storage.FileStore, dir string) (map[string]types.AgentConfig, error) {
	out := map[string]types.AgentConfig{}
	names, err := files.List(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	sort.Strings(names)
	for _, name := range names {
		var agentPath, dirName string
		switch {
		case filepath.Ext(name) == ".md":
			// Flat <agent-id>.md is the legacy layout; packages use directories
			// only, but reading it costs nothing and keeps Collect usable on a
			// plain .agents/ tree.
			agentPath = path.Join(dir, name)
			dirName = strings.TrimSuffix(name, ".md")
		case filepath.Ext(name) == "":
			agentPath = path.Join(dir, name, "AGENT.md")
			dirName = name
			if !files.Exists(agentPath) {
				continue
			}
		default:
			continue
		}

		data, err := files.Read(agentPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", agentPath, err)
		}
		var agent types.AgentConfig
		body, err := frontmatter.Parse(strings.NewReader(string(data)), &agent)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", agentPath, err)
		}
		agent.Body = string(body)
		if strings.TrimSpace(agent.Name) == "" {
			agent.Name = dirName
		}
		if _, exists := out[agent.Name]; exists {
			return nil, fmt.Errorf("duplicate agent definition %q", agent.Name)
		}
		out[agent.Name] = agent
	}
	return out, nil
}

// collectSkills returns the parsed skills plus their script lists keyed by
// skill name. Scripts are collected separately because Closure rebuilds a
// Skill from its own fields; the script list is not part of types.Skill's
// identity but is needed by check.go.
func collectSkills(files storage.FileStore, dir string) (map[string]types.Skill, map[string][]string, error) {
	out := map[string]types.Skill{}
	scripts := map[string][]string{}
	names, err := files.List(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("list %s: %w", dir, err)
	}
	sort.Strings(names)
	for _, name := range names {
		if filepath.Ext(name) != "" {
			continue
		}
		skillPath := path.Join(dir, name, "SKILL.md")
		if !files.Exists(skillPath) {
			continue
		}
		data, err := files.Read(skillPath)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", skillPath, err)
		}
		var skill types.Skill
		body, err := frontmatter.Parse(strings.NewReader(string(data)), &skill)
		if err != nil {
			return nil, nil, fmt.Errorf("decode %s: %w", skillPath, err)
		}
		skill.Body = strings.TrimSpace(string(body))
		if strings.TrimSpace(skill.Name) == "" {
			skill.Name = name
		}
		if _, exists := out[skill.Name]; exists {
			return nil, nil, fmt.Errorf("duplicate skill definition %q", skill.Name)
		}
		out[skill.Name] = skill
		scripts[skill.Name] = append([]string(nil), skill.Scripts...)
	}
	return out, scripts, nil
}

func collectRules(files storage.FileStore, dir string) ([]string, error) {
	var out []string
	seen := map[string]struct{}{}
	names, err := files.List(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	sort.Strings(names)
	for _, name := range names {
		if filepath.Ext(name) != ".md" {
			continue
		}
		rulePath := path.Join(dir, name)
		data, err := files.Read(rulePath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rulePath, err)
		}
		var rule types.RuleItem
		if _, err := frontmatter.Parse(strings.NewReader(string(data)), &rule); err != nil {
			return nil, fmt.Errorf("decode %s: %w", rulePath, err)
		}
		id := strings.TrimSpace(rule.ID)
		if id == "" {
			id = strings.TrimSuffix(name, filepath.Ext(name))
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("duplicate rule definition %q", id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// Closure returns the subset of the tree reachable from teamID: the team
// itself, the Agents its calls reference, the Skills and Rules those Agents
// declare. Assets the entry team never reaches are dropped, which is what
// keeps a shared package to what the Team actually needs.
//
// The graph may contain cycles (an Agent listing a Skill that mentions the
// Agent back by name), so every asset kind carries its own visited set.
func (t *Tree) Closure(teamID string) (*Tree, error) {
	root, ok := t.Teams[teamID]
	if !ok {
		return nil, fmt.Errorf("team %q not found", teamID)
	}

	out := &Tree{
		Teams:   map[string]types.Team{},
		Agents:  map[string]types.AgentConfig{},
		Skills:  map[string]types.Skill{},
		Scripts: map[string][]string{},
	}
	out.Teams[root.ID] = root

	ruleSeen := map[string]struct{}{}
	// Dangling references are left dangling on purpose: Closure reports what
	// the tree actually reaches, and Check is what turns a missing name into a
	// user-facing error.
	for _, call := range root.Calls {
		if call.Type != types.CallAgent || strings.TrimSpace(call.AgentID) == "" {
			continue
		}
		agent, ok := t.Agents[call.AgentID]
		if !ok {
			continue
		}
		if _, done := out.Agents[agent.Name]; done {
			continue
		}
		out.Agents[agent.Name] = agent

		for _, skillName := range agent.Skills {
			if _, done := out.Skills[skillName]; done {
				continue
			}
			skill, ok := t.Skills[skillName]
			if !ok {
				continue
			}
			out.Skills[skillName] = skill
			if scripts, ok := t.Scripts[skillName]; ok {
				out.Scripts[skillName] = append([]string(nil), scripts...)
			}
		}

		for _, ruleName := range agent.Rules {
			if _, done := ruleSeen[ruleName]; done {
				continue
			}
			if !t.hasRule(ruleName) {
				continue
			}
			ruleSeen[ruleName] = struct{}{}
			out.Rules = append(out.Rules, ruleName)
		}
	}

	sort.Strings(out.Rules)
	return out, nil
}

func (t *Tree) hasRule(name string) bool {
	for _, rule := range t.Rules {
		if rule == name {
			return true
		}
	}
	return false
}
