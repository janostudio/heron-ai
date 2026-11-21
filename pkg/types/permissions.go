package types

import (
	"fmt"
)

// PermissionDecision is one rung of the permission table.
//
// Three values, ordered: deny beats ask, ask beats allow.
type PermissionDecision string

const (
	PermissionAllow PermissionDecision = "allow"
	PermissionAsk   PermissionDecision = "ask"
	PermissionDeny  PermissionDecision = "deny"
)

// Rank orders the decisions so "strongest wins" can be a comparison. Unknown
// values rank below allow, so a typo can neither tighten nor loosen anything: it
// is reported by Validate and then ignored.
func (d PermissionDecision) Rank() int {
	switch d {
	case PermissionDeny:
		return 3
	case PermissionAsk:
		return 2
	case PermissionAllow:
		return 1
	default:
		return 0
	}
}

// Valid reports whether the value is one of the three known decisions.
func (d PermissionDecision) Valid() bool { return d.Rank() > 0 }

// PermissionsConfig is the permission posture of one engine runtime.
//
// It is global — `.agents/settings.json`'s `permissions` section — because it
// answers a question about the machine rather than about a task: may runs started
// here execute commands, change files, reach the network. Putting it per Flow or
// per Agent would ask the same question once per file and let the answers drift.
//
// Each field is named after the tool it governs, so a reader never has to guess
// what covers what. Only tools that can change something outside the conversation
// are listed: Read, Grep, Glob, Todo, State and the hand-off tools cannot, so they
// are not part of this decision.
//
// REPL is deliberately absent: it is not a capability, it is a program that calls
// the tools above. Whether a program may run is answered by the tools it touches
// (see the REPL section of docs/configuration/settings.md).
type PermissionsConfig struct {
	// Bash governs the Bash tool.
	Bash PermissionDecision `json:"bash,omitempty"`
	// Write governs the Write tool.
	Write PermissionDecision `json:"write,omitempty"`
	// WebSearch governs the WebSearch tool.
	WebSearch PermissionDecision `json:"websearch,omitempty"`
	// WebFetch governs the WebFetch tool.
	WebFetch PermissionDecision `json:"webfetch,omitempty"`
}

// governedTools is the set of fields above, in the order a diagnostic prints them.
// A tool outside this list keeps its own behaviour: the posture answers "may this
// runtime touch the machine", and reading a file from the workspace does not.
var governedTools = []string{"Bash", "Write", "WebSearch", "WebFetch"}

// GovernedTools returns the tools this posture governs, for diagnostics and for the
// sandbox's bindable set.
func GovernedTools() []string {
	out := make([]string, len(governedTools))
	copy(out, governedTools)
	return out
}

// Governed reports whether a tool is subject to the posture.
func Governed(tool string) bool {
	for _, name := range governedTools {
		if name == tool {
			return true
		}
	}
	return false
}

// Normalize fills the defaults: allow everywhere, and any unknown value becomes
// allow as well — Validate reports the typo, and refusing to run on a typo would
// turn a documentation mistake into an outage.
func (p PermissionsConfig) Normalize() PermissionsConfig {
	out := p
	for _, target := range []*PermissionDecision{&out.Bash, &out.Write, &out.WebSearch, &out.WebFetch} {
		if !target.Valid() {
			*target = PermissionAllow
		}
	}
	return out
}

// Validate reports every entry that will not be applied as written.
func (p PermissionsConfig) Validate() []string {
	var problems []string
	entries := []struct {
		name  string
		value PermissionDecision
	}{
		{"bash", p.Bash},
		{"write", p.Write},
		{"websearch", p.WebSearch},
		{"webfetch", p.WebFetch},
	}
	for _, entry := range entries {
		if entry.value != "" && !entry.value.Valid() {
			problems = append(problems, fmt.Sprintf(
				"permissions.%s: unknown decision %q (want allow, ask or deny)", entry.name, entry.value))
		}
	}
	return problems
}

// DecisionFor returns the posture that governs one tool.
func (p PermissionsConfig) DecisionFor(tool string) PermissionDecision {
	normalized := p.Normalize()
	switch tool {
	case "Bash":
		return normalized.Bash
	case "Write":
		return normalized.Write
	case "WebSearch":
		return normalized.WebSearch
	case "WebFetch":
		return normalized.WebFetch
	default:
		return PermissionAllow
	}
}

// DescribeTool renders one tool's posture for a human-readable reason, naming the
// configuration key rather than the tool so an operator can find the line.
func (p PermissionsConfig) DescribeTool(tool string) string {
	key := tool
	switch tool {
	case "Bash":
		key = "bash"
	case "Write":
		key = "write"
	case "WebSearch":
		key = "websearch"
	case "WebFetch":
		key = "webfetch"
	}
	return fmt.Sprintf("%s=%s", key, p.DecisionFor(tool))
}
