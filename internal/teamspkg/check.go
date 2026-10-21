package teamspkg

import (
	"fmt"
	"sort"
	"strings"
)

// Severity values for Problem.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Problem is one finding from Check. An error blocks installation; a warning
// means the package will install but behave differently than its author
// described.
type Problem struct {
	Severity string
	Message  string
}

// CheckResult reports findings plus the model ids the package needs. Models
// are collected rather than compared because models.json is the receiving
// side's environment; Check cannot know what is installed.
type CheckResult struct {
	Problems []Problem
	Missing  []string
	Models   []string
}

// HasErrors reports whether anything blocks installation.
func (r CheckResult) HasErrors() bool {
	for _, p := range r.Problems {
		if p.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Check validates a manifest against the asset tree it describes.
func Check(m *Manifest, t *Tree) CheckResult {
	var result CheckResult
	if m == nil {
		result.Problems = append(result.Problems, Problem{Severity: SeverityError, Message: "manifest is missing"})
		return result
	}
	if t == nil {
		t = &Tree{}
	}

	checkProvides(m, t, &result)
	checkReferences(t, &result)
	checkSandbox(m, t, &result)
	result.Models = collectModels(t)

	sort.Strings(result.Models)
	return result
}

// checkProvides verifies that every declared asset exists. A declared name
// that is absent means the package lies about its contents, so the receiving
// side would resolve a reference to nothing.
func checkProvides(m *Manifest, t *Tree, result *CheckResult) {
	if len(m.Provides.Teams) == 0 {
		result.Problems = append(result.Problems, Problem{
			Severity: SeverityError,
			Message:  fmt.Sprintf("manifest %q: provides.teams is empty", m.Name),
		})
	}
	for _, name := range m.Provides.Teams {
		if _, ok := t.Teams[name]; !ok {
			result.Problems = append(result.Problems, Problem{
				Severity: SeverityError,
				Message:  fmt.Sprintf("manifest %q provides missing team %q", m.Name, name),
			})
		}
	}
	for _, name := range m.Provides.Agents {
		if _, ok := t.Agents[name]; !ok {
			result.Problems = append(result.Problems, Problem{
				Severity: SeverityError,
				Message:  fmt.Sprintf("manifest %q provides missing agent %q", m.Name, name),
			})
		}
	}
	for _, name := range m.Provides.Skills {
		if _, ok := t.Skills[name]; !ok {
			result.Problems = append(result.Problems, Problem{
				Severity: SeverityError,
				Message:  fmt.Sprintf("manifest %q provides missing skill %q", m.Name, name),
			})
		}
	}
	for _, name := range m.Provides.Rules {
		if !t.hasRule(name) {
			result.Problems = append(result.Problems, Problem{
				Severity: SeverityError,
				Message:  fmt.Sprintf("manifest %q provides missing rule %q", m.Name, name),
			})
		}
	}
}

// checkReferences verifies that every in-tree reference resolves. A package
// must be self-contained, and there is no fallback to the receiver's local
// assets: a dangling name has to fail here rather than silently bind to a
// same-named local asset with different behavior.
func checkReferences(t *Tree, result *CheckResult) {
	for _, teamID := range sortedKeys(t.Teams) {
		team := t.Teams[teamID]
		for _, callName := range sortedKeys(team.Calls) {
			call := team.Calls[callName]
			if call.Type != "agent" {
				continue
			}
			if _, ok := t.Agents[call.AgentID]; !ok {
				result.Problems = append(result.Problems, Problem{
					Severity: SeverityError,
					Message: fmt.Sprintf(
						"team %q call %q references missing agent %q", teamID, callName, call.AgentID,
					),
				})
			}
		}
	}

	for _, agentName := range sortedKeys(t.Agents) {
		agent := t.Agents[agentName]
		for _, skillName := range agent.Skills {
			if _, ok := t.Skills[skillName]; !ok {
				result.Problems = append(result.Problems, Problem{
					Severity: SeverityError,
					Message:  fmt.Sprintf("agent %q references missing skill %q", agentName, skillName),
				})
			}
		}
		for _, ruleName := range agent.Rules {
			if !t.hasRule(ruleName) {
				result.Problems = append(result.Problems, Problem{
					Severity: SeverityError,
					Message:  fmt.Sprintf("agent %q references missing rule %q", agentName, ruleName),
				})
			}
		}
	}
}

// checkSandbox applies the package's declared limits to its contents.
func checkSandbox(m *Manifest, t *Tree, result *CheckResult) {
	if len(m.Sandbox.Tools) > 0 {
		allowed := make(map[string]struct{}, len(m.Sandbox.Tools))
		for _, tool := range m.Sandbox.Tools {
			allowed[tool] = struct{}{}
		}
		for _, agentName := range sortedKeys(t.Agents) {
			var trimmed []string
			for _, tool := range t.Agents[agentName].Tools.Builtin {
				if _, ok := allowed[tool]; !ok {
					trimmed = append(trimmed, tool)
				}
			}
			if len(trimmed) == 0 {
				continue
			}
			result.Problems = append(result.Problems, Problem{
				Severity: SeverityWarning,
				Message: fmt.Sprintf(
					"agent %q declares tools outside sandbox.tools and they will be removed: %s",
					agentName, strings.Join(trimmed, ", "),
				),
			})
		}
	}

	if m.Sandbox.Scripts == ScriptsDeny {
		for _, skillName := range sortedKeys(t.Skills) {
			scripts := t.Scripts[skillName]
			if len(scripts) == 0 {
				scripts = t.Skills[skillName].Scripts
			}
			if len(scripts) == 0 {
				continue
			}
			result.Problems = append(result.Problems, Problem{
				Severity: SeverityError,
				Message: fmt.Sprintf(
					"skill %q ships scripts but sandbox.scripts is deny: %s",
					skillName, strings.Join(scripts, ", "),
				),
			})
		}
	}
}

// collectModels gathers the model ids the agents ask for. An agent without a
// model is skipped: that is a configuration problem the loader reports, not a
// package dependency.
func collectModels(t *Tree) []string {
	seen := map[string]struct{}{}
	var models []string
	for _, agent := range t.Agents {
		id := strings.TrimSpace(agent.Model.Model)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	return models
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
