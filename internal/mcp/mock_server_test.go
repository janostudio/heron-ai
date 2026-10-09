package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The mock MCP server is this test binary re-executed with mockEnv set.
//
// Running a real child process rather than an in-process fake is the point of
// these tests: the stdio transport's contract is "spawn a process, talk to it
// over two pipes, and make sure it is gone afterwards". An in-process fake
// cannot exercise any of that, and the failure this suite exists to prevent —
// a server process outliving the engine — is invisible to one.
const (
	mockEnv        = "HERON_MCP_MOCK"
	mockModeServer = "server"
)

// Environment the mock reads.
const (
	envMockMode     = "HERON_MCP_MOCK_MODE"
	envChildPIDFile = "HERON_MCP_MOCK_CHILD_PID"
)

// Mock modes.
const (
	mockModeExit  = "exit"
	mockModeSlow  = "slow"
	mockModePaged = "paged"
	mockModeNoise = "noise"
)

// mockSlowDelay is long relative to the timeouts the tests set and short
// relative to the suite's patience.
const mockSlowDelay = 1500 * time.Millisecond

// sleeperCommand is the grandchild the mock forks to stand in for the real
// processes an MCP server leaves behind — the node process `npx` execs, a
// helper daemon, a language server. It is a plain command rather than another
// copy of the test binary so "is it still alive?" is a question the test can
// answer with a signal-0 probe on any platform.
var sleeperCommand = []string{"/bin/sleep", "60"}

func TestMain(m *testing.M) {
	if os.Getenv(mockEnv) == mockModeServer {
		runMockServer()
		return
	}
	os.Exit(m.Run())
}

// --- stdio mock server -----------------------------------------------------

func runMockServer() {
	if pidFile := os.Getenv(envChildPIDFile); pidFile != "" {
		if pid := startMockSleeper(); pid > 0 {
			_ = os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0o600)
		}
	}
	mode := os.Getenv(envMockMode)
	if mode == mockModeExit {
		// A server that cannot start says why on stderr and exits. This is the
		// shape the engine has to turn into an actionable error.
		_, _ = os.Stderr.WriteString("mock: refusing to start: MOCK_TOKEN is not set\n")
		os.Exit(3)
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	out := bufio.NewWriter(os.Stdout)
	for in.Scan() {
		line := bytes.TrimSpace(in.Bytes())
		if len(line) == 0 {
			continue
		}
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		if mode == mockModeNoise {
			// Real servers do this: npm warnings and deprecation notices end
			// up on stdout, where they must not desynchronise the framing.
			_, _ = out.WriteString("npm warn deprecated inflight@1.0.6\n")
		}
		switch req.Method {
		case "initialize":
			if mode == mockModeSlow {
				time.Sleep(mockSlowDelay)
			}
			writeMockResult(out, req.ID, mockInitializeResult())
		case "tools/list":
			writeMockResult(out, req.ID, mockToolsListResult(req.Params, mode))
		case "tools/call":
			if mode == mockModeSlow {
				time.Sleep(mockSlowDelay)
			}
			body, rpcErr := mockToolsCallResult(req.Params)
			if rpcErr != nil {
				writeMockError(out, req.ID, rpcErr.Code, rpcErr.Message)
				break
			}
			writeMockResult(out, req.ID, body)
		default:
			if req.ID != 0 {
				writeMockError(out, req.ID, -32601, "method not found: "+req.Method)
			}
		}
	}
}

func mockInitializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "mock", "version": "1.0.0"},
	}
}

func mockTool(name, description string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string", "description": "text to echo"},
				"count":   map[string]any{"type": "integer", "description": "how many times"},
			},
			"required": []any{"message"},
		},
	}
}

func mockToolsListResult(raw json.RawMessage, mode string) map[string]any {
	var params struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(raw, &params)
	if mode == mockModePaged {
		if params.Cursor == "" {
			return map[string]any{
				"tools":      []any{mockTool("echo", "Echo the message")},
				"nextCursor": "page-2",
			}
		}
		return map[string]any{
			"tools": []any{mockTool("summarize", "Summarize the message")},
		}
	}
	return map[string]any{
		"tools": []any{
			mockTool("echo", "Echo the message"),
			mockTool("failing", "Always fails"),
		},
	}
}

func mockToolsCallResult(raw json.RawMessage) (map[string]any, *ErrorObject) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	_ = json.Unmarshal(raw, &params)
	message, _ := params.Arguments["message"].(string)
	switch params.Name {
	case "echo":
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "echo: " + message}},
			"isError": false,
		}, nil
	case "failing":
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "boom: " + message}},
			"isError": true,
		}, nil
	case "summarize":
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "summary of: " + message}},
			"isError": false,
		}, nil
	}
	return nil, &ErrorObject{Code: -32602, Message: "unknown tool: " + params.Name}
}

func writeMockResult(out *bufio.Writer, id int64, result any) {
	raw, err := json.Marshal(map[string]any{"jsonrpc": jsonrpcVersion, "id": id, "result": result})
	if err != nil {
		return
	}
	_, _ = out.Write(append(raw, '\n'))
	_ = out.Flush()
}

func writeMockError(out *bufio.Writer, id int64, code int, message string) {
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": jsonrpcVersion,
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
	if err != nil {
		return
	}
	_, _ = out.Write(append(raw, '\n'))
	_ = out.Flush()
}

// --- grandchild used to prove the whole process group is signalled ----------

// startMockSleeper forks a process the engine knows nothing about.
//
// It is started WITHOUT a process group of its own on purpose: it must inherit
// the mock's group, because the property under test is that closing the adapter
// reaches processes the engine never started itself. Killing only the direct
// child — what exec.Cmd.Kill does by default — leaves this one running for the
// rest of the machine's uptime.
func startMockSleeper() int {
	cmd := exec.Command(sleeperCommand[0], sleeperCommand[1:]...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	if err := cmd.Start(); err != nil {
		return 0
	}
	return cmd.Process.Pid
}

// --- helpers used by the test cases ----------------------------------------

// mockStdioServer returns a config pointing at the mock, so a stdio test
// exercises the real spawn-and-pipe path.
func mockStdioServer(mode string, extraEnv ...string) MCPServer {
	env := map[string]string{mockEnv: mockModeServer, envMockMode: mode}
	for i := 0; i+1 < len(extraEnv); i += 2 {
		env[extraEnv[i]] = extraEnv[i+1]
	}
	exe, err := os.Executable()
	if err != nil {
		panic("mock stdio server: " + err.Error())
	}
	return MCPServer{Name: "mock", Transport: TransportStdio, Command: exe, Env: env}
}

// newMockAdapter builds an adapter that cleans up after the test.
func newMockAdapter(t *testing.T, opts Options) *MCPAdapter {
	t.Helper()
	adapter := NewMCPAdapter(opts)
	t.Cleanup(func() { _ = adapter.Close() })
	return adapter
}

// waitForFile polls for a file to appear and returns its contents.
func waitForFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s did not appear within %s", filepath.Base(path), timeout)
			return ""
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// processGone reports whether a pid no longer exists. Signal 0 delivers
// nothing; ESRCH means the process is gone and has been reaped.
func processGone(pid int) bool {
	return syscall.Kill(pid, syscall.Signal(0)) != nil
}

func mustAtoi(t *testing.T, raw string) int {
	t.Helper()
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return value
}
