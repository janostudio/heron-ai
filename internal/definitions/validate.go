package definitions

import (
	"fmt"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// This file holds the checks the config loader does NOT already make.
//
// LoadDefinitions runs the full chain — Flow.ValidateWithTeams,
// validateAgentCallDefinitions, validateAgentSupportDefinitions,
// validateSkillScripts — against the candidate staging tree, and that chain is
// the authority on whether a tree loads. What it cannot check is anything that
// is only wrong RELATIVE TO THE CHANGE we are making: "this second coordinator
// was fine before and is the new problem", "this binding key was free and is
// not any more". Those live here as small pure functions over the candidate
// typed structs, so they are unit-testable without a filesystem and so the
// writer never has to re-derive config semantics.

// checkSingleCoordinator rejects a candidate flow with zero or several
// coordinator teams.
//
// Flow.Validate (pkg/types/domain.go) enforces this too, so a violation is
// reported twice. It is checked here anyway because the message the loader
// produces is wrapped several layers deep ("validate definitions: flow %q:
// exactly one coordinator team is required, got 2"), and this is the error a
// model is most likely to hit when it asks for a second coordinator by
// accident. The dedicated text is what tells it to drop the flag instead of
// retrying the same spec.
func checkSingleCoordinator(flow types.Flow) error {
	count := 0
	for _, binding := range flow.Teams {
		if binding.Coordinator {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf(
			"flow %q: exactly one coordinator team is required, got %d",
			flow.ID,
			count,
		)
	}
	return nil
}

// checkAgentNameAvailable rejects creating an agent whose name is taken.
//
// The loader catches this as "duplicate agent definition", but only once the
// file exists — and for a create request the file must not exist at all, so the
// failure has to be raised from the snapshot before a single byte is written.
func checkAgentNameAvailable(agents map[string]types.AgentConfig, name string) error {
	if _, exists := agents[name]; exists {
		return fmt.Errorf(
			"agent %q already exists: use mode upsert to update it",
			name,
		)
	}
	return nil
}

// checkTeamNameAvailable is checkAgentNameAvailable for teams.
func checkTeamNameAvailable(teams map[string]types.Team, name string) error {
	if _, exists := teams[name]; exists {
		return fmt.Errorf(
			"team %q already exists: use mode upsert to update it",
			name,
		)
	}
	return nil
}

// checkFlowBindingKeyAvailable rejects a new flow team binding whose flow-local
// key is already used.
//
// Two different things can go wrong with a duplicate key and only one is
// obvious. The loud case is reusing an existing key for a DIFFERENT team, which
// silently repoints a live binding. The quiet case is binding the SAME team
// definition under a second name, which loads fine and looks harmless but makes
// the flow ambiguous: Flow.Validate's coordinator count, can_activate targets
// and on_proceed routing all address teams by key, so the same definition now
// has two identities and a route written against one of them will not match the
// other. Both are rejected; the fix is always to pick a fresh key.
func checkFlowBindingKeyAvailable(flow types.Flow, key, teamID string) error {
	existing, exists := flow.Teams[key]
	if !exists {
		return nil
	}
	if existing.TeamID == teamID {
		return fmt.Errorf(
			"flow %q: team key %q already binds team %q",
			flow.ID,
			key,
			teamID,
		)
	}
	return fmt.Errorf(
		"flow %q: team key %q is already taken by team %q",
		flow.ID,
		key,
		existing.TeamID,
	)
}

// checkCallIDsMatchKeys rejects a team whose call map key and call id disagree.
//
// Team.Validate enforces this, but the writer synthesizes the id from the key
// itself, so a mismatch here means a bug in the spec decoding rather than user
// input. Catching it before staging turns "the tree I built is internally
// inconsistent" into a message that names the offending call.
func checkCallIDsMatchKeys(team types.Team) error {
	keys := make([]string, 0, len(team.Calls))
	for key := range team.Calls {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if err := checkCallIDMatchesKey(team.ID, key, team.Calls[key]); err != nil {
			return err
		}
	}
	return nil
}

func checkCallIDMatchesKey(teamID, key string, call types.Call) error {
	if call.ID != key {
		return fmt.Errorf(
			"team %q: call key %q does not match call id %q",
			teamID,
			key,
			call.ID,
		)
	}
	return nil
}

// checkNoRename rejects an upsert whose spec renames the target.
//
// Renaming is not a merge: it leaves the old file behind under its old name, so
// the tree ends up with two definitions and every existing reference now points
// at a copy that will never be updated again. An explicit create plus a cleanup
// makes the caller decide what happens to the old name and its references.
func checkNoRename(existingName, requestedName, kind string) error {
	if strings.TrimSpace(requestedName) == "" {
		return nil
	}
	if requestedName == existingName {
		return nil
	}
	return fmt.Errorf(
		"cannot rename %s %q to %q: creating a new %s is an explicit create, and renaming leaves the old definition behind",
		kind,
		existingName,
		requestedName,
		kind,
	)
}

// checkAgentSpecSupport rejects an agent referencing skills or knowledge that
// do not exist in the candidate tree.
//
// LoadDefinitions only validates skill references (and only for agents a flow
// actually reaches) but never knowledge: agent.Knowledge is an allowlist of
// knowledge entry IDs, and an unknown ID silently filters everything out, so a
// typo would produce an agent that answers with no knowledge at all and no
// error anywhere. Rules are deliberately NOT checked — the loader itself stays
// permissive about rules for flat agent files, and mirroring a looser rule here
// would reject specs the engine happily runs.
func checkAgentSpecSupport(
	agent types.AgentConfig,
	skills map[string]types.Skill,
	knowledge map[string]struct{},
) error {
	for _, name := range agent.Skills {
		if _, ok := skills[name]; !ok {
			return fmt.Errorf("agent %q references missing skill %q", agent.Name, name)
		}
	}
	for _, id := range agent.Knowledge {
		if len(knowledge) == 0 {
			// No knowledge directory at all: nothing can be validated, and
			// failing here would make knowledge unusable in trees that keep
			// their entries somewhere the loader cannot see.
			break
		}
		if _, ok := knowledge[id]; !ok {
			return fmt.Errorf("agent %q references missing knowledge %q", agent.Name, id)
		}
	}
	return nil
}

// checkCallDependencies is a defensive pass over a candidate team's call graph.
//
// Team.Validate already walks dependencies, so this adds no new rule; it exists
// so a team assembled by the writer (calls merged by name across two specs) is
// checked by the same function the tests call directly, instead of the check
// being reachable only through a filesystem round trip.
func checkCallDependencies(team types.Team) error {
	for key := range team.Calls {
		if err := checkCallDependencyTargets(team, key); err != nil {
			return err
		}
	}
	return nil
}

func checkCallDependencyTargets(team types.Team, key string) error {
	call := team.Calls[key]
	for _, dependency := range call.DependsOn {
		if _, ok := team.Calls[dependency]; !ok {
			return fmt.Errorf(
				"team %q: call %q depends on unknown call %q",
				team.ID,
				key,
				dependency,
			)
		}
	}
	return nil
}

// checkUnknownAgentSpecKeys is a readability guard for generated agent files:
// it reports frontmatter keys with no field in types.AgentConfig.
//
// Serialization round-trips through types.AgentConfig, so an unrecognised key is
// silently dropped by yaml rather than rejected — which is exactly the problem.
// A model that writes `temperature: 0.2` at the top level instead of under
// `model` would get a file that looks configured and an agent that behaves as
// if nothing was set. Naming the key beats that silence.
func checkUnknownAgentSpecKeys(spec map[string]any) error {
	known := map[string]struct{}{
		"name": {}, "persona": {}, "model": {}, "tools": {}, "skills": {},
		"knowledge": {}, "rules": {}, "loop": {}, "context": {}, "budget": {},
		"completion": {}, "structured_output": {}, "hitl": {}, "hooks": {},
		"workspace": {},
	}
	var unknown []string
	for key := range spec {
		if _, ok := known[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("agent spec: unsupported field(s) %s", strings.Join(unknown, ", "))
}
