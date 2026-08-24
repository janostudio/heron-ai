package member

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type WebhookExecutor struct {
	client *http.Client
}

func NewWebhookExecutor(client *http.Client) *WebhookExecutor {
	if client == nil {
		client = http.DefaultClient
	}
	return &WebhookExecutor{client: client}
}

func (e *WebhookExecutor) Type() types.MemberType {
	return types.MemberWebhook
}

func (e *WebhookExecutor) Execute(ctx context.Context, req types.MemberRequest) (types.MemberResult, error) {
	if req.Member.Webhook == nil || strings.TrimSpace(req.Member.Webhook.URL) == "" {
		return types.MemberResult{Status: types.TurnFailed, Error: "webhook url is required"}, fmt.Errorf("webhook url is required for member %q", req.Member.ID)
	}

	payload := map[string]any{
		"input":           req.Input,
		"records":         req.Records,
		"flow_session_id": req.FlowSession.ID,
		"flow_turn_id":    req.FlowTurn.ID,
		"team_id":         req.TeamTurn.TeamID,
		"team_turn_id":    req.TeamTurn.ID,
		"member_id":       req.Member.ID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return types.MemberResult{Status: types.TurnFailed, Error: err.Error()}, err
	}

	method := req.Member.Webhook.Method
	if strings.TrimSpace(method) == "" {
		method = http.MethodPost
	}
	execCtx, cancel, err := withTimeout(ctx, req.Member.Timeout, req.Member.Webhook.Timeout)
	if err != nil {
		return types.MemberResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	defer cancel()

	httpReq, err := http.NewRequestWithContext(execCtx, method, req.Member.Webhook.URL, bytes.NewReader(body))
	if err != nil {
		return types.MemberResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for name, value := range req.Member.Webhook.Headers {
		httpReq.Header.Set(name, value)
	}

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return types.MemberResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return types.MemberResult{Status: types.TurnFailed, Error: readErr.Error()}, readErr
	}

	summary := strings.TrimSpace(string(responseBody))
	result := types.MemberResult{
		Status: types.TurnCompleted,
		Reply:  summary,
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Status = types.TurnFailed
		result.Error = fmt.Sprintf("webhook returned status %d", resp.StatusCode)
	}
	if recordName := req.Member.Output.Record; recordName != "" {
		result.Records = []types.SharedRecord{newMemberRecord(
			req,
			recordName,
			"webhook_result",
			summary,
			map[string]any{
				"status_code": resp.StatusCode,
				"body":        string(responseBody),
			},
		)}
	}
	return result, nil
}
