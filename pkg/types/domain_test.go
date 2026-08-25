package types

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFlowValidateAcceptsMixedMembers(t *testing.T) {
	flow := Flow{
		ID:          "code-fix",
		EntryTeamID: "default",
		Teams: map[string]FlowTeamBinding{
			"default": {
				ID:          "default",
				TeamID:      "default-team",
				Coordinator: true,
				CanActivate: []string{"diagnose"},
			},
			"diagnose": {
				ID:        "diagnose",
				TeamID:    "diagnose-team",
				DependsOn: []string{"default"},
			},
		},
	}

	teams := map[string]Team{
		"default-team": {
			ID: "default-team",
			Members: map[string]Member{
				"assistant": {
					ID:      "assistant",
					Type:    MemberSubagent,
					AgentID: "default-assistant",
				},
			},
		},
		"diagnose-team": {
			ID: "diagnose-team",
			Members: map[string]Member{
				"inspect": {
					ID:   "inspect",
					Type: MemberCommand,
					Command: &CommandSpec{
						Command: "go test ./...",
					},
				},
				"notify": {
					ID:   "notify",
					Type: MemberWebhook,
					Webhook: &WebhookSpec{
						URL:    "https://example.com/hooks/diagnose",
						Method: "POST",
					},
				},
			},
		},
	}

	if err := flow.ValidateWithTeams(teams); err != nil {
		t.Fatalf("expected valid flow, got %v", err)
	}
}

func TestFlowValidateRequiresExactlyOneCoordinator(t *testing.T) {
	tests := []struct {
		name  string
		teams map[string]FlowTeamBinding
	}{
		{
			name: "none",
			teams: map[string]FlowTeamBinding{
				"default": {ID: "default", TeamID: "default-team"},
			},
		},
		{
			name: "multiple",
			teams: map[string]FlowTeamBinding{
				"default": {ID: "default", TeamID: "default-team", Coordinator: true},
				"other":   {ID: "other", TeamID: "other-team", Coordinator: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (Flow{
				ID:          "flow",
				EntryTeamID: "default",
				Teams:       tt.teams,
			}).Validate()
			if err == nil || !strings.Contains(err.Error(), "exactly one coordinator") {
				t.Fatalf("expected coordinator validation error, got %v", err)
			}
		})
	}
}

func TestFlowValidateRejectsUnknownReferencesAndCycles(t *testing.T) {
	tests := []struct {
		name string
		flow Flow
		want string
	}{
		{
			name: "unknown activation",
			flow: Flow{
				ID:          "flow",
				EntryTeamID: "default",
				Teams: map[string]FlowTeamBinding{
					"default": {
						ID:          "default",
						TeamID:      "default-team",
						Coordinator: true,
						CanActivate: []string{"missing"},
					},
				},
			},
			want: "can_activate references unknown team",
		},
		{
			name: "team cycle",
			flow: Flow{
				ID:          "flow",
				EntryTeamID: "default",
				Teams: map[string]FlowTeamBinding{
					"default": {
						ID:          "default",
						TeamID:      "default-team",
						Coordinator: true,
						DependsOn:   []string{"worker"},
					},
					"worker": {
						ID:        "worker",
						TeamID:    "worker-team",
						DependsOn: []string{"default"},
					},
				},
			},
			want: "team dependency cycle detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.flow.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestTeamValidateRejectsUnknownMemberDependency(t *testing.T) {
	team := Team{
		ID: "review",
		Members: map[string]Member{
			"reviewer": {
				ID:        "reviewer",
				Type:      MemberSubagent,
				AgentID:   "reviewer-definition",
				DependsOn: []string{"missing"},
			},
		},
	}

	err := team.Validate()
	if err == nil || !strings.Contains(err.Error(), "depends on unknown call") {
		t.Fatalf("expected unknown member dependency error, got %v", err)
	}
}

func TestTeamValidateRejectsMemberDependencyCycle(t *testing.T) {
	team := Team{
		ID: "review",
		Members: map[string]Member{
			"first": {
				ID:        "first",
				Type:      MemberSubagent,
				AgentID:   "first-definition",
				DependsOn: []string{"second"},
			},
			"second": {
				ID:        "second",
				Type:      MemberSubagent,
				AgentID:   "second-definition",
				DependsOn: []string{"first"},
			},
		},
	}

	err := team.Validate()
	if err == nil || !strings.Contains(err.Error(), "call dependency cycle detected") {
		t.Fatalf("expected member dependency cycle error, got %v", err)
	}
}

func TestTeamValidateRejectsUnknownOutputMember(t *testing.T) {
	team := Team{
		ID: "review",
		Members: map[string]Member{
			"reviewer": {
				ID:      "reviewer",
				Type:    MemberSubagent,
				AgentID: "reviewer-definition",
			},
		},
		Output: OutputSpec{
			Records: []OutputBinding{
				{From: "missing", Record: "ReviewReport"},
			},
		},
	}

	err := team.Validate()
	if err == nil || !strings.Contains(err.Error(), "output references unknown call") {
		t.Fatalf("expected unknown output member error, got %v", err)
	}
}

func TestMemberValidateRejectsMixedOrInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name   string
		member Member
		want   string
	}{
		{
			name: "missing subagent definition",
			member: Member{
				ID:   "worker",
				Type: MemberSubagent,
			},
			want: "agent is required",
		},
		{
			name: "command with agent",
			member: Member{
				ID:      "test",
				Type:    MemberCommand,
				AgentID: "wrong",
				Command: &CommandSpec{Command: "go test ./..."},
			},
			want: "agent/webhook must be empty",
		},
		{
			name: "invalid webhook url",
			member: Member{
				ID:      "notify",
				Type:    MemberWebhook,
				Webhook: &WebhookSpec{URL: "not a url"},
			},
			want: "invalid url",
		},
		{
			name: "unknown type",
			member: Member{
				ID:   "worker",
				Type: MemberType("agent"),
			},
			want: "unsupported type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.member.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestMemberValidateRequiresIdempotencyKeyForIdempotentReplay(t *testing.T) {
	member := Member{
		ID:   "verify",
		Type: MemberCommand,
		Command: &CommandSpec{
			Command:      "git status --short",
			ReplayPolicy: ReplayIdempotent,
		},
	}
	err := member.Validate()
	if err == nil || !strings.Contains(err.Error(), "idempotency_key") {
		t.Fatalf("expected idempotency key validation error, got %v", err)
	}
}

func TestMemberValidateAcceptsIdempotentReplayWithKey(t *testing.T) {
	member := Member{
		ID:   "verify",
		Type: MemberCommand,
		Command: &CommandSpec{
			Command:        "git status --short",
			ReplayPolicy:   ReplayIdempotent,
			IdempotencyKey: "verify-${flow_turn_id}",
		},
	}
	if err := member.Validate(); err != nil {
		t.Fatalf("expected valid idempotent command, got %v", err)
	}
}

func TestNewConfigFormsDecode(t *testing.T) {
	const config = `
id: code-fix
entry: default
teams:
  default:
    team: default-team
    coordinator: true
    can_activate: [verify]
    inputs:
      user_message: true
  verify:
    team: verify-team
    depends_on: [default]
    inputs:
      - from: default
        record: DiagnosisReport
teams_definitions:
  ignored: true
`

	var flow Flow
	if err := yaml.Unmarshal([]byte(config), &flow); err != nil {
		t.Fatalf("decode flow: %v", err)
	}
	flow.Normalize()

	if flow.Teams["default"].ID != "default" {
		t.Fatalf("expected flow-local team ID to be filled, got %q", flow.Teams["default"].ID)
	}
	if !flow.Teams["default"].Inputs.UserMessage {
		t.Fatal("expected user_message input to decode")
	}
	if len(flow.Teams["verify"].Inputs.Records) != 1 {
		t.Fatalf("expected one explicit input binding, got %d", len(flow.Teams["verify"].Inputs.Records))
	}

	var team Team
	if err := yaml.Unmarshal([]byte(`
id: verify-team
members:
  unit_test:
    type: command
    command: "go test ./..."
    output:
      record: UnitTestResult
  notify:
    type: webhook
    url: "https://example.com/api/comment"
    method: POST
`), &team); err != nil {
		t.Fatalf("decode team: %v", err)
	}
	team.Normalize()

	if err := team.Validate(); err != nil {
		t.Fatalf("expected decoded team to be valid: %v", err)
	}
	if team.Members["unit_test"].ID != "unit_test" {
		t.Fatalf("expected member ID to be filled, got %q", team.Members["unit_test"].ID)
	}
	if team.Members["notify"].Webhook == nil {
		t.Fatal("expected direct webhook URL to be decoded")
	}
}
