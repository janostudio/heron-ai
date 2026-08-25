package types

import (
	"fmt"
	"strings"
)

// Flow is the V1 Flow definition. It owns the available Team definitions and
// identifies the entry Team. Flow execution state belongs to FlowSession.
type Flow struct {
	ID          string                     `yaml:"id" json:"id"`
	EntryTeamID string                     `yaml:"entry" json:"entry"`
	Teams       map[string]FlowTeamBinding `yaml:"teams" json:"teams"`
}

// Normalize fills Flow-local Team names from their map keys. The config
// loader should call this immediately after decoding a Flow.
func (f *Flow) Normalize() {
	for name, binding := range f.Teams {
		if binding.ID == "" {
			binding.ID = name
			f.Teams[name] = binding
		}
	}
}

// FlowTeamBinding binds a Team definition into a Flow and gives it a role in
// this Flow. The map key is the Flow-local Team name; TeamID points to the
// reusable Team definition under .agents/teams/.
type FlowTeamBinding struct {
	ID          string    `yaml:"-" json:"id"`
	TeamID      string    `yaml:"team" json:"team"`
	Coordinator bool      `yaml:"coordinator,omitempty" json:"coordinator,omitempty"`
	CanActivate []string  `yaml:"can_activate,omitempty" json:"can_activate,omitempty"`
	DependsOn   []string  `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Inputs      InputSpec `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	OnProceed   *Route    `yaml:"on_proceed,omitempty" json:"on_proceed,omitempty"`
}

// Team is the V1 Team definition. A Team directly coordinates Agent,
// Command, and Webhook calls. Members is retained as an internal/legacy
// decoding alias; new configuration should use Calls.
type Team struct {
	ID      string            `yaml:"id" json:"id"`
	Members map[string]Member `yaml:"members,omitempty" json:"-"`
	Calls   map[string]Member `yaml:"calls,omitempty" json:"calls,omitempty"`
	Inputs  InputSpec         `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Output  OutputSpec        `yaml:"output,omitempty" json:"output,omitempty"`
	Outputs OutputSpec        `yaml:"outputs,omitempty" json:"outputs,omitempty"`
	Memory  MemoryConfig      `yaml:"memory,omitempty" json:"memory,omitempty"`
	Goal    string            `yaml:"goal,omitempty" json:"goal,omitempty"`
}

// MemoryConfig is the small core-facing configuration slot for the optional
// Memory Extension.
type MemoryConfig struct {
	Enabled       bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	MaxItems      int  `yaml:"max_items,omitempty" json:"max_items,omitempty"`
	MaxChars      int  `yaml:"max_chars,omitempty" json:"max_chars,omitempty"`
	InjectSummary bool `yaml:"inject_summary,omitempty" json:"inject_summary,omitempty"`
}

func (o OutputSpec) IsZero() bool {
	return o.From == "" && o.Record == "" && len(o.Records) == 0 && !o.Publish
}

// Normalize fills call IDs from the Team call map keys. The config loader
// should call this immediately after decoding a Team. Internally the
// scheduler still uses Members so the executor abstraction stays private.
func (t *Team) Normalize() {
	if len(t.Members) == 0 && len(t.Calls) > 0 {
		t.Members = t.Calls
	}
	if len(t.Calls) == 0 && len(t.Members) > 0 {
		t.Calls = t.Members
	}
	for name, member := range t.Members {
		if member.ID == "" {
			member.ID = name
			t.Members[name] = member
		}
	}
	if t.Output.IsZero() && !t.Outputs.IsZero() {
		t.Output = t.Outputs
	}
}

// Validate checks a Team and all of its members without requiring a complete
// Flow graph.
func (t Team) Validate() error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("team id is required")
	}
	t.Normalize()

	memberNames := make(map[string]struct{}, len(t.Members))
	for name, member := range t.Members {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("team %q: call name is required", t.ID)
		}
		if member.ID != name {
			return fmt.Errorf("team %q: call key %q does not match call id %q", t.ID, name, member.ID)
		}
		if err := member.Validate(); err != nil {
			return fmt.Errorf("team %q: %w", t.ID, err)
		}
		memberNames[name] = struct{}{}
	}

	for _, member := range t.Members {
		for _, dependency := range member.DependsOn {
			if _, ok := memberNames[dependency]; !ok {
				return fmt.Errorf("team %q: call %q depends on unknown call %q", t.ID, member.ID, dependency)
			}
			if dependency == member.ID {
				return fmt.Errorf("team %q: call %q cannot depend on itself", t.ID, member.ID)
			}
		}
	}
	if err := validateAcyclicMemberDependencies(t.Members); err != nil {
		return fmt.Errorf("team %q: %w", t.ID, err)
	}
	if err := validateTeamOutputs(t); err != nil {
		return fmt.Errorf("team %q: %w", t.ID, err)
	}

	return nil
}

func validateTeamOutputs(t Team) error {
	validateBinding := func(binding OutputBinding) error {
		if strings.TrimSpace(binding.From) == "" {
			return fmt.Errorf("output record %q: from call is required", binding.Record)
		}
		if _, ok := t.Members[binding.From]; !ok {
			return fmt.Errorf("output references unknown call %q", binding.From)
		}
		if strings.TrimSpace(binding.Record) == "" {
			return fmt.Errorf("output from call %q: record is required", binding.From)
		}
		return nil
	}

	if !t.Output.IsZero() && len(t.Output.Records) == 0 {
		if strings.TrimSpace(t.Output.From) == "" {
			return fmt.Errorf("output: from call is required")
		}
		if _, ok := t.Members[t.Output.From]; !ok {
			return fmt.Errorf("output references unknown call %q", t.Output.From)
		}
		if strings.TrimSpace(t.Output.Record) == "" {
			return fmt.Errorf("output from call %q: record is required", t.Output.From)
		}
	}
	if !t.Outputs.IsZero() && len(t.Outputs.Records) == 0 {
		if strings.TrimSpace(t.Outputs.From) == "" {
			return fmt.Errorf("outputs: from call is required")
		}
		if _, ok := t.Members[t.Outputs.From]; !ok {
			return fmt.Errorf("outputs references unknown call %q", t.Outputs.From)
		}
		if strings.TrimSpace(t.Outputs.Record) == "" {
			return fmt.Errorf("outputs from call %q: record is required", t.Outputs.From)
		}
	}

	for _, binding := range t.Output.Records {
		if err := validateBinding(binding); err != nil {
			return err
		}
	}
	for _, binding := range t.Outputs.Records {
		if err := validateBinding(binding); err != nil {
			return err
		}
	}
	return nil
}

func validateAcyclicMemberDependencies(members map[string]Member) error {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)

	states := make(map[string]int, len(members))
	var visit func(string) error
	visit = func(memberID string) error {
		switch states[memberID] {
		case visiting:
			return fmt.Errorf("call dependency cycle detected at %q", memberID)
		case visited:
			return nil
		}

		states[memberID] = visiting
		for _, dependency := range members[memberID].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		states[memberID] = visited
		return nil
	}

	for memberID := range members {
		if err := visit(memberID); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks the graph-independent invariants of a Flow.
func (f Flow) Validate() error {
	if strings.TrimSpace(f.ID) == "" {
		return fmt.Errorf("flow id is required")
	}
	if strings.TrimSpace(f.EntryTeamID) == "" {
		return fmt.Errorf("flow %q: entry team is required", f.ID)
	}
	if len(f.Teams) == 0 {
		return fmt.Errorf("flow %q: at least one team is required", f.ID)
	}
	if _, ok := f.Teams[f.EntryTeamID]; !ok {
		return fmt.Errorf("flow %q: entry team %q not found", f.ID, f.EntryTeamID)
	}

	coordinators := 0
	for name, binding := range f.Teams {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("flow %q: team name is required", f.ID)
		}
		if binding.ID != "" && name != binding.ID {
			return fmt.Errorf("flow %q: team key %q does not match binding id %q", f.ID, name, binding.ID)
		}
		if binding.Coordinator {
			coordinators++
		}
	}
	if coordinators != 1 {
		return fmt.Errorf("flow %q: exactly one coordinator team is required, got %d", f.ID, coordinators)
	}

	for name, team := range f.Teams {
		for _, dependency := range team.DependsOn {
			if _, ok := f.Teams[dependency]; !ok {
				return fmt.Errorf("flow %q: team %q depends on unknown team %q", f.ID, name, dependency)
			}
			if dependency == name {
				return fmt.Errorf("flow %q: team %q cannot depend on itself", f.ID, name)
			}
		}
		for _, target := range team.CanActivate {
			if _, ok := f.Teams[target]; !ok {
				return fmt.Errorf("flow %q: team %q can_activate references unknown team %q", f.ID, name, target)
			}
		}
		if team.OnProceed != nil {
			for _, target := range team.OnProceed.Teams {
				if _, ok := f.Teams[target]; !ok {
					return fmt.Errorf("flow %q: team %q on_proceed references unknown team %q", f.ID, name, target)
				}
			}
		}
	}
	if err := validateAcyclicTeamDependencies(f.Teams); err != nil {
		return fmt.Errorf("flow %q: %w", f.ID, err)
	}

	return nil
}

// ValidateWithTeams validates a Flow together with its reusable Team
// definitions and their member dependencies.
func (f Flow) ValidateWithTeams(teams map[string]Team) error {
	if err := f.Validate(); err != nil {
		return err
	}

	for flowTeamName, binding := range f.Teams {
		if strings.TrimSpace(binding.TeamID) == "" {
			return fmt.Errorf("flow %q: team %q definition reference is required", f.ID, flowTeamName)
		}
		team, ok := teams[binding.TeamID]
		if !ok {
			return fmt.Errorf("flow %q: team definition %q not found for %q", f.ID, binding.TeamID, flowTeamName)
		}
		if err := team.Validate(); err != nil {
			return fmt.Errorf("flow %q: %w", f.ID, err)
		}
	}
	return nil
}

func validateAcyclicTeamDependencies(teams map[string]FlowTeamBinding) error {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)

	states := make(map[string]int, len(teams))
	var visit func(string) error
	visit = func(teamID string) error {
		switch states[teamID] {
		case visiting:
			return fmt.Errorf("team dependency cycle detected at %q", teamID)
		case visited:
			return nil
		}

		states[teamID] = visiting
		for _, dependency := range teams[teamID].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		states[teamID] = visited
		return nil
	}

	for teamID := range teams {
		if err := visit(teamID); err != nil {
			return err
		}
	}
	return nil
}
