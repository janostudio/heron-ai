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

// teamTurnResultWireFields is the same contract for the team_result payload.
// TeamTurnResult is what internal/runtime/flow decodes when it needs to
// resume a Team turn that ended waiting on a tool or an approval.
var teamTurnResultWireFields = []string{
	"Turn",
	"Reply",
	"Records",
	"CallResults",
	"PendingToolTasks",
	"PendingApprovals",
	"Usage",
	"Next",
	"Error",
}

// assertWireFields fails if the marshaled struct does not expose exactly the
// published field names, and returns the decoded field map for further checks.
func assertWireFields(t *testing.T, value any, want []string) map[string]json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &fields))

	got := make([]string, 0, len(fields))
	for name := range fields {
		got = append(got, name)
	}
	sort.Strings(got)

	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	require.Equal(t, sorted, got, "wire fields changed; this breaks external jsonl parsers")
	return fields
}

func TestCallResultMarshalUsesPublishedFieldNames(t *testing.T) {
	assertWireFields(t, CallResult{}, callResultWireFields)
}

func TestCallResultMarshalKeepsEmptyFields(t *testing.T) {
	// No omitempty: consumers can rely on every field being present, even
	// when null. This matches every session jsonl written so far.
	fields := assertWireFields(t, CallResult{Status: TurnCompleted}, callResultWireFields)
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

func TestTeamTurnResultMarshalUsesPublishedFieldNames(t *testing.T) {
	assertWireFields(t, TeamTurnResult{}, teamTurnResultWireFields)
}

// TestTeamTurnResultLeafTypesStaySnakeCase pins the other half of the mixed
// contract: TeamTurnResult is PascalCase but the TeamTurn nested in it is
// snake_case. Both halves have to stay put.
func TestTeamTurnResultLeafTypesStaySnakeCase(t *testing.T) {
	data, err := json.Marshal(TeamTurnResult{
		Turn: TeamTurn{
			ID:         "tt_1",
			FlowTurnID: "ft_1",
			TeamID:     "default",
			Attempt:    2,
			Status:     TurnWaitingTool,
			CallerTeam: "parent",
			RecoveryOf: "tt_0",
		},
		PendingToolTasks: []PendingToolTask{{CallID: "answer", TaskID: "task_1", CheckpointID: "cp_1"}},
		Usage:            TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	})
	require.NoError(t, err)

	var decoded struct {
		Turn struct {
			ID         string     `json:"id"`
			FlowTurnID string     `json:"flow_turn_id"`
			TeamID     string     `json:"team_id"`
			Attempt    int        `json:"attempt"`
			Status     TurnStatus `json:"status"`
			CallerTeam string     `json:"caller_team"`
			RecoveryOf string     `json:"recovery_of"`
		}
		PendingToolTasks []struct {
			CallID       string `json:"call_id"`
			TaskID       string `json:"task_id"`
			CheckpointID string `json:"checkpoint_id"`
		}
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}
	}
	require.NoError(t, json.Unmarshal(data, &decoded))

	require.Equal(t, "tt_1", decoded.Turn.ID)
	require.Equal(t, "ft_1", decoded.Turn.FlowTurnID)
	require.Equal(t, "default", decoded.Turn.TeamID)
	require.Equal(t, 2, decoded.Turn.Attempt)
	require.Equal(t, TurnWaitingTool, decoded.Turn.Status)
	require.Equal(t, "parent", decoded.Turn.CallerTeam)
	require.Equal(t, "tt_0", decoded.Turn.RecoveryOf)
	require.Len(t, decoded.PendingToolTasks, 1)
	require.Equal(t, "answer", decoded.PendingToolTasks[0].CallID)
	require.Equal(t, "task_1", decoded.PendingToolTasks[0].TaskID)
	require.Equal(t, "cp_1", decoded.PendingToolTasks[0].CheckpointID)
	require.Equal(t, 30, decoded.Usage.TotalTokens)
}

// TestTeamTurnResultUnmarshalLegacyPayload decodes a real team_result
// payload taken verbatim from an on-disk session jsonl (examples/,
// 2026-10-08). Every session jsonl written so far uses these PascalCase
// names, so reading them back must keep working — this is the path
// internal/runtime/flow uses to resume a Team turn that ended waiting.
func TestTeamTurnResultUnmarshalLegacyPayload(t *testing.T) {
	const legacy = `{
		"Turn": {
			"id": "tt_7b562393f6deca50",
			"flow_turn_id": "ft_b6a1a45aaea98e86",
			"team_session_id": "fs_df2a5c8c965ec5ef:default",
			"team_id": "default",
			"attempt": 1,
			"status": "completed",
			"next": {"action": "proceed"},
			"record_ids": ["answer-1791449146019266000"],
			"started_at": "2026-10-08T08:45:44.463241Z",
			"finished_at": "2026-10-08T08:45:46.020982Z"
		},
		"Reply": "请问有什么可以帮您？",
		"Records": [],
		"CallResults": {
			"answer": {
				"Status": "completed",
				"Reply": "请问有什么可以帮您？",
				"CallTurnID": "tt_7b562393f6deca50:answer",
				"AgentID": "assistant",
				"Next": {"action": "proceed"},
				"Usage": {"prompt_tokens": 2295, "completion_tokens": 7, "total_tokens": 2302},
				"Requests": [],
				"ToolCalls": 0,
				"CheckpointID": "",
				"TaskID": ""
			}
		},
		"PendingToolTasks": null,
		"PendingApprovals": null,
		"Usage": {"prompt_tokens": 2295, "completion_tokens": 7, "total_tokens": 2302},
		"Next": {"action": "proceed"},
		"Error": ""
	}`

	var result TeamTurnResult
	require.NoError(t, json.Unmarshal([]byte(legacy), &result))

	require.Equal(t, "tt_7b562393f6deca50", result.Turn.ID)
	require.Equal(t, "ft_b6a1a45aaea98e86", result.Turn.FlowTurnID)
	require.Equal(t, "default", result.Turn.TeamID)
	require.Equal(t, 1, result.Turn.Attempt)
	require.Equal(t, TurnCompleted, result.Turn.Status)
	require.Equal(t, "请问有什么可以帮您？", result.Reply)
	require.Equal(t, 2295, result.Usage.PromptTokens)
	require.Equal(t, 2302, result.Usage.TotalTokens)
	require.NotNil(t, result.Next)
	require.Equal(t, "proceed", string(result.Next.Action))

	// The embedded CallResult must survive too; this is the part a
	// snake_case rename of CallResults would silently wipe out.
	require.Len(t, result.CallResults, 1)
	callResult, ok := result.CallResults["answer"]
	require.True(t, ok, `payload.team_result.CallResults["answer"] missing`)
	require.Equal(t, TurnCompleted, callResult.Status)
	require.Equal(t, "tt_7b562393f6deca50:answer", callResult.CallTurnID)
	require.Equal(t, "assistant", callResult.AgentID)
	require.Equal(t, 2295, callResult.Usage.PromptTokens)
}

// TestTeamTurnResultRoundTrip guards the resume path end to end: a waiting
// Team result written to team.jsonl and read back loses nothing, including
// the per-call results nested inside it.
func TestTeamTurnResultRoundTrip(t *testing.T) {
	original := TeamTurnResult{
		Turn: TeamTurn{ID: "tt_1", TeamID: "default", Status: TurnWaitingTool, Attempt: 3},
		CallResults: map[string]CallResult{
			"answer": {
				Status:       TurnWaitingTool,
				CallTurnID:   "tt_1:answer",
				AgentID:      "assistant",
				CheckpointID: "cp_1",
				TaskID:       "task_1",
				Usage:        TokenUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
				Requests:     []ModelRequestStats{{Round: 2, MessageCount: 5}},
			},
		},
		PendingToolTasks: []PendingToolTask{{CallID: "answer", TaskID: "task_1", CheckpointID: "cp_1"}},
		Usage:            TokenUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		Next:             &Route{Action: NextWaitTool},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	// Round-trip through a generic map, the way storage.SessionEvent
	// decodes team.jsonl before runtime decodes it into TeamTurnResult.
	var generic map[string]any
	require.NoError(t, json.Unmarshal(data, &generic))

	reencoded, err := json.Marshal(generic)
	require.NoError(t, err)

	var decoded TeamTurnResult
	require.NoError(t, json.Unmarshal(reencoded, &decoded))

	require.Equal(t, original.Turn, decoded.Turn)
	require.Equal(t, original.Reply, decoded.Reply)
	require.Equal(t, original.Usage, decoded.Usage)
	require.Equal(t, original.PendingToolTasks, decoded.PendingToolTasks)
	require.Equal(t, "wait_tool", string(decoded.Next.Action))

	require.Len(t, decoded.CallResults, 1)
	callResult, ok := decoded.CallResults["answer"]
	require.True(t, ok)
	require.Equal(t, TurnWaitingTool, callResult.Status)
	require.Equal(t, "tt_1:answer", callResult.CallTurnID)
	require.Equal(t, "assistant", callResult.AgentID)
	require.Equal(t, "cp_1", callResult.CheckpointID)
	require.Equal(t, "task_1", callResult.TaskID)
	require.Equal(t, original.CallResults["answer"].Usage, callResult.Usage)
	require.Equal(t, original.CallResults["answer"].Requests, callResult.Requests)
}
