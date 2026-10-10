package call

import (
	"encoding/json"
	"fmt"
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
			got, err := resolveTemplate(tt.input, req)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveTemplateRecords(t *testing.T) {
	req := templateRequest()
	req.Records = []types.SharedRecord{
		{Name: "WorktreeTarget", RecordID: "rec-1", Summary: "genie",
			Data: map[string]any{"repo": "genie", "branch": "feat/134", "count": float64(3)}},
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"summary shorthand", "${records.WorktreeTarget}", "genie"},
		{"explicit summary", "${records.WorktreeTarget.Summary}", "genie"},
		{"data string", "${records.WorktreeTarget.Data.repo}", "genie"},
		{"data branch", "${records.WorktreeTarget.Data.branch}", "feat/134"},
		{"data number", "${records.WorktreeTarget.Data.count}", "3"},
		{"record id", "${records.WorktreeTarget.RecordID}", "rec-1"},
		{"mixed with identity", "${records.WorktreeTarget.Data.repo}/${call_id}", "genie/answer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTemplate(tt.input, req)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveTemplateRecordsMissingIsError(t *testing.T) {
	req := templateRequest()
	req.Records = []types.SharedRecord{
		{Name: "WorktreeTarget", Data: map[string]any{"repo": "genie"}},
	}

	cases := []struct {
		name  string
		input string
	}{
		{"unknown record", "${records.Nope.Data.x}"},
		{"unknown data field", "${records.WorktreeTarget.Data.missing}"},
		{"unknown fixed field", "${records.WorktreeTarget.Nope}"},
		{"malformed deep", "${records.WorktreeTarget.Data.a.b}"},
		{"unterminated", "${records.WorktreeTarget.Data.x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveTemplate(tc.input, req)
			assert.Error(t, err, "a missing record reference must fail, not expand to empty")
		})
	}
}

func TestResolveTemplateRecordsLastWins(t *testing.T) {
	req := templateRequest()
	req.Records = []types.SharedRecord{
		{Name: "R", Summary: "old"},
		{Name: "R", Summary: "new"},
	}
	got, err := resolveTemplate("${records.R}", req)
	assert.NoError(t, err)
	assert.Equal(t, "new", got)
}

func TestResolveCommandArgs(t *testing.T) {
	req := templateRequest()
	req.Records = []types.SharedRecord{
		{Name: "Target", Data: map[string]any{"repo": "genie"}},
	}
	req.Call.Command = &types.CommandSpec{
		Args: []string{"acquire", "${records.Target.Data.repo}", "${call_id}"},
	}
	args, err := resolveCommandArgs(req)
	assert.NoError(t, err)
	assert.Equal(t, []string{"acquire", "genie", "answer"}, args)
}

func TestResolveCommandArgsEmpty(t *testing.T) {
	req := templateRequest()
	req.Call.Command = &types.CommandSpec{}
	args, err := resolveCommandArgs(req)
	assert.NoError(t, err)
	assert.Nil(t, args)
}

func TestCommandEnv(t *testing.T) {
	req := templateRequest()
	req.Call.Command = &types.CommandSpec{
		Env: map[string]string{
			"FLOW": "${flow_session_id}",
			"CALL": "${call_id}",
		},
	}

	env, err := commandEnv(req)
	assert.NoError(t, err)

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

func TestCommandEnvRecordMissingFails(t *testing.T) {
	req := templateRequest()
	req.Call.Command = &types.CommandSpec{
		Env: map[string]string{"AUTO_REPO": "${records.Target.Data.repo}"},
	}
	_, err := commandEnv(req)
	assert.Error(t, err, "an unresolvable record in env must fail the call, not send an empty var")
}

func TestCommandEnvIdempotencyKey(t *testing.T) {
	req := templateRequest()
	req.Call.Command = &types.CommandSpec{
		IdempotencyKey: "key-${call_id}",
	}

	env, err := commandEnv(req)
	assert.NoError(t, err)

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

	env, err := commandEnv(req)
	assert.NoError(t, err)

	// commandEnv always appends os.Environ() first.
	assert.GreaterOrEqual(t, len(env), len(os.Environ()))
}

func TestStringifyRecordValueScalars(t *testing.T) {
	// Regression protection: scalar behaviour must not change.
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string", "hello", "hello"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"float integral", float64(134288), "134288"},
		{"float fractional", 1.5, "1.5"},
		{"int", 42, "42"},
		{"int64", int64(7), "7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, stringifyRecordValue(tc.in))
		})
	}
}

func TestStringifyRecordValueStructures(t *testing.T) {
	// Non-scalar values must be rendered as their original JSON shape,
	// never Go's fmt.Sprint debug format (map[...] / [1 2]).
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"map", map[string]any{"a": float64(1)}, `{"a":1}`},
		{"slice", []any{float64(1), float64(2)}, `[1,2]`},
		{
			"nested",
			map[string]any{
				"candidates": []any{
					map[string]any{"number": float64(134288), "repo": "genie/genie"},
				},
				"notes": "x",
			},
			`{"candidates":[{"number":134288,"repo":"genie/genie"}],"notes":"x"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stringifyRecordValue(tc.in)
			assert.Equal(t, tc.want, got)
			assert.True(t, json.Valid([]byte(got)), "output must be valid JSON, got %q", got)
		})
	}
}

func TestStringifyRecordValueEscapesSpecialChars(t *testing.T) {
	in := map[string]any{"quote": `he said "hi"`, "newline": "a\nb", "cn": "中文"}
	got := stringifyRecordValue(in)
	assert.True(t, json.Valid([]byte(got)), "output must be valid JSON, got %q", got)

	var decoded map[string]string
	assert.NoError(t, json.Unmarshal([]byte(got), &decoded))
	assert.Equal(t, `he said "hi"`, decoded["quote"])
	assert.Equal(t, "a\nb", decoded["newline"])
	assert.Equal(t, "中文", decoded["cn"])
}

func TestStringifyRecordValueFallsBackOnMarshalError(t *testing.T) {
	// A channel cannot be marshalled; fall back to fmt.Sprint without panicking.
	ch := make(chan int)
	assert.NotPanics(t, func() {
		assert.Equal(t, fmt.Sprint(ch), stringifyRecordValue(ch))
	})
}

func TestCommandEnvPassesNestedRecordAsJSON(t *testing.T) {
	// End-to-end through the real template path (commandEnv -> resolveTemplate
	// -> resolveRecordPlaceholder -> stringifyRecordValue): a config that does
	// env: AUTO_QUERY: "${records.DiagnosisReport.Data.parsed}" must receive
	// valid JSON, not Go's fmt.Sprint format (map[...]).
	req := templateRequest()
	req.Call.Command = &types.CommandSpec{Env: map[string]string{
		"AUTO_QUERY": "${records.DiagnosisReport.Data.parsed}",
	}}
	req.Records = []types.SharedRecord{
		{
			Name: "DiagnosisReport",
			Data: map[string]any{
				"reply": "done",
				"parsed": map[string]any{
					"candidates": []any{
						map[string]any{"number": float64(134288), "repo": "genie/genie"},
					},
					"notes": "x",
				},
			},
		},
	}

	env, err := commandEnv(req)
	assert.NoError(t, err)

	var got string
	for _, entry := range env {
		if strings.HasPrefix(entry, "AUTO_QUERY=") {
			got = strings.TrimPrefix(entry, "AUTO_QUERY=")
		}
	}
	want := `{"candidates":[{"number":134288,"repo":"genie/genie"}],"notes":"x"}`
	assert.Equal(t, want, got)
	assert.True(t, json.Valid([]byte(got)), "env value must be valid JSON, got %q", got)

	// The whole point: a downstream tool (jq) can parse it.
	var decoded struct {
		Candidates []struct {
			Number int    `json:"number"`
			Repo   string `json:"repo"`
		} `json:"candidates"`
	}
	assert.NoError(t, json.Unmarshal([]byte(got), &decoded))
	assert.Len(t, decoded.Candidates, 1)
	assert.Equal(t, 134288, decoded.Candidates[0].Number)
	assert.Equal(t, "genie/genie", decoded.Candidates[0].Repo)
}
