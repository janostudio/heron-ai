package call

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

func (e *WebhookExecutor) Type() types.CallType {
	return types.CallWebhook
}

func (e *WebhookExecutor) Execute(ctx context.Context, req types.CallRequest) (types.CallResult, error) {
	if req.Call.Webhook == nil || strings.TrimSpace(req.Call.Webhook.URL) == "" {
		return types.CallResult{Status: types.TurnFailed, Error: "webhook url is required"}, fmt.Errorf("webhook url is required for call %q", req.Call.ID)
	}
	if req.RecoveryOf != "" && req.Call.Webhook.ReplayPolicy != types.ReplayAllow && req.Call.Webhook.ReplayPolicy != types.ReplayIdempotent {
		return types.CallResult{
			Status: types.TurnFailed,
			Error:  "webhook replay is not allowed by replay_policy",
		}, fmt.Errorf("webhook call %q replay policy is %q", req.Call.ID, req.Call.Webhook.ReplayPolicy)
	}
	if req.RecoveryOf != "" &&
		req.Call.Webhook.ReplayPolicy == types.ReplayIdempotent &&
		strings.TrimSpace(req.Call.Webhook.IdempotencyKey) == "" {
		return types.CallResult{
			Status: types.TurnFailed,
			Error:  "webhook idempotent replay requires idempotency_key",
		}, fmt.Errorf("webhook call %q idempotent replay requires idempotency_key", req.Call.ID)
	}

	payload := map[string]any{
		"input":           req.Input,
		"records":         req.Records,
		"flow_session_id": req.FlowSession.ID,
		"flow_turn_id":    req.FlowTurn.ID,
		"team_id":         req.TeamTurn.TeamID,
		"team_turn_id":    req.TeamTurn.ID,
		"call_id":         req.Call.ID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return types.CallResult{Status: types.TurnFailed, Error: err.Error()}, err
	}

	method := req.Call.Webhook.Method
	if strings.TrimSpace(method) == "" {
		method = http.MethodPost
	}
	execCtx, cancel, err := withTimeout(ctx, req.Call.Timeout, req.Call.Webhook.Timeout)
	if err != nil {
		return types.CallResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	defer cancel()

	httpReq, err := http.NewRequestWithContext(execCtx, method, req.Call.Webhook.URL, bytes.NewReader(body))
	if err != nil {
		return types.CallResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(req.Call.Webhook.IdempotencyKey); key != "" {
		httpReq.Header.Set("Idempotency-Key", resolveTemplate(key, req))
	}
	for name, value := range req.Call.Webhook.Headers {
		httpReq.Header.Set(name, value)
	}

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return types.CallResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return types.CallResult{Status: types.TurnFailed, Error: readErr.Error()}, readErr
	}

	summary := strings.TrimSpace(string(responseBody))
	result := types.CallResult{
		Status: types.TurnCompleted,
		Reply:  summary,
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Status = types.TurnFailed
		result.Error = fmt.Sprintf("webhook returned status %d", resp.StatusCode)
	}
	if recordName := req.Call.Output.Record; recordName != "" {
		result.Records = []types.SharedRecord{newCallRecord(
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
