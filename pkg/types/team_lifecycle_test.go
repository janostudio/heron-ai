package types

import (
	"strings"
	"testing"
)

func TestNormalizeTeamLifecycle(t *testing.T) {
	cases := []struct {
		in   TeamLifecycleMode
		want TeamLifecycleMode
	}{
		{"", TeamLifecycleResident},
		{TeamLifecycleResident, TeamLifecycleResident},
		{TeamLifecyclePerTask, TeamLifecyclePerTask},
		{"unexpected", TeamLifecycleMode("unexpected")},
	}
	for _, tc := range cases {
		if got := NormalizeTeamLifecycle(tc.in); got != tc.want {
			t.Fatalf("NormalizeTeamLifecycle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTeamSessionEffectiveLifecycle(t *testing.T) {
	// Data written before the field existed has an empty lifecycle and must
	// keep behaving like the historical single session.
	if got := (TeamSession{}).EffectiveLifecycle(); got != TeamLifecycleResident {
		t.Fatalf("zero TeamSession lifecycle = %q, want resident", got)
	}
	session := TeamSession{Lifecycle: TeamLifecyclePerTask}
	if !session.IsPerTask() {
		t.Fatal("TeamSession with lifecycle per_task should report IsPerTask")
	}
	if (TeamSession{}).IsPerTask() {
		t.Fatal("zero TeamSession should not report IsPerTask")
	}
}

func TestFlowValidateTeamLifecycle(t *testing.T) {
	base := func(binding FlowTeamBinding) Flow {
		return Flow{
			ID:          "f",
			EntryTeamID: "entry",
			Teams: map[string]FlowTeamBinding{
				"entry": {ID: "entry", TeamID: "entry_team"},
				"dev":   binding,
			},
		}
	}

	t.Run("unset defaults to resident", func(t *testing.T) {
		flow := base(FlowTeamBinding{ID: "dev", TeamID: "dev_team"})
		if err := flow.Validate(); err != nil {
			t.Fatalf("unset lifecycle should validate: %v", err)
		}
	})

	t.Run("explicit resident", func(t *testing.T) {
		flow := base(FlowTeamBinding{ID: "dev", TeamID: "dev_team", Lifecycle: TeamLifecycleResident})
		if err := flow.Validate(); err != nil {
			t.Fatalf("resident lifecycle should validate: %v", err)
		}
	})

	t.Run("per_task requires user_message", func(t *testing.T) {
		flow := base(FlowTeamBinding{ID: "dev", TeamID: "dev_team", Lifecycle: TeamLifecyclePerTask})
		err := flow.Validate()
		if err == nil || !strings.Contains(err.Error(), "inputs.user_message") {
			t.Fatalf("want user_message error, got %v", err)
		}
	})

	t.Run("per_task with user_message is valid", func(t *testing.T) {
		flow := base(FlowTeamBinding{
			ID:        "dev",
			TeamID:    "dev_team",
			Lifecycle: TeamLifecyclePerTask,
			Inputs:    InputSpec{UserMessage: true},
		})
		if err := flow.Validate(); err != nil {
			t.Fatalf("per_task with user_message should validate: %v", err)
		}
	})

	t.Run("entry cannot be per_task", func(t *testing.T) {
		flow := Flow{
			ID:          "f",
			EntryTeamID: "entry",
			Teams: map[string]FlowTeamBinding{
				"entry": {
					ID:        "entry",
					TeamID:    "entry_team",
					Lifecycle: TeamLifecyclePerTask,
					Inputs:    InputSpec{UserMessage: true},
				},
			},
		}
		err := flow.Validate()
		if err == nil || !strings.Contains(err.Error(), "entry team") {
			t.Fatalf("want entry team error, got %v", err)
		}
	})

	t.Run("unknown lifecycle is rejected", func(t *testing.T) {
		flow := base(FlowTeamBinding{ID: "dev", TeamID: "dev_team", Lifecycle: "sometimes"})
		err := flow.Validate()
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("want unsupported lifecycle error, got %v", err)
		}
	})
}
