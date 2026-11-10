//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// env is one running engine instance over one throw-away workspace.
//
// Every case gets its own env: separate workspace, separate port, separate
// mock. Sharing any of the three makes a failure depend on which case ran
// first, and the point of this package is that a failure names one contract
// that is not honoured.
type env struct {
	t       *testing.T
	Root    string // workspace root; also the server's cwd
	BaseURL string
	Token   string
	Mock    *mockModel
	Case    string
	Intent  string

	streamModel bool
	args        []string
	port        string
	cmd         *exec.Cmd
	stdout      *os.File
	stderr      *os.File
	logPath     string
	runDir      string
}

type envOptions struct {
	caseName string
	intent   string
	// bindHost overrides the listen address (used to observe the refusal of an
	// exposed bind).
	bindHost string
	// noToken starts the server without a bearer token.
	noToken bool
	token   string
	// logLevel writes .agents/settings.json with that logging level. Empty
	// leaves the engine on its default (info), which is what a deployment runs.
	logLevel string
	// streamModel makes the generated model profile ask for streaming, which is
	// what the streaming cases need to exercise at all.
	streamModel bool
	// scriptEnabled turns on settings.json tools.script and adds Script to the
	// fixture agent's builtin list: the tool has to be both enabled and
	// declared, and a missing half fails silently (the model simply never sees
	// the tool).
	scriptEnabled bool
	// extraTools declares additional builtin tools on the fixture agent. Some
	// tools have no auto-grant, so an agent that does not list them never sees
	// them and the case would fail for a configuration reason.
	extraTools []string
	// fixture selects a committed workspace under testdata/. Empty means the
	// simple-qa example (single team); "dispatch" is the two-team topology used
	// for the dispatch and mailbox cases.
	fixture string
}

func newEnv(t *testing.T, opts envOptions) *env {
	t.Helper()

	e := &env{
		t:      t,
		Case:   firstNonEmpty(opts.caseName, t.Name()),
		Intent: opts.intent,
		Root:   t.TempDir(),
	}
	if !opts.noToken {
		e.Token = firstNonEmpty(opts.token, "e2e-token")
	}
	e.streamModel = opts.streamModel
	e.Mock = newMockModel(t)

	if opts.fixture != "" {
		source := filepath.Join(repoRootOrFail(t), "test", "e2e", "testdata", opts.fixture)
		if err := copyTree(source, e.Root); err != nil {
			t.Fatalf("copy fixture %q: %v", opts.fixture, err)
		}
	} else {
		if err := copyTree(filepath.Join(repoRootOrFail(t), "examples", "simple-qa", ".agents"), filepath.Join(e.Root, ".agents")); err != nil {
			t.Fatalf("copy fixture: %v", err)
		}
		// The example's project files are the workspace the agents read from; a
		// tool case that reads package.json needs them present at the cwd.
		if err := copyTree(filepath.Join(repoRootOrFail(t), "examples", "simple-qa", "project"), e.Root); err != nil {
			t.Fatalf("copy fixture project: %v", err)
		}
	}
	// Sessions from the example are development leftovers, not fixture state.
	_ = os.RemoveAll(filepath.Join(e.Root, ".agents", "data"))

	if err := e.writeModels(); err != nil {
		t.Fatalf("write models.json: %v", err)
	}
	if opts.logLevel != "" || opts.scriptEnabled {
		if err := e.writeSettings(opts.logLevel, opts.scriptEnabled); err != nil {
			t.Fatalf("write settings.json: %v", err)
		}
	}
	if opts.scriptEnabled {
		// The fixture declares the current name. The cases that call it by the
		// old name ("Script") are the alias coverage: they must keep executing.
		if err := ensureBuiltinTool(filepath.Join(e.Root, ".agents", "agents", "assistant", "AGENT.md"), "REPL"); err != nil {
			t.Fatalf("declare REPL in fixture agent: %v", err)
		}
	}
	for _, name := range opts.extraTools {
		if err := ensureBuiltinTool(filepath.Join(e.Root, ".agents", "agents", "assistant", "AGENT.md"), name); err != nil {
			t.Fatalf("declare %s in fixture agent: %v", name, err)
		}
	}

	e.logPath = filepath.Join(e.Root, ".agents", "data", "logs")
	port := freePort(t)
	e.BaseURL = "http://127.0.0.1:" + port

	args := []string{"--serve", "--port", port, "--host", firstNonEmpty(opts.bindHost, "127.0.0.1")}
	if e.Token != "" {
		args = append(args, "--auth-token", e.Token)
	}
	args = append(args, "--flow", ".agents/flows/default.yml")

	e.args = args
	e.port = port
	e.cmd = exec.Command(binPath, args...)
	e.cmd.Dir = e.Root
	output := &lockedBuffer{}
	e.cmd.Stdout = output
	e.cmd.Stderr = output
	e.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := e.cmd.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	e.stdout = nil
	e.stderr = nil

	t.Cleanup(func() {
		e.stop()
		e.dump(output.String())
	})

	if opts.noToken || opts.bindHost != "" {
		// This env only observes the startup refusal; there is nothing to wait
		// for and waiting would turn a refusal into a timeout.
		return e
	}
	e.waitReady()
	return e
}

// writeModels generates .agents/models.json. The file is gitignored upstream,
// so it can only exist at run time; one profile per model name the fixture
// agents reference, all pointed at the mock.
func (e *env) writeModels() error {
	names, err := fixtureModelNames(filepath.Join(e.Root, ".agents", "agents"))
	if err != nil {
		return err
	}
	if len(names) == 0 {
		names = []string{"mock"}
	}
	baseURL := e.Mock.URL + "/v1"
	profiles := make([]map[string]any, 0, len(names))
	for _, name := range names {
		profiles = append(profiles, map[string]any{
			"id":               name,
			"name":             name,
			"base_url":         baseURL,
			"api_key":          "e2e-key",
			"protocol":         "openai_chat",
			"stream":           e.streamModel,
			"maxInputTokens":   100000,
			"maxOutputTokens":  4096,
			"supportsToolCall": true,
		})
	}
	payload := map[string]any{"model": names[0], "models": profiles}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(e.Root, ".agents", "models.json"), data, 0o600)
}

var modelLine = regexp.MustCompile(`(?m)^\s*model:\s*([A-Za-z0-9._-]+)\s*$`)

// writeSettings pins the process log level and the Script switch, keeping them
// in the workspace the way a deployment configures them.
func (e *env) writeSettings(level string, scriptEnabled bool) error {
	settings := map[string]any{}
	if level != "" {
		settings["logging"] = map[string]any{
			"level":  level,
			"output": "file",
			"dir":    ".agents/data/logs",
		}
	}
	if scriptEnabled {
		settings["tools"] = map[string]any{"repl": map[string]any{"enabled": true}}
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(e.Root, ".agents", "settings.json"), data, 0o600)
}

// ensureBuiltinTool adds a name to the AGENT.md tools.builtin list when it is
// absent. Script has no auto-grant (unlike Collect for Team/Spawn), so an agent
// that does not declare it never sees it — and nothing reports the omission.
func ensureBuiltinTool(agentFile, name string) error {
	data, err := os.ReadFile(agentFile)
	if err != nil {
		return err
	}
	text := string(data)
	if strings.Contains(text, "- "+name+"\n") {
		return nil
	}
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "builtin:" {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))] + "  "
		insert := make([]string, 0, len(lines)+1)
		insert = append(insert, lines[:index+1]...)
		insert = append(insert, indent+"- "+name)
		insert = append(insert, lines[index+1:]...)
		return os.WriteFile(agentFile, []byte(strings.Join(insert, "\n")), 0o644)
	}
	return fmt.Errorf("%s has no tools.builtin block", agentFile)
} // fixtureModelNames lists the model names declared by the fixture agents, so
// models.json matches whatever the fixture asks for instead of hard-coding a
// name that drifts.
func fixtureModelNames(agentsDir string) ([]string, error) {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, entry := range entries {
		path := filepath.Join(agentsDir, entry.Name(), "AGENT.md")
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		for _, match := range modelLine.FindAllStringSubmatch(string(data), -1) {
			name := strings.TrimSpace(match[1])
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

func (e *env) waitReady() {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		body, status, err := e.do(http.MethodGet, "/api/health", nil)
		if err == nil && status == http.StatusOK {
			var health struct {
				Ready bool `json:"ready"`
			}
			if json.Unmarshal(body, &health) == nil && health.Ready {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("engine did not become ready within 20s (health=%v)", e.healthSnapshot())
}

func (e *env) healthSnapshot() string {
	body, status, err := e.do(http.MethodGet, "/api/health", nil)
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("status=%d body=%s", status, strings.TrimSpace(string(body)))
}

func (e *env) stop() {
	if e.cmd == nil || e.cmd.Process == nil {
		return
	}
	_ = e.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- e.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-e.cmd.Process.Pid, syscall.SIGKILL)
		<-done
		e.t.Logf("engine needed SIGKILL; child processes may have leaked")
	}
	// A tool that spawned a child can outlive the server; the process group is
	// what catches it, so kill the group even after a clean exit.
	_ = syscall.Kill(-e.cmd.Process.Pid, syscall.SIGKILL)
}

// dump copies the run's evidence next to the report when a case fails. The
// rule for this package is that a failure must be diagnosable from artifacts
// alone, without re-running the case — so a failing case writes a trace index
// too, not just the cases that got far enough to call writeTrace.
func (e *env) dump(serverOutput string) {
	// Failures always keep their evidence. HERON_E2E_KEEP_ALL=1 keeps it for
	// passing cases too, which is what makes a run's data analysable after the
	// fact instead of only when something went red.
	if !e.t.Failed() && os.Getenv("HERON_E2E_KEEP_ALL") != "1" {
		return
	}
	dir := filepath.Join(runDir(e.t), "artifacts", sanitize(e.Case))
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "server.log"), []byte(serverOutput), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "model-script.log"), []byte(e.Mock.summary()), 0o644)
	_ = copyTree(filepath.Join(e.Root, ".agents", "data"), filepath.Join(dir, "data"))

	sessions := map[string]any{}
	for _, sessionID := range sessionDirs(e.t, e.Root) {
		events := readEventsIn(e.t, filepath.Join(e.Root, ".agents", "data", "sessions", sessionID))
		files := map[string][]int{}
		for _, ev := range events {
			files[ev.File] = append(files[ev.File], ev.Line)
		}
		sessions[sessionID] = map[string]any{"events": len(events), "files": files, "last_type": lastType(events)}
	}
	tracePath := filepath.Join(dir, "trace-index.json")
	if _, statErr := os.Stat(tracePath); statErr == nil {
		// The case already wrote a trace with its own findings; keep both
		// rather than letting the dump overwrite the richer one.
		tracePath = filepath.Join(dir, "dump-index.json")
	}
	_ = os.WriteFile(tracePath, mustJSON(map[string]any{
		"case":     e.Case,
		"intent":   e.Intent,
		"base_url": e.BaseURL,
		"root":     e.Root,
		"binary":   binPath,
		"sessions": sessions,
	}), 0o644)
}

func lastType(events []event) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].Type
}

// killAndRestart hard-kills the engine (simulating a crash) and starts a new
// one on the same workspace and port. Recovery has no other way to be tested:
// a graceful stop is not an interruption.
func (e *env) killAndRestart() {
	e.t.Helper()
	_ = syscall.Kill(-e.cmd.Process.Pid, syscall.SIGKILL)
	_, _ = e.cmd.Process.Wait()

	e.cmd = exec.Command(binPath, e.args...)
	e.cmd.Dir = e.Root
	output := &lockedBuffer{}
	e.cmd.Stdout = output
	e.cmd.Stderr = output
	e.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := e.cmd.Start(); err != nil {
		e.t.Fatalf("restart engine: %v", err)
	}
	e.waitReady()
}

// --- HTTP helpers ---

func (e *env) do(method, path string, body any) ([]byte, int, error) {
	return e.doWithToken(method, path, e.Token, body)
}

// doWithToken lets a case send a wrong or missing credential without a second
// env: the auth promise is about the same running instance, not a new one.
func (e *env) doWithToken(method, path, token string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, e.BaseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

func (e *env) doRaw(method, path, contentType string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequest(method, e.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if e.Token != "" {
		req.Header.Set("Authorization", "Bearer "+e.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

// runTurn starts or continues a turn and returns the decoded result.
func (e *env) runTurn(payload map[string]any) turnResult {
	e.t.Helper()
	body, status, err := e.do(http.MethodPost, "/api/run", payload)
	if err != nil {
		e.t.Fatalf("POST /api/run: %v", err)
	}
	if status != http.StatusOK {
		e.t.Fatalf("POST /api/run: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}
	var result turnResult
	if err := json.Unmarshal(body, &result); err != nil {
		e.t.Fatalf("decode run result: %v (body=%s)", err, strings.TrimSpace(string(body)))
	}
	return result
}

// turnOutcome is the result of a turn started off the test goroutine.
type turnOutcome struct {
	Result turnResult
	Status int
	Body   []byte
	Err    error
}

// runTurnAsync starts a turn without blocking the case, so the case can be
// subscribed before the turn's events exist. It never calls t.Fatalf: the
// failure must surface on the test goroutine.
func (e *env) runTurnAsync(payload map[string]any) <-chan turnOutcome {
	out := make(chan turnOutcome, 1)
	go func() {
		body, status, err := e.do(http.MethodPost, "/api/run", payload)
		outcome := turnOutcome{Status: status, Body: body, Err: err}
		if err == nil && status == http.StatusOK {
			_ = json.Unmarshal(body, &outcome.Result)
		}
		out <- outcome
	}()
	return out
}

// turnResult mirrors the fields this package judges. It deliberately keeps
// only the contract surface: adding a field here means a case needs it.
type turnResult struct {
	Session struct {
		ID     string `json:"id"`
		FlowID string `json:"flow_id"`
		Status string `json:"status"`
	} `json:"session"`
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turn"`
	Reply            string            `json:"reply"`
	FinalReply       string            `json:"final_reply"`
	FinalReplyTeam   string            `json:"final_reply_team"`
	Queued           bool              `json:"queued"`
	Deliveries       []any             `json:"deliveries"`
	PendingApprovals []pendingApproval `json:"pending_approvals"`
	PendingToolTasks []any             `json:"pending_tool_tasks"`
	TeamResults      []any             `json:"team_results"`
}

// pendingApproval is the handle a client needs to answer an approval; without
// the request id there is no way to resume the turn.
type pendingApproval struct {
	RequestID  string `json:"request_id"`
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id"`
	Reason     string `json:"reason"`
}

// --- process / fs helpers ---

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	return port
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, readErr := os.ReadFile(src)
		if readErr != nil {
			return readErr
		}
		if mkErr := os.MkdirAll(filepath.Dir(dst), 0o755); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(dst, data, info.Mode())
	}
	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyTree(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func repoRootOrFail(t *testing.T) string {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return root
}

func artifactRoot(t *testing.T) string {
	if run := os.Getenv("HERON_E2E_RUN_DIR"); run != "" {
		return run
	}
	root := os.Getenv("HERON_E2E_ARTIFACTS")
	if root == "" {
		// Outside the checkout on purpose: a test run must not dirty the
		// worktree it is judging. Runners pass HERON_E2E_RUN_DIR to publish
		// results somewhere durable.
		root = filepath.Join(os.TempDir(), "heron-e2e-reports")
	}
	return filepath.Join(root, time.Now().Format("2006-01-02-150405"))
}

func sanitize(name string) string {
	replacer := strings.NewReplacer("/", "_", " ", "_", "|", "_")
	return replacer.Replace(name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func mustJSON(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return data
}

// lockedBuffer collects the engine's stdout/stderr. The copy goroutines write
// while a failing case reads it, so the buffer needs its own lock — with -race
// the unlocked version trips a data race inside the harness, not the engine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(data)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}
