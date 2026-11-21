package view

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// configRuntime adds the optional topology extension to the shared mock: the
// config view reads the Flow graph from the runtime, not from a file.
type configRuntime struct {
	mockRuntime
	topology types.FlowTopology
}

func (c *configRuntime) Topology(context.Context, string) (types.FlowTopology, error) {
	return c.topology, nil
}

const configSettings = `{
  "logging": {"level": "info", "dir": ".agents/data/logs", "max_file_size": "50MB",
    "retention_days": 7, "sink_token": "log-secret"},
  "runtime": {"max_agent_rounds": 12, "max_team_turns": 4, "max_parallel_tools": 2,
    "internal_budget": 99},
  "env": {"SERVICE_TOKEN": "env-secret-value", "WORKSPACE": "/path/to/workspace"},
  "tools": {"repl": {"enabled": true, "sandbox": "internal"}, "web": {"enabled": false}}
}`

const configModels = `{
  "model": "model-a",
  "models": [
    {"id": "model-a", "name": "model-a", "base_url": "https://api.example.com/v1",
     "api_key": "sk-secret-value"},
    {"id": "model-b", "name": "model-b", "base_url": "https://api.example.com/v1",
     "api_key": "sk-other-secret"}
  ]
}`

func newConfigFixture(t *testing.T) *Handler {
	t.Helper()
	files := storage.NewFileStore(t.TempDir())
	require.NoError(t, files.Write(settingsConfigPath, []byte(configSettings)))
	require.NoError(t, files.Write(modelsConfigPath, []byte(configModels)))

	handler := NewRuntimeHandler(&configRuntime{topology: types.FlowTopology{
		FlowID: "flow-a",
		Entry:  "desk",
		Teams: []types.TopologyTeam{
			{Name: "desk", Entry: true, Lifecycle: "shared", Calls: []types.TopologyCall{
				{Name: "plan", Type: "agent", Agent: "planner"},
			}},
			{Name: "fixer", Lifecycle: "isolated", Calls: []types.TopologyCall{
				{Name: "patch", Type: "agent", Agent: "fixer"},
			}},
		},
	}})
	handler.SetFileStore(files)
	handler.SetBuildInfo(BuildInfo{Version: "1.2.3", Commit: "abc"})
	return handler
}

func getConfig(t *testing.T, handler *Handler) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.HandleConfig(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	require.Equal(t, http.StatusOK, recorder.Code)

	body := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

func TestHandleConfigReportsSettingsWithoutSecrets(t *testing.T) {
	handler := newConfigFixture(t)
	recorder := httptest.NewRecorder()
	handler.HandleConfig(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	raw := recorder.Body.String()

	// The page shows what a reader can act on. A credential or an env value that
	// leaks here leaks into every browser that opens the page.
	for _, secret := range []string{"sk-secret-value", "sk-other-secret", "env-secret-value",
		"log-secret", `"api_key":`, `"base_url":`} {
		require.NotContains(t, raw, secret, "config response must not carry %s", secret)
	}
	// Values under settings.json that the whitelist does not cover stay out too.
	require.NotContains(t, raw, "internal_budget")
	require.NotContains(t, raw, "sandbox")

	body := getConfig(t, handler)
	require.Equal(t, "1.2.3", body["version"])
	require.NotEmpty(t, body["workdir"])

	runtime, ok := body["runtime"].(map[string]any)
	require.True(t, ok)
	// The limits in force: configured values win, the rest keep their defaults.
	values := runtime["values"].(map[string]any)
	require.EqualValues(t, 12, values["max_agent_rounds"])
	require.EqualValues(t, 4, values["max_team_turns"])
	require.EqualValues(t, 2, values["max_parallel_tools"])
	require.EqualValues(t, 100, values["max_calls_per_team_turn"], "unset limits fall back to the default")
	require.Len(t, values, len(runtimeConfigFields))
	require.Equal(t, []any{"max_team_turns", "max_agent_rounds", "max_parallel_tools"}, runtime["explicit"],
		"the page can tell a configured value from a default")

	logging := body["logging"].(map[string]any)
	loggingValues := logging["values"].(map[string]any)
	require.Equal(t, "info", loggingValues["level"])
	require.EqualValues(t, 7, loggingValues["retention_days"])
	require.Len(t, logging["explicit"], 4)

	// Permissions default to allow, and the page says so instead of leaving the
	// posture blank.
	permissions := body["permissions"].(map[string]any)
	require.Equal(t, map[string]any{
		"bash": "allow", "write": "allow", "websearch": "allow", "webfetch": "allow",
	}, permissions["values"])
	require.Equal(t, []any{}, permissions["explicit"])

	// Env is reported by name: the values are the engine's environment, which is
	// exactly what an operator would not want on a page.
	require.Equal(t, []any{"SERVICE_TOKEN", "WORKSPACE"}, body["env_keys"])

	tools := body["tools"].(map[string]any)
	require.Equal(t, map[string]any{"enabled": true}, tools["repl"])
	require.Equal(t, []any{}, tools["restricted_bash_phrases"])

	web := body["web"].(map[string]any)
	require.Equal(t, "duckduckgo", web["provider"])
	require.Equal(t, false, web["api_key_set"])
}

func TestHandleConfigListsModelsWithoutEndpoints(t *testing.T) {
	body := getConfig(t, newConfigFixture(t))

	model, ok := body["model"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "model-a", model["default"])
	require.Equal(t, []any{"model-a", "model-b"}, model["available"])
}

func TestHandleConfigCarriesTheFlowGraph(t *testing.T) {
	body := getConfig(t, newConfigFixture(t))

	flow, ok := body["flow"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "flow-a", flow["flow_id"])
	require.Equal(t, "desk", flow["entry"])

	teams, ok := flow["teams"].([]any)
	require.True(t, ok)
	require.Len(t, teams, 2)
	first, ok := teams[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "desk", first["name"])
	require.Equal(t, true, first["entry"])
}

// A handler built without a file store still answers: the runtime's graph and
// the build identity are not files, and a 503 would hide them for no reason.
func TestHandleConfigWithoutFileStore(t *testing.T) {
	handler := NewRuntimeHandler(&configRuntime{topology: types.FlowTopology{FlowID: "flow-b"}})
	body := getConfig(t, handler)

	require.Equal(t, "flow-b", body["flow"].(map[string]any)["flow_id"])
	// Limits and postures still answer: they are the engine's defaults, not a
	// file's, and hiding them would make an empty workspace look unconfigured.
	runtime := body["runtime"].(map[string]any)
	require.EqualValues(t, 200, runtime["values"].(map[string]any)["max_agent_rounds"])
	require.Equal(t, []any{}, runtime["explicit"])
	require.NotContains(t, body, "model")
	require.NotContains(t, body, "env_keys")
}

// ===== PATCH /api/config =====

func patchConfig(t *testing.T, handler *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.HandleConfigUpdate(recorder, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(body)))
	return recorder
}

// The page edits some fields; everything it does not send has to stay exactly
// where it was, including sections this page has never heard of.
func TestHandleConfigUpdateMergesEditableSections(t *testing.T) {
	handler := newConfigFixture(t)
	require.NoError(t, handler.files.Write(settingsConfigPath, []byte(`{
  "logging": {"level": "info", "dir": ".agents/data/logs", "max_file_size": "50MB", "retention_days": 7},
  "runtime": {"max_agent_rounds": 12, "max_team_turns": 4, "max_parallel_tools": 2},
  "env": {"SERVICE_TOKEN": "env-secret-value"},
  "tools": {"repl": {"enabled": true, "sandbox": "internal"}},
  "permissions": {"posture": "ask"}
}`)))

	recorder := patchConfig(t, handler, `{
  "runtime": {"max_agent_rounds": 30},
  "logging": {"level": "debug", "retention_days": 3},
  "tools": {"repl": {"enabled": false}},
  "env": {"WORKSPACE": "/path/to/workspace"}
}`)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	body := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, true, body["updated"])
	require.Equal(t, true, body["restart_required"])
	require.EqualValues(t, 30, body["runtime"].(map[string]any)["values"].(map[string]any)["max_agent_rounds"])

	raw, err := handler.files.Read(settingsConfigPath)
	require.NoError(t, err)
	saved := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &saved))

	runtime := saved["runtime"].(map[string]any)
	require.EqualValues(t, 30, runtime["max_agent_rounds"])
	require.EqualValues(t, 4, runtime["max_team_turns"], "untouched keys stay")

	logging := saved["logging"].(map[string]any)
	require.Equal(t, "debug", logging["level"])
	require.EqualValues(t, 3, logging["retention_days"])
	require.Equal(t, "50MB", logging["max_file_size"])

	repl := saved["tools"].(map[string]any)["repl"].(map[string]any)
	require.Equal(t, false, repl["enabled"])
	require.Equal(t, "internal", repl["sandbox"], "fields the page does not edit stay")

	require.Equal(t, "ask", saved["permissions"].(map[string]any)["posture"], "unknown sections survive")

	env := saved["env"].(map[string]any)
	require.Equal(t, "/path/to/workspace", env["WORKSPACE"])
	require.Equal(t, "env-secret-value", env["SERVICE_TOKEN"], "untouched env values stay")

	// The write path never echoes a value back, not even the one just written.
	require.NotContains(t, recorder.Body.String(), "env-secret-value")
	require.NotContains(t, recorder.Body.String(), "/path/to/workspace")
}

func TestHandleConfigUpdateRejectsWhatItCannotApply(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"unknown runtime field", `{"runtime": {"max_turns": 3}}`},
		{"runtime field of the wrong type", `{"runtime": {"max_agent_rounds": "many"}}`},
		{"logging field of the wrong type", `{"logging": {"retention_days": "seven"}}`},
		{"unknown logging field", `{"logging": {"sink_token": "x"}}`},
		{"unknown tool", `{"tools": {"nope": {"enabled": true}}}`},
		{"unknown tool field", `{"tools": {"repl": {"sandbox": "x"}}}`},
		{"unknown section", `{"permissions": {"posture": "ask"}}`},
		{"permission outside allow/ask/deny", `{"permissions": {"bash": "maybe"}}`},
		{"repl budget of the wrong type", `{"tools": {"repl": {"timeout_ms": "long"}}}`},
		{"repl budget of zero", `{"tools": {"repl": {"max_steps": 0}}}`},
		{"bash phrases not a list", `{"tools": {"restricted_bash_phrases": "curl"}}`},
		{"unknown knowledge field", `{"knowledge": {"summary": "x"}}`},
		{"empty update", `{}`},
		{"broken json", `{"runtime":`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			handler := newConfigFixture(t)
			recorder := patchConfig(t, handler, tt.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		})
	}
}

// A file the engine cannot parse is never overwritten: the patch would replace
// whatever is actually there with a file built from nothing.
func TestHandleConfigUpdateRefusesToOverwriteUnparsableSettings(t *testing.T) {
	handler := newConfigFixture(t)
	require.NoError(t, handler.files.Write(settingsConfigPath, []byte("{not json")))

	recorder := patchConfig(t, handler, `{"runtime": {"max_agent_rounds": 5}}`)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)

	raw, err := handler.files.Read(settingsConfigPath)
	require.NoError(t, err)
	require.Equal(t, "{not json", string(raw))
}

// The rest of settings.json is editable too: the posture each governed tool
// gets, the REPL budget, the bash phrases the gate refuses, and the knowledge
// summary model.
func TestHandleConfigUpdateCoversTheRestOfSettings(t *testing.T) {
	handler := newConfigFixture(t)

	recorder := patchConfig(t, handler, `{
  "permissions": {"bash": "ask", "webfetch": "deny"},
  "knowledge": {"summary_model": "model-b"},
  "tools": {"repl": {"timeout_ms": 120000, "max_steps": 50}, "restricted_bash_phrases": ["curl"]}
}`)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	raw, err := handler.files.Read(settingsConfigPath)
	require.NoError(t, err)
	saved := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &saved))

	permissions := saved["permissions"].(map[string]any)
	require.Equal(t, "ask", permissions["bash"])
	require.Equal(t, "deny", permissions["webfetch"])

	require.Equal(t, "model-b", saved["knowledge"].(map[string]any)["summary_model"])

	tools := saved["tools"].(map[string]any)
	repl := tools["repl"].(map[string]any)
	require.EqualValues(t, 120000, repl["timeout_ms"])
	require.EqualValues(t, 50, repl["max_steps"])
	require.Equal(t, true, repl["enabled"], "untouched switches stay")
	require.Equal(t, "internal", repl["sandbox"], "fields the page does not edit stay")
	require.Equal(t, []any{"curl"}, tools["restricted_bash_phrases"])
}
