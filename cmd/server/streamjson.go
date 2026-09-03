package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/heron-ai/heron-engine/internal/media"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// streamJSONInput is intentionally compatible with the common CLI shape:
// user messages use {type:"user",message:{role:"user",content:...}} and
// permission responses are explicit control messages.
type streamJSONInput struct {
	Type       string          `json:"type"`
	SessionID  string          `json:"session_id,omitempty"`
	Input      string          `json:"input,omitempty"`
	Message    json.RawMessage `json:"message,omitempty"`
	ApprovalID string          `json:"approval_id,omitempty"`
	Approved   bool            `json:"approved,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	ApproverID string          `json:"approver_id,omitempty"`
	Approver   string          `json:"approver,omitempty"`
	Channel    string          `json:"channel,omitempty"`
}

func runStreamJSONClient(flowPath, serverURL string) {
	_ = flowPath // The server owns Flow configuration; retain flag symmetry.
	client := &streamJSONClient{
		baseURL: strings.TrimRight(strings.TrimSpace(serverURL), "/"),
		session: "",
		out:     bufio.NewWriter(os.Stdout),
	}
	if err := client.Serve(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "stream-json client error:", err)
		os.Exit(1)
	}
}

type streamJSONClient struct {
	baseURL string
	session string
	out     *bufio.Writer
}

func (c *streamJSONClient) Serve(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var input streamJSONInput
		if err := json.Unmarshal([]byte(line), &input); err != nil {
			if err := c.write(map[string]any{
				"type": "error", "subtype": "parse_error", "error": err.Error(),
			}); err != nil {
				return err
			}
			continue
		}
		if input.SessionID != "" {
			c.session = input.SessionID
		}
		switch input.Type {
		case "user":
			text, parts, parseErr := parseStreamJSONContent(input.Message)
			if parseErr != nil {
				if writeErr := c.write(map[string]any{"type": "error", "subtype": "invalid_content", "error": parseErr.Error()}); writeErr != nil {
					return writeErr
				}
				continue
			}
			if text == "" {
				text = input.Input
			}
			if strings.TrimSpace(text) == "" && len(parts) == 0 {
				return c.write(map[string]any{"type": "error", "subtype": "invalid_input", "error": "user input or content is required"})
			}
			content := make([]any, 0, len(parts)+1)
			if text != "" {
				content = append(content, map[string]any{"type": "text", "text": text})
			}
			for _, part := range parts {
				content = append(content, part)
			}
			var payload any
			if len(content) == 1 && text != "" {
				payload = text
			} else if len(content) > 0 {
				payload = content
			}
			body := map[string]any{"flow_session_id": c.session, "input": text}
			if payload != nil {
				body["content"] = payload
			}
			result, err := c.post("/api/run", body)
			if err != nil {
				if writeErr := c.write(map[string]any{"type": "error", "subtype": "request_failed", "error": err.Error()}); writeErr != nil {
					return writeErr
				}
				continue
			}
			if err := c.writeResult(result); err != nil {
				return err
			}
		case "permission_response", "approval":
			if c.session == "" || input.ApprovalID == "" {
				return c.write(map[string]any{"type": "error", "subtype": "invalid_approval", "error": "session_id and approval_id are required"})
			}
			result, err := c.post("/api/approvals?session_id="+urlQueryEscape(c.session), map[string]any{
				"approval_id": input.ApprovalID, "approved": input.Approved,
				"reason": input.Reason, "approver_id": input.ApproverID,
				"approver": input.Approver, "channel": input.Channel,
			})
			if err != nil {
				return c.write(map[string]any{"type": "error", "subtype": "approval_failed", "error": err.Error()})
			}
			if err := c.writeResult(result); err != nil {
				return err
			}
		case "resume", "poll":
			if c.session == "" {
				return c.write(map[string]any{"type": "error", "subtype": "invalid_resume", "error": "session_id is required"})
			}
			result, err := c.get("/api/result?session_id=" + urlQueryEscape(c.session))
			if err != nil {
				return c.write(map[string]any{"type": "error", "subtype": "resume_failed", "error": err.Error()})
			}
			if err := c.writeResult(result); err != nil {
				return err
			}
		default:
			if err := c.write(map[string]any{"type": "error", "subtype": "unsupported_input", "error": "unsupported stream-json input type: " + input.Type}); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func parseStreamJSONContent(raw json.RawMessage) (string, []types.ContentPart, error) {
	return media.ParseMessageContent(raw)
}

func (c *streamJSONClient) writeResult(raw json.RawMessage) error {
	var result types.FlowTurnResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.Session.ID != "" {
		c.session = result.Session.ID
	}
	subtype := "success"
	switch result.Session.Status {
	case types.SessionWaitingApproval:
		subtype = "permission_required"
	case types.SessionWaitingInput:
		subtype = "input_required"
	case types.SessionWaitingTool:
		subtype = "tool_waiting"
	case types.SessionFailed:
		subtype = "failure"
	}
	return c.write(map[string]any{
		"type": "result", "subtype": subtype,
		"session_id": result.Session.ID, "status": result.Session.Status,
		"reply": result.Reply, "error": result.Error,
		"pending_approvals":  result.PendingApprovals,
		"pending_tool_tasks": result.PendingToolTasks,
		"records":            result.Records,
	})
}

func (c *streamJSONClient) post(path string, body any) (json.RawMessage, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c *streamJSONClient) get(path string) (json.RawMessage, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *streamJSONClient) do(req *http.Request) (json.RawMessage, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.RawMessage(body), nil
}

func (c *streamJSONClient) write(value any) error {
	if err := json.NewEncoder(c.out).Encode(value); err != nil {
		return err
	}
	return c.out.Flush()
}

func urlQueryEscape(value string) string {
	replacer := strings.NewReplacer("%", "%25", " ", "%20", "?", "%3F", "&", "%26", "=", "%3D")
	return replacer.Replace(value)
}
