package mcp

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the MCP revision this client speaks on the wire.
//
// The handshake records whatever revision the server answers with but does not
// enforce it: initialize, tools/list and tools/call are byte-identical across
// every revision in circulation, and refusing to talk to a server that answers
// "2025-06-18" would break real servers for no behavioural gain. Renegotiating
// per response is what the protocol asks for, and that negotiation is exactly
// what this constant starts.
const ProtocolVersion = "2024-11-05"

const jsonrpcVersion = "2.0"

// Request is a JSON-RPC 2.0 request that expects a response.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Notification is a JSON-RPC 2.0 request that expects no response. The
// handshake's "notifications/initialized" step is the only one this client
// sends, which is why it has a type of its own rather than being a Request
// with a nil ID.
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response. Exactly one of Result and Error is set
// for a well-formed server.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ErrorObject    `json:"error,omitempty"`
}

// ErrorObject is the JSON-RPC 2.0 error member. It implements error so a
// failed call can be returned directly.
type ErrorObject struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *ErrorObject) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return fmt.Sprintf("MCP error %d", e.Code)
	}
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

// encodeParams marshals a params object once. Marshaling in the caller keeps
// the transport free of any knowledge of individual MCP methods.
func encodeParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode MCP params: %w", err)
	}
	return raw, nil
}
