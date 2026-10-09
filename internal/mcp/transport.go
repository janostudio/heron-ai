package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// maxLineBytes bounds one stdio message. Tool results can be large
	// (a file read through an MCP server is one line), so this is generous;
	// beyond it the scanner errors and the transport closes rather than
	// silently truncating a response into an unparseable fragment.
	maxLineBytes = 8 << 20
	// maxHTTPBodyBytes bounds one HTTP response body.
	maxHTTPBodyBytes = 32 << 20
	// stderrTailBytes is how much of a child's stderr is kept for error
	// messages.
	stderrTailBytes = 4096
	// terminateGrace is how long a child gets to exit after SIGTERM before it
	// is SIGKILLed.
	terminateGrace = 5 * time.Second
)

// Transport carries JSON-RPC 2.0 messages to one MCP server.
//
// Two implementations exist: stdio (a child process, framed as
// newline-delimited JSON) and http (one POST per request). The deprecated SSE
// transport — a long-lived GET event stream plus a separate POST endpoint —
// is deliberately NOT implemented: a config asking for it fails with an
// explicit error at connect time instead of quietly falling back to something
// that half works.
type Transport interface {
	// Start brings the transport up. For stdio this spawns the child; for
	// http it only validates the URL.
	Start(ctx context.Context) error
	// Send round-trips one request and returns its response.
	Send(ctx context.Context, req Request) (*Response, error)
	// Notify sends a request that expects no response.
	Notify(ctx context.Context, n Notification) error
	// Close releases the transport, including any child process.
	Close() error
}

// tailBuffer keeps the last bytes a child wrote to stderr.
//
// A server that fails to start almost always says why on stderr and exits
// immediately. Without this the only symptom reaching the operator is "EOF on
// stdout", which says nothing about the missing API token or the module that
// failed to install.
type tailBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > stderrTailBytes {
		b.data = append(b.data[:0], b.data[len(b.data)-stderrTailBytes:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}

// StdioTransport runs an MCP server as a child process and speaks
// newline-delimited JSON over its stdin/stdout.
type StdioTransport struct {
	Command string
	Args    []string
	Env     map[string]string

	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	pending   map[int64]chan *Response
	done      chan struct{}
	once      sync.Once
	closeErr  error
	writeMu   sync.Mutex
	stderrBuf *tailBuffer
}

// NewStdioTransport builds a transport for `command args...`.
func NewStdioTransport(command string, args []string, env map[string]string) *StdioTransport {
	return &StdioTransport{
		Command:   command,
		Args:      args,
		Env:       env,
		pending:   make(map[int64]chan *Response),
		done:      make(chan struct{}),
		stderrBuf: &tailBuffer{},
	}
}

func (t *StdioTransport) Start(_ context.Context) error {
	if strings.TrimSpace(t.Command) == "" {
		return errors.New("mcp stdio transport: command is empty")
	}
	if _, err := exec.LookPath(t.Command); err != nil {
		return fmt.Errorf("mcp stdio transport: %w", err)
	}
	cmd := exec.Command(t.Command, t.Args...)
	setProcessGroup(cmd)
	cmd.Env = t.environment()
	cmd.Stderr = t.stderrBuf
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp stdio transport: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mcp stdio transport: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcp stdio transport: start %q: %w", t.Command, err)
	}
	t.mu.Lock()
	t.cmd = cmd
	t.stdin = stdin
	t.mu.Unlock()
	// The reader owns the transport's liveness: when the server closes stdout
	// the transport is finished, whether or not anyone called Close.
	go t.readLoop(stdout)
	return nil
}

// environment layers the configured env over the parent's.
//
// The merge has to collapse duplicates rather than append them. Go resolves a
// duplicated environment key to its FIRST occurrence and blanks the later
// ones, so `append(os.Environ(), "TOKEN=x")` does not override TOKEN at all —
// it is silently ignored. An MCP server's env is almost entirely credentials,
// and a config whose GITHUB_TOKEN loses to whatever the shell happened to
// export is a config that looks applied and is not.
func (t *StdioTransport) environment() []string {
	if len(t.Env) == 0 {
		return os.Environ()
	}
	merged := make(map[string]string, len(t.Env)+16)
	for _, entry := range os.Environ() {
		if i := strings.IndexByte(entry, '='); i > 0 {
			merged[entry[:i]] = entry[i+1:]
		}
	}
	for name, value := range t.Env {
		merged[name] = value
	}
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)
	env := make([]string, 0, len(names))
	for _, name := range names {
		env = append(env, name+"="+merged[name])
	}
	return env
}

func (t *StdioTransport) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			// Servers do write non-JSON chatter to stdout (an npm warning, a
			// deprecation notice). Skipping the line keeps the framing
			// aligned; resynchronising inside the JSON stream is not possible
			// without knowing where the next object starts.
			continue
		}
		if resp.ID == 0 {
			// A server-to-client request or notification. This client
			// implements no server-initiated method, so it is dropped rather
			// than answered with "method not found".
			continue
		}
		t.deliver(resp.ID, &resp)
	}
	_ = t.Close()
}

func (t *StdioTransport) deliver(id int64, resp *Response) {
	t.mu.Lock()
	ch := t.pending[id]
	delete(t.pending, id)
	t.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}

func (t *StdioTransport) forget(id int64) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

func (t *StdioTransport) Send(ctx context.Context, req Request) (*Response, error) {
	t.mu.Lock()
	if t.stdin == nil {
		t.mu.Unlock()
		return nil, errors.New("mcp stdio transport: not started")
	}
	ch := make(chan *Response, 1)
	t.pending[req.ID] = ch
	done := t.done
	stdin := t.stdin
	t.mu.Unlock()

	line, err := json.Marshal(req)
	if err != nil {
		t.forget(req.ID)
		return nil, fmt.Errorf("mcp stdio transport: encode request: %w", err)
	}
	line = append(line, '\n')
	t.writeMu.Lock()
	_, writeErr := stdin.Write(line)
	t.writeMu.Unlock()
	if writeErr != nil {
		t.forget(req.ID)
		return nil, fmt.Errorf("mcp stdio transport: write to %q: %w", t.Command, writeErr)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp, nil
	case <-done:
		t.forget(req.ID)
		return nil, t.goneError()
	case <-ctx.Done():
		t.forget(req.ID)
		return nil, ctx.Err()
	}
}

func (t *StdioTransport) Notify(_ context.Context, n Notification) error {
	t.mu.Lock()
	stdin := t.stdin
	t.mu.Unlock()
	if stdin == nil {
		return errors.New("mcp stdio transport: not started")
	}
	line, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("mcp stdio transport: encode notification: %w", err)
	}
	line = append(line, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := stdin.Write(line); err != nil {
		return fmt.Errorf("mcp stdio transport: write to %q: %w", t.Command, err)
	}
	return nil
}

// goneError explains a request that outlived the server. The stderr tail is
// attached because "the server exited" alone is not actionable.
func (t *StdioTransport) goneError() error {
	t.mu.Lock()
	closeErr := t.closeErr
	t.mu.Unlock()
	msg := fmt.Sprintf("mcp stdio transport: server %q exited", t.Command)
	if closeErr != nil && !errors.Is(closeErr, context.Canceled) {
		msg = fmt.Sprintf("%s: %v", msg, closeErr)
	}
	if tail := t.stderrBuf.String(); tail != "" {
		msg = fmt.Sprintf("%s (stderr: %s)", msg, tail)
	}
	return errors.New(msg)
}

func (t *StdioTransport) Close() error {
	t.once.Do(func() {
		t.mu.Lock()
		close(t.done)
		stdin := t.stdin
		cmd := t.cmd
		t.stdin = nil
		t.mu.Unlock()
		if stdin != nil {
			_ = stdin.Close()
		}
		t.mu.Lock()
		t.closeErr = reap(cmd)
		t.mu.Unlock()
	})
	t.mu.Lock()
	defer t.mu.Unlock()
	// A child we just signalled always exits as "signal: terminated", and a
	// child that had already crashed exits non-zero. Neither is a failure to
	// close; the status is kept for goneError, which is where it is useful.
	var exitErr *exec.ExitError
	if errors.As(t.closeErr, &exitErr) {
		return nil
	}
	return t.closeErr
}

// reap stops a child and waits for it. Blocking here is the point: Close does
// not return until the process group is gone, so a caller that exits right
// after Close leaves nothing behind.
func reap(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// A failure to signal the group is not fatal: Wait still returns, and its
	// error is what the caller sees.
	_ = terminateProcess(cmd)
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		return err
	case <-time.After(terminateGrace):
		_ = killProcess(cmd)
		return <-waited
	}
}

// HTTPTransport speaks MCP over Streamable HTTP: one POST per request.
type HTTPTransport struct {
	URL     string
	Headers map[string]string
	Client  *http.Client

	mu        sync.Mutex
	sessionID string
	closed    bool
}

// NewHTTPTransport builds a transport for an MCP HTTP endpoint.
func NewHTTPTransport(target string, headers map[string]string, client *http.Client) *HTTPTransport {
	return &HTTPTransport{URL: target, Headers: headers, Client: client}
}

func (t *HTTPTransport) Start(_ context.Context) error {
	if strings.TrimSpace(t.URL) == "" {
		return errors.New("mcp http transport: url is empty")
	}
	parsed, err := url.Parse(t.URL)
	if err != nil {
		return fmt.Errorf("mcp http transport: invalid url %q: %w", t.URL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("mcp http transport: url %q must be http or https, got %q", t.URL, parsed.Scheme)
	}
	return nil
}

func (t *HTTPTransport) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return http.DefaultClient
}

func (t *HTTPTransport) Send(ctx context.Context, req Request) (*Response, error) {
	if t.isClosed() {
		return nil, errors.New("mcp http transport: closed")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp http transport: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcp http transport: build request: %w", err)
	}
	t.applyHeaders(httpReq)
	res, err := t.client().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mcp http transport: %s %s: %w", req.Method, t.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp http transport: %s %s: server returned %s: %s",
			req.Method, t.URL, res.Status, readSnippet(res.Body))
	}
	t.rememberSession(res)
	msg, err := readResponseMessage(res)
	if err != nil {
		return nil, err
	}
	if msg.Error != nil {
		return nil, msg.Error
	}
	return msg, nil
}

func (t *HTTPTransport) Notify(ctx context.Context, n Notification) error {
	if t.isClosed() {
		return errors.New("mcp http transport: closed")
	}
	body, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("mcp http transport: encode notification: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mcp http transport: build request: %w", err)
	}
	t.applyHeaders(httpReq)
	res, err := t.client().Do(httpReq)
	if err != nil {
		return fmt.Errorf("mcp http transport: %s %s: %w", n.Method, t.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	// A notification is best-effort: a server that does not implement
	// notifications/initialized may answer 400, and that must not fail a
	// handshake that otherwise succeeded.
	return nil
}

func (t *HTTPTransport) applyHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	// Streamable HTTP servers may answer a POST with either shape; asking for
	// both is what the protocol's content negotiation expects.
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	t.mu.Lock()
	session := t.sessionID
	t.mu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	for name, value := range t.Headers {
		req.Header.Set(name, value)
	}
}

func (t *HTTPTransport) rememberSession(res *http.Response) {
	id := res.Header.Get("Mcp-Session-Id")
	if id == "" {
		return
	}
	t.mu.Lock()
	t.sessionID = id
	t.mu.Unlock()
}

func (t *HTTPTransport) Close() error {
	t.mu.Lock()
	t.closed = true
	client := t.Client
	t.mu.Unlock()
	if client != nil {
		client.CloseIdleConnections()
	}
	return nil
}

func (t *HTTPTransport) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// readResponseMessage decodes one JSON-RPC message from a response body,
// accepting either a plain JSON body or an SSE-framed one.
func readResponseMessage(res *http.Response) (*Response, error) {
	body := io.LimitReader(res.Body, maxHTTPBodyBytes)
	if strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream") {
		return readSSEMessage(body)
	}
	var msg Response
	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		return nil, fmt.Errorf("mcp http transport: decode response: %w", err)
	}
	return &msg, nil
}

// readSSEMessage takes the first JSON-RPC message out of an SSE body.
//
// This is NOT the deprecated SSE transport. That one opens a long-lived GET
// stream and posts to a separate endpoint returned by it; it is unsupported
// and a config asking for it is rejected. What is handled here is the
// streamable-HTTP rule that a POST may answer `Content-Type:
// text/event-stream` with one or more `data:` events, which every server
// built on the current SDKs does.
func readSSEMessage(r io.Reader) (*Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxHTTPBodyBytes)
	var event bytes.Buffer
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if event.Len() == 0 {
				continue
			}
			var msg Response
			if err := json.Unmarshal(event.Bytes(), &msg); err == nil {
				return &msg, nil
			}
			event.Reset()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			event.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			event.WriteByte('\n')
		}
	}
	if event.Len() > 0 {
		var msg Response
		if err := json.Unmarshal(event.Bytes(), &msg); err == nil {
			return &msg, nil
		}
	}
	return nil, errors.New("mcp http transport: no JSON-RPC message in SSE response")
}

// readSnippet returns a short prefix of an error body so an HTTP failure
// carries something more useful than a status code.
func readSnippet(r io.Reader) string {
	limited, err := io.ReadAll(io.LimitReader(r, 512))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(limited))
	if text == "" {
		return "(empty body)"
	}
	return text
}
