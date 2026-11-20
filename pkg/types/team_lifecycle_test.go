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
		{"", TeamLifecycleShared},
		{TeamLifecycleShared, TeamLifecycleShared},
		{TeamLifecycleIsolated, TeamLifecycleIsolated},
		{"unexpected", TeamLifecycleMode("unexpected")},
	}
	for _, tc := range cases {
		if got := NormalizeTeamLifecycle(tc.in); got != tc.want {
			t.Fatalf("NormalizeTeamLifecycle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTeamSessionEffectiveLifecycle(t *testing.T) {
	// An unset lifecycle means one shared session.
	if got := (TeamSession{}).EffectiveLifecycle(); got != TeamLifecycleShared {
		t.Fatalf("zero TeamSession lifecycle = %q, want shared", got)
	}
	session := TeamSession{Lifecycle: TeamLifecycleIsolated}
	if !session.IsIsolated() {
		t.Fatal("TeamSession with lifecycle isolated should report IsIsolated")
	}
	if (TeamSession{}).IsIsolated() {
		t.Fatal("zero TeamSession should not report IsIsolated")
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

	t.Run("unset defaults to shared", func(t *testing.T) {
		flow := base(FlowTeamBinding{ID: "dev", TeamID: "dev_team"})
		if err := flow.Validate(); err != nil {
			t.Fatalf("unset lifecycle should validate: %v", err)
		}
	})

	t.Run("explicit shared", func(t *testing.T) {
		flow := base(FlowTeamBinding{ID: "dev", TeamID: "dev_team", Lifecycle: TeamLifecycleShared})
		if err := flow.Validate(); err != nil {
			t.Fatalf("shared lifecycle should validate: %v", err)
		}
	})

	t.Run("isolated requires user_message", func(t *testing.T) {
		flow := base(FlowTeamBinding{ID: "dev", TeamID: "dev_team", Lifecycle: TeamLifecycleIsolated})
		err := flow.Validate()
		if err == nil || !strings.Contains(err.Error(), "inputs.user_message") {
			t.Fatalf("want user_message error, got %v", err)
		}
	})

	t.Run("isolated with user_message is valid", func(t *testing.T) {
		flow := base(FlowTeamBinding{
			ID:        "dev",
			TeamID:    "dev_team",
			Lifecycle: TeamLifecycleIsolated,
			Inputs:    InputSpec{UserMessage: true},
		})
		if err := flow.Validate(); err != nil {
			t.Fatalf("isolated with user_message should validate: %v", err)
		}
	})

	t.Run("entry cannot be isolated", func(t *testing.T) {
		flow := Flow{
			ID:          "f",
			EntryTeamID: "entry",
			Teams: map[string]FlowTeamBinding{
				"entry": {
					ID:        "entry",
					TeamID:    "entry_team",
					Lifecycle: TeamLifecycleIsolated,
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
