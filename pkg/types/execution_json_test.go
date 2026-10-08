package types

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// callResultWireFields is the published payload contract. CallResult is the
// fact source external consumers read token usage from, so this list is
// frozen: adding a field is fine, renaming one is a breaking change.
var callResultWireFields = []string{
	"Status",
	"Reply",
	"CallTurnID",
	"AgentID",
	"Records",
	"Next",
	"Usage",
	"Requests",
	"WorkspaceOps",
	"ToolCalls",
	"Error",
	"CheckpointID",
	"Checkpoint",
	"TaskID",
	"PendingApproval",
	"Approval",
}

func TestCallResultMarshalUsesPublishedFieldNames(t *testing.T) {
	data, err := json.Marshal(CallResult{})
	require.NoError(t, err)

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &fields))

	got := make([]string, 0, len(fields))
	for name := range fields {
		got = append(got, name)
	}
	sort.Strings(got)

	want := append([]string(nil), callResultWireFields...)
	sort.Strings(want)
	require.Equal(t, want, got, "CallResult wire fields changed; this breaks external jsonl parsers")
}

func TestCallResultMarshalKeepsEmptyFields(t *testing.T) {
	// No omitempty: consumers can rely on every field being present, even
	// when null. This matches every session jsonl written so far.
	data, err := json.Marshal(CallResult{Status: TurnCompleted})
	require.NoError(t, err)

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &fields))
	require.Len(t, fields, len(callResultWireFields))
	require.JSONEq(t, `"completed"`, string(fields["Status"]))
	require.Equal(t, "null", string(fields["Checkpoint"]))
}

func TestCallResultLeafTypesStaySnakeCase(t *testing.T) {
	// The mixed shape is intentional: CallResult is PascalCase, the leaf
	// types nested in it are snake_case. Lock both halves so a future
	// "consistency cleanup" cannot silently drop one.
	data, err := json.Marshal(CallResult{
		Usage:    TokenUsage{PromptTokens: 2195, CompletionTokens: 81, TotalTokens: 2276},
		Requests: []ModelRequestStats{{Round: 3, MessageCount: 2, Model: "hy3-ioa"}},
	})
	require.NoError(t, err)

	var fields struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}
		Requests []struct {
			Round        int    `json:"round"`
			MessageCount int    `json:"message_count"`
			Model        string `json:"model"`
		}
	}
	require.NoError(t, json.Unmarshal(data, &fields))
	require.Equal(t, 2195, fields.Usage.PromptTokens)
	require.Equal(t, 81, fields.Usage.CompletionTokens)
	require.Equal(t, 2276, fields.Usage.TotalTokens)
	require.Len(t, fields.Requests, 1)
	require.Equal(t, 3, fields.Requests[0].Round)
	require.Equal(t, 2, fields.Requests[0].MessageCount)
	require.Equal(t, "hy3-ioa", fields.Requests[0].Model)
}

// TestCallResultUnmarshalLegacyPayload decodes a real call_result payload
// taken verbatim from an on-disk session jsonl (examples/, 2026-09). Every
// session jsonl produced so far uses these PascalCase names, so reading
// them back must keep working.
func TestCallResultUnmarshalLegacyPayload(t *testing.T) {
	const legacy = `{
		"Status": "completed",
		"Reply": "hi",
		"CallTurnID": "tt_5eb9655c3700b924:answer",
		"AgentID": "assistant",
		"Records": [],
		"Next": {"action": "proceed"},
		"Usage": {"prompt_tokens": 2195, "completion_tokens": 81, "total_tokens": 2276},
		"Requests": [{
			"round": 0,
			"message_count": 2,
			"system_chars": 3781,
			"user_chars": 42,
			"tool_schema_count": 6,
			"estimated_prompt_tokens": 1504,
			"prompt_hash": "sha256:ebd5c19d14833c8145407d3fbb6c5f8f89b6c5cc821633ad053209e8f5b8a616",
			"usage": {"prompt_tokens": 2195, "completion_tokens": 81, "total_tokens": 2276},
			"model": "hy3-ioa"
		}],
		"WorkspaceOps": null,
		"ToolCalls": 0,
		"Error": "",
		"CheckpointID": "",
		"Checkpoint": null,
		"TaskID": "",
		"PendingApproval": null,
		"Approval": null
	}`

	var result CallResult
	require.NoError(t, json.Unmarshal([]byte(legacy), &result))

	require.Equal(t, TurnCompleted, result.Status)
	require.Equal(t, "hi", result.Reply)
	require.Equal(t, "tt_5eb9655c3700b924:answer", result.CallTurnID)
	require.Equal(t, "assistant", result.AgentID)
	require.Equal(t, 2195, result.Usage.PromptTokens)
	require.Equal(t, 2276, result.Usage.TotalTokens)
	require.Len(t, result.Requests, 1)
	require.Equal(t, "hy3-ioa", result.Requests[0].Model)
	require.Equal(t, 2195, result.Requests[0].Usage.PromptTokens)
	require.NotNil(t, result.Next)
	require.Equal(t, "proceed", string(result.Next.Action))
}

// TestCallResultRoundTrip guards the property that actually matters for
// resume: a result written to team.jsonl and read back loses nothing.
func TestCallResultRoundTrip(t *testing.T) {
	original := CallResult{
		Status:       TurnWaitingTool,
		Reply:        "waiting on bash",
		CallTurnID:   "tt_1:answer",
		AgentID:      "assistant",
		Next:         &Route{Action: NextProceed},
		Usage:        TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		Requests:     []ModelRequestStats{{Round: 1, MessageCount: 4}},
		WorkspaceOps: []WorkspaceOperation{{}},
		ToolCalls:    7,
		Error:        "",
		CheckpointID: "cp_1",
		TaskID:       "task_1",
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	// Round-trip through a generic map, the way storage.SessionEvent
	// decodes team.jsonl before runtime decodes it into CallResult.
	var generic map[string]any
	require.NoError(t, json.Unmarshal(data, &generic))

	reencoded, err := json.Marshal(generic)
	require.NoError(t, err)

	var decoded CallResult
	require.NoError(t, json.Unmarshal(reencoded, &decoded))

	require.Equal(t, original.Status, decoded.Status)
	require.Equal(t, original.Reply, decoded.Reply)
	require.Equal(t, original.CallTurnID, decoded.CallTurnID)
	require.Equal(t, original.AgentID, decoded.AgentID)
	require.Equal(t, original.Usage, decoded.Usage)
	require.Equal(t, original.Requests, decoded.Requests)
	require.Equal(t, original.WorkspaceOps, decoded.WorkspaceOps)
	require.Equal(t, original.ToolCalls, decoded.ToolCalls)
	require.Equal(t, original.CheckpointID, decoded.CheckpointID)
	require.Equal(t, original.TaskID, decoded.TaskID)
	require.Equal(t, "proceed", string(decoded.Next.Action))
}
