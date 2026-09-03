package call

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func webhookRequest(url string) types.CallRequest {
	return types.CallRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "team-1"},
		Call: types.Call{
			ID:      "notify",
			Type:    types.CallWebhook,
			Webhook: &types.WebhookSpec{URL: url},
		},
		Input: "hello",
	}
}

func TestWebhookExecutorSuccess(t *testing.T) {
	var gotMethod, gotContentType, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("accepted"))
	}))
	defer server.Close()

	exec := NewWebhookExecutor(server.Client())
	result, err := exec.Execute(context.Background(), webhookRequest(server.URL))

	require.NoError(t, err)
	assert.Equal(t, types.TurnCompleted, result.Status)
	assert.Equal(t, "accepted", result.Reply)
	assert.Equal(t, http.MethodPost, gotMethod, "default method should be POST")
	assert.Equal(t, "application/json", gotContentType)
	assert.Contains(t, gotBody, "hello")
	assert.Contains(t, gotBody, "fs-1")
}

func TestWebhookExecutorNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	exec := NewWebhookExecutor(server.Client())
	result, err := exec.Execute(context.Background(), webhookRequest(server.URL))

	require.NoError(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, result.Error, "500")
	assert.Equal(t, "boom", result.Reply)
}

func TestWebhookExecutorCustomMethodAndHeaders(t *testing.T) {
	var gotMethod, gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	req := webhookRequest(server.URL)
	req.Call.Webhook.Method = http.MethodPut
	req.Call.Webhook.Headers = map[string]string{"X-Custom": "abc"}

	exec := NewWebhookExecutor(server.Client())
	_, err := exec.Execute(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "abc", gotHeader)
}

func TestWebhookExecutorMissingURL(t *testing.T) {
	exec := NewWebhookExecutor(nil)
	req := webhookRequest("")
	req.Call.Webhook = &types.WebhookSpec{}

	result, err := exec.Execute(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, err.Error(), "url is required")
}

func TestWebhookExecutorTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("late"))
	}))
	defer server.Close()

	req := webhookRequest(server.URL)
	req.Call.Webhook.Timeout = "10ms"

	exec := NewWebhookExecutor(server.Client())
	result, err := exec.Execute(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
}
