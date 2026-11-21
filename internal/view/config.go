package view

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Where the engine keeps the configuration this view reports: both paths are
// relative to the workspace root the file store is anchored at, which is the
// directory the process was started in.
const (
	settingsConfigPath = ".agents/settings.json"
	modelsConfigPath   = ".agents/models.json"
	// A settings file is small; a caller that sends megabytes is not editing it.
	maxConfigPayload = 1 << 20
)

// Every section settings.json can hold, in the order this page reads them.
// settingsSchema in internal/config is the authority on which keys exist; this
// list is the projection of it that a reader can act on.
var (
	runtimeConfigFields = []string{
		"max_team_turns", "max_calls_per_team_turn", "max_agent_rounds",
		"max_parallel_teams", "max_parallel_calls", "max_coordinate_retries",
		"max_parallel_tools", "max_active_team_sessions", "max_task_context_depth",
	}
	loggingConfigFields = []string{"level", "output", "dir", "max_file_size", "max_backups", "retention_days"}
	permissionFields    = []string{"bash", "write", "websearch", "webfetch"}
	replConfigFields    = []string{"enabled", "timeout_ms", "max_steps", "max_output_bytes", "catalog_budget"}
)

// Definition assets: the files that make up this workspace's Flow, teams,
// agents and skills. They are configuration too, just not the kind that fits a
// key/value page.
var assetDirs = map[string]string{
	"flows":     ".agents/flows",
	"teams":     ".agents/teams",
	"agents":    ".agents/agents",
	"skills":    ".agents/skills",
	"rules":     ".agents/rules",
	"knowledge": ".agents/knowledge",
}

// HandleConfig reports the configuration the server is running with.
//
// It is an extract, not a file dump. models.json carries provider credentials
// and settings.json carries API keys and the environment the engine runs with,
// so neither file is passed through: what leaves the process is settings,
// switches, model ids and the Flow graph, with credentials reduced to "is one
// set" and env entries named without their values.
func (h *Handler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.configSnapshot(r))
}

func (h *Handler) configSnapshot(r *http.Request) map[string]any {
	response := map[string]any{
		"version": h.build.Version,
		"commit":  h.build.Commit,
		"files": map[string]string{
			"settings": settingsConfigPath,
			"models":   modelsConfigPath,
		},
	}
	if workdir, err := os.Getwd(); err == nil {
		response["workdir"] = workdir
	}

	settings := h.readConfigJSON(settingsConfigPath)
	response["runtime"] = runtimeSnapshot(settings)
	response["logging"] = sectionSnapshot(settings, "logging", loggingConfigFields)
	response["permissions"] = permissionsSnapshot(settings)
	response["tools"] = toolsSnapshot(settings)
	response["web"] = webSnapshot(settings)
	response["knowledge"] = sectionSnapshot(settings, "knowledge", []string{"summary_model"})
	if mcp := mcpSnapshot(settings); len(mcp) > 0 {
		response["mcp"] = mcp
	}
	if env := nestedObject(settings, "env"); env != nil {
		response["env_keys"] = sortedKeys(env)
	}
	response["assets"] = h.assetsSnapshot()

	if models := h.readConfigJSON(modelsConfigPath); models != nil {
		response["model"] = modelSummary(models)
	}

	// The Flow graph is the runtime's, not a file's: it is the same answer the
	// run board draws, so the two cannot disagree about the configured Teams.
	if runtime, ok := h.runtime.(types.TopologyRuntime); ok {
		if topology, err := runtime.Topology(r.Context(), ""); err == nil {
			response["flow"] = topology
		}
	}
	return response
}

// runtimeSnapshot reports the limits in force, each one with its default
// already applied, plus which of them the file actually sets. Without the
// second list a reader cannot tell "this project chose 200" from "nobody
// configured it and 200 is the fallback".
func runtimeSnapshot(settings map[string]any) map[string]any {
	values := structMap(types.RuntimeLimits{}.WithDefaults())
	explicit := []string{}
	if object := nestedObject(settings, "runtime"); object != nil {
		for _, field := range runtimeConfigFields {
			value, ok := object[field]
			if !ok {
				continue
			}
			if number, ok := toInt(value); ok {
				values[field] = number
			}
			explicit = append(explicit, field)
		}
	}
	return map[string]any{"values": values, "explicit": explicit}
}

func sectionSnapshot(settings map[string]any, name string, fields []string) map[string]any {
	values := map[string]any{}
	explicit := []string{}
	if object := nestedObject(settings, name); object != nil {
		for _, field := range fields {
			value, ok := object[field]
			if !ok {
				continue
			}
			values[field] = value
			explicit = append(explicit, field)
		}
	}
	return map[string]any{"values": values, "explicit": explicit}
}

// permissionsSnapshot fills the unset entries with the default the engine
// applies (allow) so the page never shows a blank posture.
func permissionsSnapshot(settings map[string]any) map[string]any {
	values := map[string]any{}
	for _, field := range permissionFields {
		values[field] = "allow"
	}
	explicit := []string{}
	if object := nestedObject(settings, "permissions"); object != nil {
		for _, field := range permissionFields {
			value, ok := object[field].(string)
			if !ok || value == "" {
				continue
			}
			values[field] = value
			explicit = append(explicit, field)
		}
	}
	return map[string]any{"values": values, "explicit": explicit}
}

// toolsSnapshot covers both halves of the tools section: the REPL switch with
// its budget, and the bash phrases the gate refuses.
func toolsSnapshot(settings map[string]any) map[string]any {
	repl := map[string]any{}
	if block := nestedObject(settings, "tools"); block != nil {
		if object := nestedObject(block, "repl"); object != nil {
			for _, field := range replConfigFields {
				if value, ok := object[field]; ok {
					repl[field] = value
				}
			}
		}
	}
	phrases := []string{}
	if block := nestedObject(settings, "tools"); block != nil {
		if list, ok := block["restricted_bash_phrases"].([]any); ok {
			for _, item := range list {
				if text, ok := item.(string); ok {
					phrases = append(phrases, text)
				}
			}
		}
	}
	sort.Strings(phrases)
	return map[string]any{"repl": repl, "restricted_bash_phrases": phrases}
}

// webSnapshot answers "which provider, and is a key set" without ever carrying
// the key or the endpoint: both can hold a token.
func webSnapshot(settings map[string]any) map[string]any {
	summary := map[string]any{"provider": "duckduckgo", "api_key_set": false}
	web := nestedObject(settings, "web")
	if web == nil {
		return summary
	}
	if provider, ok := web["provider"].(string); ok && provider != "" {
		summary["provider"] = provider
	}
	if key, ok := web["api_key"].(string); ok && key != "" {
		summary["api_key_set"] = true
	}
	summary["search"] = webEndpoint(web, "search")
	summary["fetch"] = webEndpoint(web, "fetch")
	return summary
}

func webEndpoint(web map[string]any, name string) map[string]any {
	out := map[string]any{}
	object := nestedObject(web, name)
	if object == nil {
		return out
	}
	for _, field := range []string{"provider", "max_results", "location", "language", "timeout_ms", "format"} {
		if value, ok := object[field]; ok {
			out[field] = value
		}
	}
	if url, ok := object["url"].(string); ok && url != "" {
		// The endpoint can carry a key in its query string; the fact that one is
		// configured is all this page needs.
		out["url_set"] = true
	}
	return out
}

// mcpSnapshot keeps server identity and shapes, not the arguments, headers or
// env values: those are where an MCP server is handed its credentials.
func mcpSnapshot(settings map[string]any) []map[string]any {
	list, _ := settings["mcp"].([]any)
	servers := make([]map[string]any, 0, len(list))
	for _, item := range list {
		server, ok := item.(map[string]any)
		if !ok {
			continue
		}
		entry := map[string]any{}
		for _, field := range []string{"name", "transport", "command"} {
			if value, ok := server[field].(string); ok && value != "" {
				entry[field] = value
			}
		}
		if url, ok := server["url"].(string); ok && url != "" {
			entry["url_set"] = true
		}
		if args, ok := server["args"].([]any); ok {
			entry["args_count"] = len(args)
		}
		if headers := nestedObject(server, "headers"); headers != nil {
			entry["header_keys"] = sortedKeys(headers)
		}
		if env := nestedObject(server, "env"); env != nil {
			entry["env_keys"] = sortedKeys(env)
		}
		servers = append(servers, entry)
	}
	return servers
}

func (h *Handler) assetsSnapshot() map[string]any {
	assets := map[string]any{}
	for name, dir := range assetDirs {
		assets[name] = h.listAssetNames(dir)
	}
	return assets
}

// listAssetNames reports what defines this workspace. Backups are left out:
// they are not loaded, and listing them would suggest they are.
func (h *Handler) listAssetNames(dir string) []string {
	if h.files == nil {
		return nil
	}
	names, err := h.files.List(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if strings.HasPrefix(name, ".") || strings.Contains(name, ".bak") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// configUpdate is a partial settings.json.
//
// Sections are merged into the file, never replace it: a client that edits the
// log level must not wipe the tool switches it did not send. Unknown sections
// are rejected rather than ignored — a typo that silently writes nothing is
// worse than a 400.
type configUpdate struct {
	Runtime     map[string]json.RawMessage `json:"runtime"`
	Logging     map[string]json.RawMessage `json:"logging"`
	Knowledge   map[string]json.RawMessage `json:"knowledge"`
	Permissions map[string]json.RawMessage `json:"permissions"`
	// Tools holds the REPL block and the bash phrase list, so each value is
	// decoded on its own: one is an object, the other an array.
	Tools map[string]json.RawMessage `json:"tools"`
	Env   map[string]string          `json:"env"`
}

func (u configUpdate) empty() bool {
	return len(u.Runtime) == 0 && len(u.Logging) == 0 && len(u.Knowledge) == 0 &&
		len(u.Permissions) == 0 && len(u.Tools) == 0 && len(u.Env) == 0
}

// HandleConfigUpdate applies the editable parts of settings.json.
//
// The values it writes are read when the runtime is built, so the response says
// the change arrives on the next start rather than pretending it is live.
func (h *Handler) HandleConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if h.files == nil {
		http.Error(w, "settings are not writable in this process", http.StatusServiceUnavailable)
		return
	}
	var update configUpdate
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxConfigPayload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&update); err != nil {
		http.Error(w, "invalid config update: "+err.Error(), http.StatusBadRequest)
		return
	}
	if update.empty() {
		http.Error(w, "nothing to update", http.StatusBadRequest)
		return
	}

	current, err := h.readSettings()
	if err != nil {
		http.Error(w, "settings.json cannot be read: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if current == nil {
		current = map[string]any{}
	}

	applyErr := error(nil)
	if len(update.Runtime) > 0 {
		applyErr = mergeFields(ensureObject(current, "runtime"), "runtime", update.Runtime,
			stringSet(runtimeConfigFields), nil)
	}
	if applyErr == nil && len(update.Logging) > 0 {
		applyErr = mergeFields(ensureObject(current, "logging"), "logging", update.Logging,
			stringSet([]string{"max_backups", "retention_days"}),
			stringSet([]string{"level", "output", "dir", "max_file_size"}))
	}
	if applyErr == nil && len(update.Permissions) > 0 {
		applyErr = mergePermissions(ensureObject(current, "permissions"), update.Permissions)
	}
	if applyErr == nil && len(update.Knowledge) > 0 {
		applyErr = mergeFields(ensureObject(current, "knowledge"), "knowledge", update.Knowledge,
			nil, stringSet([]string{"summary_model"}))
	}
	if applyErr == nil && len(update.Tools) > 0 {
		applyErr = mergeTools(current, update.Tools)
	}
	if applyErr == nil && len(update.Env) > 0 {
		applyErr = mergeEnv(ensureObject(current, "env"), update.Env)
	}
	if applyErr != nil {
		http.Error(w, applyErr.Error(), http.StatusBadRequest)
		return
	}

	encoded, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.files.Write(settingsConfigPath, append(encoded, '\n')); err != nil {
		http.Error(w, "writing settings.json failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	response := h.configSnapshot(r)
	response["updated"] = true
	response["restart_required"] = true
	writeJSON(w, response)
}

func (h *Handler) readSettings() (map[string]any, error) {
	if h.files == nil {
		return nil, nil
	}
	data, err := h.files.Read(settingsConfigPath)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	parsed := map[string]any{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		// Writing over a file we cannot parse would destroy whatever it holds.
		return nil, err
	}
	return parsed, nil
}

func ensureObject(source map[string]any, key string) map[string]any {
	if object, ok := source[key].(map[string]any); ok {
		return object
	}
	object := map[string]any{}
	source[key] = object
	return object
}

// mergeFields applies one section against two whitelists: integer keys and
// string keys. Anything else is a caller error, not something to write.
func mergeFields(target map[string]any, section string, updates map[string]json.RawMessage,
	intFields, stringFields map[string]bool) error {
	for _, key := range sortedRawKeys(updates) {
		switch {
		case intFields[key]:
			value, err := decodeInt(updates[key])
			if err != nil || value <= 0 {
				return fmt.Errorf("%s.%s must be a positive integer", section, key)
			}
			target[key] = value
		case stringFields[key]:
			value, err := decodeString(updates[key])
			if err != nil {
				return fmt.Errorf("%s.%s must be a string", section, key)
			}
			target[key] = value
		default:
			return fmt.Errorf("%s.%s is not an editable setting", section, key)
		}
	}
	return nil
}

func mergePermissions(target map[string]any, updates map[string]json.RawMessage) error {
	for _, key := range sortedRawKeys(updates) {
		if !containsString(permissionFields, key) {
			return fmt.Errorf("permissions.%s is not an editable setting", key)
		}
		value, err := decodeString(updates[key])
		if err != nil {
			return fmt.Errorf("permissions.%s must be allow, ask or deny", key)
		}
		switch value {
		case "allow", "ask", "deny":
			target[key] = value
		default:
			return fmt.Errorf("permissions.%s must be allow, ask or deny", key)
		}
	}
	return nil
}

// mergeTools takes the REPL block and the bash phrase list. Only the REPL tool
// is accepted by name: adding a tool here would produce a settings entry with
// no implementation behind it.
func mergeTools(settings map[string]any, updates map[string]json.RawMessage) error {
	tools := ensureObject(settings, "tools")
	for _, name := range sortedRawKeys(updates) {
		switch name {
		case "restricted_bash_phrases":
			phrases, err := decodeStringList(updates[name])
			if err != nil {
				return fmt.Errorf("tools.restricted_bash_phrases must be a list of strings")
			}
			tools["restricted_bash_phrases"] = phrases
		case "repl":
			fields := map[string]json.RawMessage{}
			if err := json.Unmarshal(updates[name], &fields); err != nil {
				return fmt.Errorf("tool repl must be an object")
			}
			entry := ensureObject(tools, "repl")
			for _, field := range sortedRawKeys(fields) {
				switch field {
				case "enabled":
					value, err := decodeBool(fields[field])
					if err != nil {
						return fmt.Errorf("tool repl.enabled must be true or false")
					}
					entry[field] = value
				case "timeout_ms", "max_steps", "max_output_bytes", "catalog_budget":
					value, err := decodeInt(fields[field])
					if err != nil || value <= 0 {
						return fmt.Errorf("tool repl.%s must be a positive integer", field)
					}
					entry[field] = value
				default:
					return fmt.Errorf("tool repl.%s is not an editable setting", field)
				}
			}
		default:
			return fmt.Errorf("tool %s is not an editable setting", name)
		}
	}
	return nil
}

// mergeEnv takes names and values, and gives nothing back: the edit path is
// write-only on purpose, so a value that is already set never travels to a
// browser just to be shown in a form.
func mergeEnv(target map[string]any, updates map[string]string) error {
	for key, value := range updates {
		name := strings.TrimSpace(key)
		if name == "" {
			return fmt.Errorf("env name must not be empty")
		}
		target[name] = value
	}
	return nil
}

func sortedRawKeys(source map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func decodeInt(raw json.RawMessage) (int, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

func decodeString(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func decodeBool(raw json.RawMessage) (bool, error) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	return value, nil
}

func decodeStringList(raw json.RawMessage) ([]string, error) {
	var value []string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func (h *Handler) readConfigJSON(path string) map[string]any {
	if h.files == nil {
		return nil
	}
	data, err := h.files.Read(path)
	if err != nil {
		return nil
	}
	parsed := map[string]any{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	return parsed
}

func nestedObject(source map[string]any, key string) map[string]any {
	value, _ := source[key].(map[string]any)
	return value
}

func sortedKeys(source map[string]any) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// structMap flattens a config struct into the shape the page reads. The struct
// tags are the contract; marshalling is how this stays in step with them.
func structMap(value any) map[string]any {
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	parsed := map[string]any{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return map[string]any{}
	}
	return parsed
}

func toInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case int:
		return typed, true
	}
	return 0, false
}

// modelSummary lists what can be selected. base_url and api_key never leave the
// process: one names an endpoint the operator did not publish, the other is a
// credential.
func modelSummary(models map[string]any) map[string]any {
	summary := map[string]any{}
	if name, ok := models["model"].(string); ok {
		summary["default"] = name
	}
	list, _ := models["models"].([]any)
	available := make([]string, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := entry["id"].(string)
		if id == "" {
			id, _ = entry["name"].(string)
		}
		if id != "" {
			available = append(available, id)
		}
	}
	sort.Strings(available)
	summary["available"] = available
	return summary
}
