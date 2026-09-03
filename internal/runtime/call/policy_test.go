package call

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func templateRequest() types.CallRequest {
	return types.CallRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "team-1"},
		Call:        types.Call{ID: "answer"},
		CallTurnID:  "ct-1",
		Attempt:     3,
		RecoveryOf:  "prev-ct",
	}
}

func TestResolveTemplate(t *testing.T) {
	req := templateRequest()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"flow session id", "sid=${flow_session_id}", "sid=fs-1"},
		{"flow turn id", "ft=${flow_turn_id}", "ft=ft-1"},
		{"team id", "team=${team_id}", "team=team-1"},
		{"team turn id", "tt=${team_turn_id}", "tt=tt-1"},
		{"call id", "call=${call_id}", "call=answer"},
		{"call turn id", "ct=${call_turn_id}", "ct=ct-1"},
		{"attempt", "a=${attempt}", "a=3"},
		{"recovery of", "rec=${recovery_of}", "rec=prev-ct"},
		{"all placeholders replaced",
			"${flow_session_id}/${team_id}/${call_id}/${attempt}",
			"fs-1/team-1/answer/3"},
		{"no placeholder", "plain text", "plain text"},
		{"empty value", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resolveTemplate(tt.input, req))
		})
	}
}

func TestCommandEnv(t *testing.T) {
	req := templateRequest()
	req.Call.Command = &types.CommandSpec{
		Env: map[string]string{
			"FLOW": "${flow_session_id}",
			"CALL": "${call_id}",
		},
	}

	env := commandEnv(req)

	// env is os.Environ() + our entries appended, so the custom entries are at
	// the tail. Find the exact "KEY=VALUE" strings we injected.
	var foundFlow, foundCall bool
	for _, entry := range env {
		switch entry {
		case "FLOW=fs-1":
			foundFlow = true
		case "CALL=answer":
			foundCall = true
		}
	}
	assert.True(t, foundFlow, "expected FLOW=fs-1 in env")
	assert.True(t, foundCall, "expected CALL=answer in env")
	// No idempotency key configured, so no HERON_IDEMPOTENCY_KEY entry.
	for _, entry := range env {
		assert.False(t, strings.HasPrefix(entry, "HERON_IDEMPOTENCY_KEY="),
			"unexpected idempotency key entry %q", entry)
	}
}

func TestCommandEnvIdempotencyKey(t *testing.T) {
	req := templateRequest()
	req.Call.Command = &types.CommandSpec{
		IdempotencyKey: "key-${call_id}",
	}

	env := commandEnv(req)

	var found bool
	for _, entry := range env {
		if entry == "HERON_IDEMPOTENCY_KEY=key-answer" {
			found = true
		}
	}
	assert.True(t, found, "expected HERON_IDEMPOTENCY_KEY=key-answer in env")
}

func TestCommandEnvRespectsOsEnviron(t *testing.T) {
	req := templateRequest()
	req.Call.Command = &types.CommandSpec{Env: map[string]string{}}

	env := commandEnv(req)

	// commandEnv always appends os.Environ() first.
	assert.GreaterOrEqual(t, len(env), len(os.Environ()))
}
