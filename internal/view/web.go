package view

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/eval"
	"github.com/heron-ai/heron-engine/internal/sessionlist"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// The page is embedded rather than served from disk: one binary, no asset
// directory to deploy, and no path from a request to the filesystem.
//
//go:embed webassets
var webAssets embed.FS

// SetFileStore wires the read-only history source the record view replays.
//
// It is a setter rather than a constructor argument because the handler is also
// built in tests and by callers that never serve the page; those keep working
// with a nil store and get a clear 503 instead of a panic.
func (h *Handler) SetFileStore(files storage.FileStore) { h.files = files }

// sessionRow is one entry of the session index. It mirrors sessionlist.Session
// but adds the runtime-only counts the page cannot derive from files alone.
type sessionRow struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Turns     int       `json:"turns"`
	Tokens    int64     `json:"tokens"`
	Active    bool      `json:"active"`
	UpdatedAt time.Time `json:"updated_at"`
}

// HandleSessionList returns the session index.
//
// It shares the /api/sessions path with HandleRun (which creates a session):
// the method decides. Splitting the path would give the page a different base
// from the machine API for no benefit.
func (h *Handler) HandleSessionList(w http.ResponseWriter, r *http.Request) {
	if h.files == nil {
		http.Error(w, "session store is not configured", http.StatusServiceUnavailable)
		return
	}
	sessions, err := sessionlist.List(h.files, sessionlist.SessionsDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]sessionRow, 0, len(sessions))
	for _, session := range sessions {
		rows = append(rows, sessionRow{
			ID:        session.ID,
			Title:     session.Title,
			Turns:     session.Turns,
			Tokens:    session.Tokens,
			Active:    session.Active,
			UpdatedAt: session.UpdatedAt,
		})
	}
	writeJSON(w, map[string]any{"sessions": rows})
}

// eventView is one event as the page reads it.
//
// The payload is passed through unchanged. The page decides what to show; a
// relay that pre-digested events would keep losing the fields nobody thought
// to project (the same mistake the turn payload made with deliveries).
type eventView struct {
	Seq        int64          `json:"seq"`
	Type       string         `json:"type"`
	FlowTurnID string         `json:"flow_turn_id,omitempty"`
	TeamTurnID string         `json:"team_turn_id,omitempty"`
	CallTurnID string         `json:"call_turn_id,omitempty"`
	TeamID     string         `json:"team_id,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	Payload    map[string]any `json:"payload,omitempty"`
	Truncated  bool           `json:"truncated,omitempty"`
}

// nodeView is one node of the execution tree.
type nodeView struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	Parent        string         `json:"parent,omitempty"`
	Children      []string       `json:"children,omitempty"`
	Status        string         `json:"status,omitempty"`
	FirstSeq      int64          `json:"first_seq"`
	LastSeq       int64          `json:"last_seq"`
	StartedAt     *time.Time     `json:"started_at,omitempty"`
	FinishedAt    *time.Time     `json:"finished_at,omitempty"`
	Attempt       int            `json:"attempt,omitempty"`
	FlowTurnID    string         `json:"flow_turn_id,omitempty"`
	TeamTurnID    string         `json:"team_turn_id,omitempty"`
	CallID        string         `json:"call_id,omitempty"`
	CallType      types.CallType `json:"call_type,omitempty"`
	AgentID       string         `json:"agent_id,omitempty"`
	TeamID        string         `json:"team_id,omitempty"`
	FlowSessionID string         `json:"flow_session_id,omitempty"`
}

// factsView is the KPI strip. Every number is derived, never stored, so it
// always agrees with the events below it.
type factsView struct {
	Turns        int      `json:"turns"`
	TeamTurns    int      `json:"team_turns"`
	Calls        int      `json:"calls"`
	ToolCalls    int      `json:"tool_calls"`
	ToolFailure  int      `json:"tool_failures"`
	Compactions  int      `json:"compactions"`
	Mailbox      int      `json:"mailbox_messages"`
	Models       []string `json:"models,omitempty"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
}

// maxPayloadBytes keeps one runaway tool output from turning the whole record
// into a multi-megabyte response. The page shows the cut honestly.
const maxPayloadBytes = 256 * 1024

// HandleRecord returns everything the page needs for one session: the merged
// timeline, the execution tree, and the derived counts.
//
// It is deliberately one endpoint. History, tree and KPIs describe the same
// events; splitting them would make the page fetch three times and stitch the
// results back together by seq.
func (h *Handler) HandleRecord(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if strings.TrimSpace(sessionID) == "" {
		http.Error(w, "session id is required", http.StatusBadRequest)
		return
	}
	if h.files == nil {
		http.Error(w, "session store is not configured", http.StatusServiceUnavailable)
		return
	}
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = parsed
	}

	ctx := r.Context()
	observer := eval.NewObserver(eval.NewFileFactSource(h.files))
	facts, err := observer.Observe(ctx, sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(facts.Events) == 0 {
		http.Error(w, "session "+strconv.Quote(sessionID)+" not found", http.StatusNotFound)
		return
	}

	response := buildRecordResponse(facts, afterSeq, limit)
	writeJSON(w, response)
}

// buildRecordResponse turns observations into the page's shape. Split out so a
// test can assert the projection without an HTTP round trip.
func buildRecordResponse(facts *eval.SessionFacts, afterSeq int64, limit int) map[string]any {
	if limit <= 0 {
		limit = 500
	}
	events := make([]eventView, 0, limit)
	truncated := 0
	for _, event := range facts.Events {
		if event.Seq <= afterSeq {
			continue
		}
		if len(events) >= limit {
			truncated++
			continue
		}
		events = append(events, projectEvent(event))
	}

	nodes := make([]nodeView, 0, 32)
	if facts.Graph != nil {
		for _, node := range facts.Graph.NodesOfKind(eval.NodeFlowSession) {
			nodes = append(nodes, projectNode(node))
		}
		for _, node := range facts.Graph.NodesOfKind(eval.NodeFlowTurn) {
			nodes = append(nodes, projectNode(node))
		}
		for _, node := range facts.Graph.NodesOfKind(eval.NodeTeamTurn) {
			nodes = append(nodes, projectNode(node))
		}
		for _, node := range facts.Graph.NodesOfKind(eval.NodeCallTurn) {
			nodes = append(nodes, projectNode(node))
		}
		for _, node := range facts.Graph.NodesOfKind(eval.NodeAgentSession) {
			nodes = append(nodes, projectNode(node))
		}
	}

	latest := int64(0)
	if n := len(facts.Events); n > 0 {
		latest = facts.Events[n-1].Seq
	}
	return map[string]any{
		"session":    facts.FlowSessionID,
		"events":     events,
		"nodes":      nodes,
		"facts":      projectFacts(facts),
		"latest_seq": latest,
		"truncated":  truncated,
		"after_seq":  afterSeq,
	}
}

func projectEvent(event storage.SessionEvent) eventView {
	view := eventView{
		Seq:        event.Seq,
		Type:       event.Type,
		FlowTurnID: event.FlowTurnID,
		TeamTurnID: event.TeamTurnID,
		CallTurnID: event.CallTurnID,
		TeamID:     event.TeamID,
		CreatedAt:  event.CreatedAt,
		Payload:    event.Payload,
	}
	if encoded, err := json.Marshal(event.Payload); err == nil && len(encoded) > maxPayloadBytes {
		view.Payload = map[string]any{
			"truncated": true,
			"bytes":     len(encoded),
			"keys":      payloadKeys(event.Payload),
		}
		view.Truncated = true
	}
	return view
}

func payloadKeys(payload map[string]any) []string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	return keys
}

func projectNode(node *eval.Node) nodeView {
	view := nodeView{
		ID:            node.ID,
		Kind:          string(node.Kind),
		Parent:        node.Parent,
		Children:      node.Children,
		Status:        string(node.Status),
		FirstSeq:      node.FirstSeq,
		LastSeq:       node.LastSeq,
		Attempt:       node.Attempt,
		FlowTurnID:    node.FlowTurnID,
		TeamTurnID:    node.TeamTurnID,
		CallID:        node.CallID,
		CallType:      node.CallType,
		AgentID:       node.AgentID,
		TeamID:        node.TeamID,
		FlowSessionID: node.FlowSessionID,
	}
	if !node.StartedAt.IsZero() {
		started := node.StartedAt
		view.StartedAt = &started
	}
	if !node.FinishedAt.IsZero() {
		finished := node.FinishedAt
		view.FinishedAt = &finished
	}
	return view
}

func projectFacts(facts *eval.SessionFacts) factsView {
	view := factsView{Turns: len(facts.FlowTurns)}
	usage := facts.Usage()
	view.InputTokens = usage.PromptTokens
	view.OutputTokens = usage.CompletionTokens

	models := make(map[string]bool)
	for _, turn := range facts.FlowTurns {
		view.TeamTurns += len(turn.TeamTurns)
		for _, teamTurn := range turn.TeamTurns {
			view.Calls += len(teamTurn.Calls)
			for _, call := range teamTurn.Calls {
				view.ToolCalls += call.ToolCalls
				view.ToolFailure += call.ToolFailures
			}
		}
	}
	for _, event := range facts.Events {
		switch event.Type {
		case types.EventContextCompacted:
			view.Compactions++
		case types.EventMailboxMessageEnqueued:
			view.Mailbox++
		case types.EventAgentModelResponse:
			if model, ok := event.Payload["model"].(string); ok && model != "" {
				models[model] = true
			}
		}
	}
	for model := range models {
		view.Models = append(view.Models, model)
	}
	return view
}

// HandleWebStatic serves the embedded page.
//
// Every path resolves inside the embed FS. There is no http.Dir and no path
// join against a variable, so a traversal attempt can only ever miss and 404 —
// it cannot read a file the binary did not ship.
func (h *Handler) HandleWebStatic(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/s/"):
		h.serveAsset(w, r, "index.html", "text/html; charset=utf-8")
	case strings.HasPrefix(r.URL.Path, "/static/"):
		name := strings.TrimPrefix(r.URL.Path, "/static/")
		cleaned := path.Clean("/" + name)
		if cleaned == "/" || strings.Contains(cleaned, "..") {
			http.NotFound(w, r)
			return
		}
		h.serveAsset(w, r, strings.TrimPrefix(cleaned, "/"), assetContentType(name))
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) serveAsset(w http.ResponseWriter, r *http.Request, name, contentType string) {
	file, err := webAssets.Open(path.Join("webassets", name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = file.Close() }()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := io.Copy(w, file); err != nil {
		// The client went away mid-response; nothing left to say.
		return
	}
}

func assetContentType(name string) string {
	switch path.Ext(name) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

// mediaRefPrefix is the only directory an attachment may be read from.
//
// The record tells the page what the user attached, but it can only offer a
// workspace path — which a browser cannot read. Without a way to fetch the bytes
// the transcript can show a file name and nothing else, and an image attachment
// looks like a sentence about an image.
const mediaRefPrefix = ".agents/data/uploads/"

// mediaRefPattern accepts the content-addressed names the media store writes.
// Anything else (a path, a name with a slash, a guess) is refused before it
// reaches the file store.
var mediaRefPattern = regexp.MustCompile(`^[0-9a-f]{32,64}$`)

// HandleMedia serves one stored attachment back to the viewer.
//
// The ref is restricted to the uploads directory and to a content-addressed
// name, so a request cannot walk out of it; the content type is detected from the
// bytes rather than trusted from the caller, and only real media is served —
// serving text/html from a user upload would turn an attachment into script on
// this origin.
func (h *Handler) HandleMedia(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		http.Error(w, "ref is required", http.StatusBadRequest)
		return
	}
	clean := path.Clean(ref)
	if !strings.HasPrefix(clean, mediaRefPrefix) || !mediaRefPattern.MatchString(path.Base(clean)) {
		http.Error(w, "ref is not a stored attachment", http.StatusBadRequest)
		return
	}
	if h.files == nil {
		http.Error(w, "session store is not configured", http.StatusServiceUnavailable)
		return
	}
	data, err := h.files.Read(clean)
	if err != nil {
		http.Error(w, "attachment "+strconv.Quote(path.Base(clean))+" not found", http.StatusNotFound)
		return
	}
	kind := http.DetectContentType(data)
	if !mediaContentAllowed(kind) {
		http.Error(w, "unsupported media type "+kind, http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}

// mediaContentAllowed keeps the endpoint to media. Anything else (HTML, SVG as
// text/xml, plain text) is not something the transcript renders, and serving it
// would hand a caller a same-origin document.
func mediaContentAllowed(kind string) bool {
	return strings.HasPrefix(kind, "image/") ||
		strings.HasPrefix(kind, "audio/") ||
		strings.HasPrefix(kind, "video/")
}
