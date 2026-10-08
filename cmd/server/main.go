package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/adrg/frontmatter"
	"github.com/heron-ai/heron-engine/internal/app"
	"github.com/heron-ai/heron-engine/internal/config"
	"github.com/heron-ai/heron-engine/internal/knowledge"
	"github.com/heron-ai/heron-engine/internal/model"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/internal/view"
	"github.com/heron-ai/heron-engine/pkg/types"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "knowledge" {
		runKnowledgeCLI(os.Args[2:])
		return
	}

	prompt := flag.String("prompt", "", "Run one FlowTurn and exit")
	flow := flag.String("flow", "", "Flow config path (default: .agents/flows/default.yml)")
	sessionID := flag.String("session", "", "Resume an existing FlowSession")
	modelOverride := flag.String("model", "", "Override the default model (models.json \"model\" field)")
	logLevel := flag.String("log-level", "", "Override log level (debug/info/warn/error)")
	maxRounds := flag.Int("max-rounds", 0, "Override max agent rounds (0 = use config)")
	jsonRPC := flag.Bool("json-rpc", false, "Run a long-lived JSON-RPC 2.0 server over stdin/stdout")
	inputFormat := flag.String("input-format", "", "Machine input format (stream-json)")
	outputFormat := flag.String("output-format", "", "Machine output format (stream-json)")
	serverURL := flag.String("server", "", "HTTP Heron server URL for stream-json client mode")
	port := flag.String("port", "", "HTTP server port (default: 8080)")
	serve := flag.Bool("serve", false, "Start HTTP server mode")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("Heron AI v%s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	if *jsonRPC && (*prompt != "" || *serve) {
		fmt.Fprintln(os.Stderr, "Error: --json-rpc cannot be combined with --prompt or --serve")
		os.Exit(1)
	}
	if (*inputFormat != "" || *outputFormat != "") &&
		(*prompt != "" || *serve || *jsonRPC) {
		fmt.Fprintln(os.Stderr, "Error: --input-format/--output-format cannot be combined with --prompt, --serve, or --json-rpc")
		os.Exit(1)
	}
	if *inputFormat == "stream-json" || *outputFormat == "stream-json" {
		if *inputFormat != "stream-json" || *outputFormat != "stream-json" {
			fmt.Fprintln(os.Stderr, "Error: stream-json requires both --input-format and --output-format")
			os.Exit(1)
		}
		streamFlowPath := resolveFlowPath(*flow)
		if streamFlowPath == "" || strings.TrimSpace(*serverURL) == "" {
			fmt.Fprintln(os.Stderr, "Error: stream-json requires --flow and --server")
			os.Exit(1)
		}
		runStreamJSONClient(streamFlowPath, *serverURL)
		return
	}

	flowPath := resolveFlowPath(*flow)
	overrides := cliOverrides{model: *modelOverride, logLevel: *logLevel, maxRounds: *maxRounds}
	if *jsonRPC {
		if flowPath == "" {
			fmt.Fprintln(os.Stderr, "Error: --json-rpc requires a new-format Flow config")
			fmt.Fprintln(os.Stderr, "Use --flow .agents/flows/default.yml")
			os.Exit(1)
		}
		runJSONRPC(flowPath, overrides)
		return
	}

	if *serve {
		startServer(flowPath, *port, overrides)
		return
	}
	if flowPath == "" {
		fmt.Fprintln(os.Stderr, "Error: a new-format Flow config is required")
		fmt.Fprintln(os.Stderr, "Use --flow .agents/flows/default.yml")
		os.Exit(1)
	}

	if *prompt != "" {
		runPrompt(flowPath, *sessionID, *prompt, overrides)
		return
	}
	runTUI(flowPath, overrides)
}

func resolveFlowPath(flowPath string) string {
	if flowPath != "" {
		return flowPath
	}
	for _, candidate := range []string{
		".agents/flows/default.yml",
		".agents/flows/default.yaml",
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func startServer(flowPath, port string, o cliOverrides) {
	if flowPath == "" {
		fmt.Fprintln(os.Stderr, "Error: --serve requires a new-format Flow config")
		os.Exit(1)
	}

	bundle, _, err := buildCurrentRuntime(context.Background(), flowPath, o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building runtime: %v\n", err)
		os.Exit(1)
	}

	handler := view.NewRuntimeHandlerWithSessionsAndTasks(
		bundle.Flow,
		bundle.Sessions,
		bundle.Tasks,
		bundle.TaskControl,
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/run", handler.HandleRun)
	mux.HandleFunc("/api/sessions", handler.HandleRun)
	mux.HandleFunc("/api/sessions/turn", handler.HandleTurn)
	mux.HandleFunc("/api/status", handler.HandleStatus)
	mux.HandleFunc("/api/stream", handler.HandleStream)
	mux.HandleFunc("/api/recovery/status", handler.HandleRecoveryStatus)
	mux.HandleFunc("/api/recovery", handler.HandleRecover)
	mux.HandleFunc("/api/resume", handler.HandleResume)
	mux.HandleFunc("/api/approvals", handler.HandleApproval)
	mux.HandleFunc("/api/result", handler.HandleResult)
	mux.HandleFunc("/api/cancel", handler.HandleCancel)
	mux.HandleFunc("/api/tasks", handler.HandleTaskStatus)
	mux.HandleFunc("/api/tasks/cancel", handler.HandleTaskCancel)
	mux.HandleFunc("/api/tasks/stream", handler.HandleTaskStream)

	if port == "" {
		port = os.Getenv("PORT")
	}
	if port == "" {
		port = "8080"
	}

	fmt.Printf("Heron AI FlowRuntime server listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func runPrompt(flowPath, sessionID, prompt string, o cliOverrides) {
	ctx := context.Background()
	bundle, modelName, err := buildCurrentRuntime(ctx, flowPath, o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building runtime: %v\n", err)
		os.Exit(1)
	}

	result, err := executeFlowTurn(ctx, bundle.Flow, bundle.Definitions.Flow.ID, sessionID, prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running FlowTurn: %v\n", err)
		os.Exit(1)
	}

	writePromptResult(os.Stdout, bundle.Definitions.Flow.ID, modelName, result)
}

// writePromptResult renders the human-readable `--prompt` output. It shares
// aggregateTeamUsage with the JSON-RPC result so both transports report the
// same token totals.
func writePromptResult(w io.Writer, flowID, modelName string, result types.FlowTurnResult) {
	fmt.Fprintf(w, "Flow: %s\n", flowID)
	fmt.Fprintf(w, "Model: %s\n", modelName)
	fmt.Fprintf(w, "FlowSession: %s\n", result.Session.ID)
	fmt.Fprintf(w, "Status: %s\n", result.Session.Status)
	if usage := aggregateTeamUsage(result.TeamResults); usage.TotalTokens > 0 {
		fmt.Fprintf(w, "Tokens: %d (prompt %d, completion %d)\n", usage.TotalTokens, usage.PromptTokens, usage.CompletionTokens)
	}
	if strings.TrimSpace(result.Reply) != "" {
		fmt.Fprintf(w, "\n%s\n", result.Reply)
	}
	for _, record := range result.Records {
		fmt.Fprintf(w, "\n[%s] %s\n", record.Name, record.Summary)
	}
}

// extractSharedRecords collects every SharedRecord published to a session's
// event timeline. Each shared_record.published event carries its record under
// payload["record"]; after JSON round-trip the record value is a map[string]any
// that must be re-marshaled back into types.SharedRecord.
func extractSharedRecords(replay *storage.SessionReplay) []types.SharedRecord {
	if replay == nil {
		return nil
	}
	var records []types.SharedRecord
	for _, event := range replay.Events {
		if event.Type != types.EventSharedRecordPublished {
			continue
		}
		raw, ok := event.Payload["record"]
		if !ok {
			continue
		}
		data, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var record types.SharedRecord
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	return records
}

// recordsToSources converts SharedRecords into the text fragments the
// KnowledgeSummarizer expects, mirroring the summary command's source building.
func recordsToSources(records []types.SharedRecord) []string {
	sources := make([]string, 0, len(records))
	for _, r := range records {
		name := strings.TrimSpace(r.Name)
		summary := strings.TrimSpace(r.Summary)
		switch {
		case name == "" && summary == "":
			continue
		case name == "":
			sources = append(sources, summary)
		case summary == "":
			sources = append(sources, name)
		default:
			sources = append(sources, fmt.Sprintf("[%s] %s", name, summary))
		}
	}
	return sources
}

// runLearnCLI parses `heron knowledge <session-id> [--flow] [--model]` and
// dispatches to learnOneSession. The scope is no longer a command-line flag:
// knowledge is layered by the event source it came from (flow/team/agent).
func runLearnCLI(args []string) {
	fs := flag.NewFlagSet("learn", flag.ExitOnError)
	flow := fs.String("flow", "", "Flow config path (default: .agents/flows/default.yml)")
	modelOverride := fs.String("model", "", "Override the default model (models.json \"model\" field)")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: heron knowledge <session-id> [--flow <path>] [--model <name>]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	flowPath := resolveFlowPath(*flow)
	if flowPath == "" {
		fmt.Fprintln(os.Stderr, "Error: a new-format Flow config is required")
		fmt.Fprintln(os.Stderr, "Use --flow .agents/flows/default.yml")
		os.Exit(1)
	}

	if err := learnOneSession(sessionID, flowPath, *modelOverride); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// learnAllSessions enumerates every session directory and incrementally learns
// each one (skipping sessions with no new events since their last checkpoint).
func learnAllSessions(flowPath, modelOverride string) error {
	ctx := context.Background()
	files := storage.NewFileStore(".")
	progress := knowledge.NewLearnProgress(files, filepath.Join(".agents", "knowledge"))

	names, err := files.List(filepath.Join(".agents", "data", "sessions"))
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	learned := 0
	for _, name := range names {
		sessionID := strings.TrimSpace(name)
		if sessionID == "" {
			continue
		}
		changed, err := learnOneSessionWithFiles(ctx, files, progress, sessionID, flowPath, modelOverride)
		if err != nil {
			return fmt.Errorf("learn session %q: %w", sessionID, err)
		}
		if changed {
			learned++
		}
	}
	fmt.Printf("Learned %d session(s).\n", learned)
	return nil
}

// learnOneSession is the entry point for `heron knowledge <sid>`. It builds the
// model provider then delegates to the shared incremental learning path.
func learnOneSession(sessionID, flowPath, modelOverride string) error {
	ctx := context.Background()
	files := storage.NewFileStore(".")
	progress := knowledge.NewLearnProgress(files, filepath.Join(".agents", "knowledge"))

	if _, err := learnOneSessionWithFiles(ctx, files, progress, sessionID, flowPath, modelOverride); err != nil {
		return err
	}
	return nil
}

// learnOneSessionWithFiles incrementally learns one session: it reads the
// session's events beyond the recorded last_seq, groups them by source layer,
// distills each layer into one or more knowledge entries, saves them, and
// advances the checkpoint. It returns true when new knowledge was produced.
func learnOneSessionWithFiles(
	ctx context.Context,
	files storage.FileStore,
	progress *knowledge.LearnProgress,
	sessionID, flowPath, modelOverride string,
) (bool, error) {
	if strings.TrimSpace(sessionID) == "" {
		return false, fmt.Errorf("session id is required")
	}

	definitions, provider, err := buildProvider(ctx, flowPath, modelOverride)
	if err != nil {
		return false, err
	}

	sessions := storage.NewJSONLSessionWriter(files)
	replay, err := sessions.Replay(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("replay session %q: %w", sessionID, err)
	}

	lastSeq, err := progress.LastSeq(sessionID)
	if err != nil {
		return false, err
	}

	// Filter to events beyond the checkpoint.
	var newEvents []storage.SessionEvent
	for _, event := range replay.Events {
		if event.Seq > lastSeq {
			newEvents = append(newEvents, event)
		}
	}
	if len(newEvents) == 0 {
		fmt.Printf("Session %q: no new events, skipped.\n", sessionID)
		return false, nil
	}

	sources := eventsToLayeredSources(newEvents)
	if len(sources) == 0 {
		// Nothing distillable, but still advance the checkpoint so we don't
		// re-process these events next time.
		if err := progress.Update(sessionID, replay.LastSeq); err != nil {
			return false, err
		}
		fmt.Printf("Session %q: no distillable events, checkpoint advanced.\n", sessionID)
		return false, nil
	}

	summarizer := knowledge.NewKnowledgeSummarizer(provider, definitions.Knowledge.SummaryModel)
	docs, err := summarizer.SummarizeLayered(ctx, sources)
	if err != nil {
		return false, fmt.Errorf("summarize knowledge: %w", err)
	}

	store := knowledge.NewMarkdownStore(files, filepath.Join(".agents", "knowledge"))
	saved := 0
	for _, md := range docs {
		entry, parseErr := parseKnowledgeMarkdown(md, sessionID, "")
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: skip unparsable knowledge: %v\n", parseErr)
			continue
		}
		// Dedup: skip if an active entry already matches by keyword.
		if dup, dupErr := store.FindDuplicate(ctx, entry); dupErr == nil && dup != nil {
			fmt.Printf("Knowledge already exists (matches %q), skipped.\n", dup.ID)
			continue
		}
		savedEntry, saveErr := store.UpsertActive(ctx, entry)
		if saveErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: save knowledge: %v\n", saveErr)
			continue
		}
		fmt.Printf("Knowledge learned: %s [%s] (version %d)\n", savedEntry.ID, savedEntry.Scope.Type, savedEntry.Version)
		saved++
	}

	if err := progress.Update(sessionID, replay.LastSeq); err != nil {
		return false, err
	}
	return saved > 0, nil
}

// eventLayer maps a session event type to its source layer (flow/team/agent).
// flow_* and shared_record.published are flow; team_* is team; agent-layer
// events (agent_*, agent.*, tool_call.*, context.compacted) are agent.
// Team-orchestrated events without a clear prefix (approval, command_turn,
// webhook_turn) default to team.
func eventLayer(eventType string) string {
	switch {
	case eventType == types.EventSharedRecordPublished:
		return "flow"
	case strings.HasPrefix(eventType, "flow_"):
		return "flow"
	case strings.HasPrefix(eventType, "team_"):
		return "team"
	case strings.HasPrefix(eventType, "agent_"),
		strings.HasPrefix(eventType, "agent."),
		strings.HasPrefix(eventType, "tool_call"),
		strings.HasPrefix(eventType, "context.compacted"):
		return "agent"
	default:
		return "team"
	}
}

// eventsToLayeredSources converts session events into layer-tagged text
// fragments for the layered summarizer. SharedRecord payloads are rendered as
// "[name] summary"; other events fall back to their type as a lightweight
// signal.
func eventsToLayeredSources(events []storage.SessionEvent) []knowledge.LayeredSource {
	var sources []knowledge.LayeredSource
	for _, event := range events {
		layer := eventLayer(event.Type)
		text := eventSourceText(event)
		if text == "" {
			continue
		}
		sources = append(sources, knowledge.LayeredSource{Layer: layer, Text: text})
	}
	return sources
}

// eventSourceText renders a single session event as a text fragment suitable
// for distillation. SharedRecord payloads carry their name/summary; other
// events contribute their type plus a compact JSON payload when non-empty.
func eventSourceText(event storage.SessionEvent) string {
	if event.Type == types.EventSharedRecordPublished {
		raw, ok := event.Payload["record"]
		if !ok {
			return ""
		}
		data, err := json.Marshal(raw)
		if err != nil {
			return ""
		}
		var record types.SharedRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return ""
		}
		name := strings.TrimSpace(record.Name)
		summary := strings.TrimSpace(record.Summary)
		switch {
		case name == "" && summary == "":
			return ""
		case name == "":
			return summary
		case summary == "":
			return name
		default:
			return fmt.Sprintf("[%s] %s", name, summary)
		}
	}

	// Non-shared-record events: use the type and, when present, a compact
	// payload so the summarizer can see the substantive content.
	if len(event.Payload) == 0 {
		return event.Type
	}
	data, err := json.Marshal(event.Payload)
	if err != nil {
		return event.Type
	}
	return fmt.Sprintf("%s %s", event.Type, string(data))
}

// parseKnowledgeMarkdown converts the summarizer's markdown output into a
// KnowledgeEntry, extracting the stable id/scope/confidence/keywords from the
// frontmatter and the title/body from the document.
func parseKnowledgeMarkdown(md, sessionID, scope string) (types.KnowledgeEntry, error) {
	var meta struct {
		ID         string   `yaml:"id"`
		Scope      string   `yaml:"scope"`
		Confidence string   `yaml:"confidence"`
		Keywords   []string `yaml:"keywords"`
		Status     string   `yaml:"status"`
	}
	body, err := frontmatter.Parse(strings.NewReader(md), &meta)
	if err != nil {
		return types.KnowledgeEntry{}, fmt.Errorf("parse knowledge markdown: %w", err)
	}
	content := strings.TrimSpace(string(body))
	if content == "" {
		return types.KnowledgeEntry{}, fmt.Errorf("knowledge summarizer returned empty body")
	}

	id := strings.TrimSpace(meta.ID)
	if id == "" {
		id = sessionID
	}

	title := firstLineOf(content)
	if strings.HasPrefix(title, "#") {
		title = strings.TrimSpace(strings.TrimLeft(title, "#"))
	}

	scopeType := strings.ToLower(scope)
	if scopeType == "" {
		scopeType = strings.ToLower(meta.Scope)
	}
	scopeType = normalizeScope(scopeType)

	entry := types.KnowledgeEntry{
		ID:         id,
		Title:      title,
		Content:    content,
		Keys:       meta.Keywords,
		Scope:      types.Scope{Type: scopeType},
		Status:     "active",
		Confidence: meta.Confidence,
		Source:     sessionID,
		Basis:      []types.BasisRef{{Kind: "session", Path: sessionID}},
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	return entry, nil
}

// runKnowledgeCLI dispatches `heron knowledge <sid|gc>`. With no argument it
// learns every session; with `gc` it archives stale knowledge; otherwise the
// argument is treated as a session id to learn incrementally.
func runKnowledgeCLI(args []string) {
	if len(args) == 0 {
		flowPath := resolveFlowPath("")
		if flowPath == "" {
			fmt.Fprintln(os.Stderr, "Error: a new-format Flow config is required")
			fmt.Fprintln(os.Stderr, "Use --flow .agents/flows/default.yml")
			os.Exit(1)
		}
		if err := learnAllSessions(flowPath, ""); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	switch args[0] {
	case "gc":
		runKnowledgeGCCLI(args[1:])
	default:
		runLearnCLI(args)
	}
}

// normalizeScope maps a scope string onto the canonical flow|team|agent set.
// Legacy values ("all" -> flow, "agents" -> agent) are tolerated so old
// frontmatter keeps loading correctly.
func normalizeScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "team", "agent":
		return strings.ToLower(strings.TrimSpace(scope))
	case "all":
		return "flow"
	case "agents":
		return "agent"
	default:
		return "flow"
	}
}

func knowledgeStore() (*knowledge.MarkdownStore, *knowledge.StatsRecorder) {
	files := storage.NewFileStore(".")
	root := filepath.Join(".agents", "knowledge")
	return knowledge.NewMarkdownStore(files, root), knowledge.NewStatsRecorder(files, root)
}

func runKnowledgeGCCLI(args []string) {
	fs := flag.NewFlagSet("knowledge gc", flag.ExitOnError)
	windowDays := fs.Int("window", 15, "GC window in days (default: 15)")
	_ = fs.Parse(args)

	store, stats := knowledgeStore()
	entries, err := store.LoadAll(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	hitCounts, err := stats.HitCounts()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	now := time.Now().UTC()
	window := time.Duration(*windowDays) * 24 * time.Hour

	archived := 0
	for _, e := range entries {
		if e.Status != "active" {
			continue
		}
		if shouldArchive(e, hitCounts[knowledge.StatsKey(e)], now, window) {
			if err := store.Archive(context.Background(), e.ID); err != nil {
				fmt.Fprintf(os.Stderr, "Error archiving %q: %v\n", e.ID, err)
				os.Exit(1)
			}
			fmt.Printf("Archived %q\n", e.ID)
			archived++
		}
	}
	fmt.Printf("GC complete: %d knowledge archived.\n", archived)
}

// shouldArchive decides whether an active entry should be archived during GC.
func shouldArchive(e types.KnowledgeEntry, hitCount int, now time.Time, window time.Duration) bool {
	// Condition 1: explicit ExpiresAt already passed.
	if e.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, e.ExpiresAt); err == nil {
			return now.After(t)
		}
	}

	// Condition 2: implicit expiry (CreatedAt+window) or zero hits within the
	// window. Only applies when no explicit ExpiresAt governs the entry.
	if e.CreatedAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, e.CreatedAt)
	if err != nil {
		return false
	}
	age := now.Sub(t)
	if age > window {
		// Expired by age alone, or zero hits within the window.
		return hitCount == 0
	}
	return false
}

func firstLineOf(content string) string {
	if index := strings.IndexByte(content, '\n'); index >= 0 {
		return strings.TrimSpace(content[:index])
	}
	return strings.TrimSpace(content)
}

func runTUI(flowPath string, o cliOverrides) {
	ctx := context.Background()
	bundle, modelName, err := buildCurrentRuntime(ctx, flowPath, o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building runtime: %v\n", err)
		os.Exit(1)
	}

	runner := &sessionFlowRunner{runtime: bundle.Flow}
	model := view.NewTUIModel(
		bundle.Definitions.Flow.ID,
		modelName,
		len(bundle.Definitions.Agents),
		len(bundle.Definitions.Flow.Teams),
		runner,
	)
	if _, err := tea.NewProgram(model, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}

type sessionFlowRunner struct {
	runtime   types.FlowRuntime
	sessionID string
}

func (r *sessionFlowRunner) Run(ctx context.Context, input string) (*view.FlowResult, error) {
	var (
		result types.FlowTurnResult
		err    error
	)
	if r.sessionID == "" {
		result, err = r.runtime.Start(ctx, types.StartFlowRequest{Input: input})
	} else {
		result, err = r.runtime.HandleInput(ctx, r.sessionID, input)
	}
	if err != nil {
		return nil, err
	}
	r.sessionID = result.Session.ID

	outputs := make([]view.TeamOutput, 0, len(result.TeamResults))
	for _, teamResult := range result.TeamResults {
		outputs = append(outputs, view.TeamOutput{
			TeamID:  teamResult.Turn.TeamID,
			Reply:   teamResult.Reply,
			Records: teamResult.Records,
		})
	}
	return &view.FlowResult{
		Teams:  outputs,
		Status: result.Session.Status,
		Usage:  aggregateTeamUsage(result.TeamResults),
	}, nil
}

func aggregateTeamUsage(results []types.TeamTurnResult) types.TokenUsage {
	var usage types.TokenUsage
	for _, result := range results {
		usage.PromptTokens += result.Usage.PromptTokens
		usage.CompletionTokens += result.Usage.CompletionTokens
		usage.ReasoningTokens += result.Usage.ReasoningTokens
		usage.TotalTokens += result.Usage.TotalTokens
		usage.PromptCacheHitTokens += result.Usage.PromptCacheHitTokens
		usage.PromptCacheMissTokens += result.Usage.PromptCacheMissTokens
		usage.CacheReadInputTokens += result.Usage.CacheReadInputTokens
		usage.CacheCreationInputTokens += result.Usage.CacheCreationInputTokens
	}
	return usage
}

// cliOverrides carries command-line overrides for settings that normally come
// from .agents/settings.json or .agents/models.json.
type cliOverrides struct {
	model     string
	logLevel  string
	maxRounds int
}

func buildCurrentRuntime(ctx context.Context, flowPath string, o cliOverrides) (*app.RuntimeBundle, string, error) {
	definitions, provider, err := buildProvider(ctx, flowPath, o.model)
	if err != nil {
		return nil, "", err
	}
	applyMaxRounds(&definitions.Limits, o.maxRounds)
	bundle, err := app.BuildRuntime(ctx, definitions, provider, ".", o.logLevel)
	if err != nil {
		return nil, "", err
	}
	return bundle, provider.DefaultModel(), nil
}

// applyMaxRounds overrides MaxAgentRounds when maxRounds is positive.
func applyMaxRounds(limits *types.RuntimeLimits, maxRounds int) {
	if limits == nil || maxRounds <= 0 {
		return
	}
	limits.MaxAgentRounds = maxRounds
}

// buildProvider loads flow definitions and constructs the model provider
// router. It is shared by the interactive/HTTP runtimes and the summary CLI so
// provider construction stays in one place. modelOverride, when non-empty,
// replaces the default model selected by models.json's "model" field.
func buildProvider(ctx context.Context, flowPath, modelOverride string) (*types.Definitions, *model.ProviderRouter, error) {
	loader := config.NewConfigLoader(".")
	definitions, err := loader.LoadDefinitions(ctx, config.DefinitionsLoadRequest{
		FlowPath: flowPath,
	})
	if err != nil {
		return nil, nil, err
	}

	models, err := loadModelsConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("load .agents/models.json: %w", err)
	}
	if models == nil || len(models.Models) == 0 {
		return nil, nil, fmt.Errorf("models.json has no models")
	}
	if strings.TrimSpace(modelOverride) != "" {
		models.Model = modelOverride
	}
	for i := range models.Models {
		models.Models[i].APIKey = resolveAPIKey(models.Models[i].APIKey, apiKeyFallbackFor(models.Models[i]))
	}
	defaultProfile, err := resolveModelProfile(models)
	if err != nil {
		return nil, nil, err
	}
	if defaultProfile.APIKey == "" {
		return nil, nil, fmt.Errorf("API key for default model %q is not set", defaultProfile.Name)
	}

	provider, err := model.NewProviderRouter(models.Model, models.Models)
	if err != nil {
		return nil, nil, fmt.Errorf("build model providers: %w", err)
	}
	return definitions, provider, nil
}

type ModelsConfig struct {
	Model  string               `json:"model"`
	Models []types.ModelProfile `json:"models"`
}

func loadModelsConfig() (*ModelsConfig, error) {
	data, err := os.ReadFile(filepath.Join(".agents", "models.json"))
	if err != nil {
		return nil, err
	}
	var config ModelsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func resolveModelProfile(config *ModelsConfig) (types.ModelProfile, error) {
	if config == nil || len(config.Models) == 0 {
		return types.ModelProfile{}, fmt.Errorf("models.json has no models")
	}
	selected := strings.TrimSpace(config.Model)
	if selected != "" {
		for _, item := range config.Models {
			if item.Name == selected {
				return item, nil
			}
		}
		return types.ModelProfile{}, fmt.Errorf("model %q not found in models.json", selected)
	}
	return config.Models[0], nil
}

// apiKeyFallbackFor maps a model profile to the environment variable that
// should be used as its API key fallback when the profile does not declare one.
// The mapping is provider-aware so a key for one provider is never injected
// into a model of another provider (for example OPENAI_API_KEY must not leak
// into an Anthropic model). Unknown or ambiguous providers get no fallback.
func apiKeyFallbackFor(profile types.ModelProfile) string {
	switch profileProtocol(profile) {
	case "anthropic":
		return os.Getenv("ANTHROPIC_API_KEY")
	case "openai":
		return os.Getenv("OPENAI_API_KEY")
	default:
		return ""
	}
}

// profileProtocol classifies a model profile into a canonical protocol name
// using the same rules as the provider router (Protocol first, then Provider,
// defaulting to openai for backwards compatibility).
func profileProtocol(profile types.ModelProfile) string {
	value := strings.ToLower(strings.TrimSpace(profile.Protocol))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(profile.Provider))
	}

	switch value {
	case "anthropic", "anthropic_messages", "messages":
		return "anthropic"
	case "openai", "openai_chat", "openai-compatible", "openai_compatible", "chat":
		return "openai"
	default:
		// Existing models.json files only have an OpenAI-compatible endpoint.
		// Keep that format as the safe backwards-compatible default.
		return "openai"
	}
}

func resolveAPIKey(configured, fallback string) string {
	if strings.HasPrefix(configured, "${") && strings.HasSuffix(configured, "}") {
		if value := os.Getenv(strings.TrimSuffix(strings.TrimPrefix(configured, "${"), "}")); value != "" {
			return value
		}
		// An explicit environment reference must not silently fall back to a
		// key for another provider (for example ANTHROPIC_API_KEY -> the
		// process's OPENAI_API_KEY).
		return ""
	}
	if configured != "" {
		return configured
	}
	return fallback
}
