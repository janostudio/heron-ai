package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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
	if len(os.Args) > 1 && os.Args[1] == "summary" {
		runSummaryCLI(os.Args[2:])
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

	fmt.Printf("Flow: %s\n", bundle.Definitions.Flow.ID)
	fmt.Printf("Model: %s\n", modelName)
	fmt.Printf("FlowSession: %s\n", result.Session.ID)
	fmt.Printf("Status: %s\n", result.Session.Status)
	if strings.TrimSpace(result.Reply) != "" {
		fmt.Printf("\n%s\n", result.Reply)
	}
	for _, record := range result.Records {
		fmt.Printf("\n[%s] %s\n", record.Name, record.Summary)
	}
}

// runSummaryCLI parses the `heron summary <session-id> [--flow <path>]`
// subcommand arguments and dispatches to runSummary.
func runSummaryCLI(args []string) {
	fs := flag.NewFlagSet("summary", flag.ExitOnError)
	flow := fs.String("flow", "", "Flow config path (default: .agents/flows/default.yml)")
	modelOverride := fs.String("model", "", "Override the default model (models.json \"model\" field)")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: heron summary <session-id> [--flow <path>] [--model <name>]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	flowPath := resolveFlowPath(*flow)
	if flowPath == "" {
		fmt.Fprintln(os.Stderr, "Error: a new-format Flow config is required")
		fmt.Fprintln(os.Stderr, "Use --flow .agents/flows/default.yml")
		os.Exit(1)
	}

	if err := runSummary(sessionID, flowPath, *modelOverride); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// runSummary distills a session's published SharedRecords into a proposed
// Knowledge entry via the KnowledgeSummarizer and writes it to
// .agents/knowledge/proposed/<session-id>.md.
func runSummary(sessionID, flowPath, modelOverride string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session id is required")
	}

	ctx := context.Background()
	definitions, provider, err := buildProvider(ctx, flowPath, modelOverride)
	if err != nil {
		return err
	}

	files := storage.NewFileStore(".")
	sessions := storage.NewJSONLSessionWriter(files)
	replay, err := sessions.Replay(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("replay session %q: %w", sessionID, err)
	}

	records := extractSharedRecords(replay)
	if len(records) == 0 {
		return fmt.Errorf("session %q has no shared records to summarize", sessionID)
	}

	sources := recordsToSources(records)
	summarizer := knowledge.NewKnowledgeSummarizer(provider, definitions.Knowledge.SummaryModel)
	md, err := summarizer.Summarize(ctx, sources)
	if err != nil {
		return fmt.Errorf("summarize knowledge: %w", err)
	}
	if strings.TrimSpace(md) == "" {
		return fmt.Errorf("knowledge summarizer returned empty markdown")
	}

	path := filepath.Join(".agents", "knowledge", "proposed", sessionID+".md")
	if err := files.Write(path, []byte(md+"\n")); err != nil {
		return fmt.Errorf("write knowledge %s: %w", path, err)
	}

	fmt.Printf("Knowledge written to %s\n", path)
	return nil
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
	if o.maxRounds > 0 {
		definitions.Limits.MaxAgentRounds = o.maxRounds
	}
	bundle, err := app.BuildRuntime(ctx, definitions, provider, ".", o.logLevel)
	if err != nil {
		return nil, "", err
	}
	return bundle, provider.DefaultModel(), nil
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
