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
	"github.com/heron-ai/heron-engine/internal/model"
	"github.com/heron-ai/heron-engine/internal/view"
	"github.com/heron-ai/heron-engine/pkg/types"
)

var version = "dev"

func main() {
	prompt := flag.String("prompt", "", "Run one FlowTurn and exit")
	flow := flag.String("flow", "", "Flow config path (default: .agents/flows/default.yml)")
	sessionID := flag.String("session", "", "Resume an existing FlowSession")
	jsonRPC := flag.Bool("json-rpc", false, "Run a long-lived JSON-RPC 2.0 server over stdin/stdout")
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

	flowPath := resolveFlowPath(*flow)
	if *jsonRPC {
		if flowPath == "" {
			fmt.Fprintln(os.Stderr, "Error: --json-rpc requires a new-format Flow config")
			fmt.Fprintln(os.Stderr, "Use --flow .agents/flows/default.yml")
			os.Exit(1)
		}
		runJSONRPC(flowPath)
		return
	}

	if *serve {
		startServer(flowPath, *port)
		return
	}
	if flowPath == "" {
		fmt.Fprintln(os.Stderr, "Error: a new-format Flow config is required")
		fmt.Fprintln(os.Stderr, "Use --flow .agents/flows/default.yml")
		os.Exit(1)
	}

	if *prompt != "" {
		runPrompt(flowPath, *sessionID, *prompt)
		return
	}
	runTUI(flowPath)
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

func startServer(flowPath, port string) {
	if flowPath == "" {
		fmt.Fprintln(os.Stderr, "Error: --serve requires a new-format Flow config")
		os.Exit(1)
	}

	bundle, _, err := buildCurrentRuntime(context.Background(), flowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building runtime: %v\n", err)
		os.Exit(1)
	}

	handler := view.NewRuntimeHandlerWithSessions(bundle.Flow, bundle.Sessions)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/run", handler.HandleRun)
	mux.HandleFunc("/api/sessions", handler.HandleRun)
	mux.HandleFunc("/api/sessions/turn", handler.HandleTurn)
	mux.HandleFunc("/api/status", handler.HandleStatus)
	mux.HandleFunc("/api/stream", handler.HandleStream)
	mux.HandleFunc("/api/recovery/status", handler.HandleRecoveryStatus)
	mux.HandleFunc("/api/recovery", handler.HandleRecover)
	mux.HandleFunc("/api/resume", handler.HandleResume)
	mux.HandleFunc("/api/cancel", handler.HandleCancel)

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

func runPrompt(flowPath, sessionID, prompt string) {
	ctx := context.Background()
	bundle, modelName, err := buildCurrentRuntime(ctx, flowPath)
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

func runTUI(flowPath string) {
	ctx := context.Background()
	bundle, modelName, err := buildCurrentRuntime(ctx, flowPath)
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
		usage.TotalTokens += result.Usage.TotalTokens
	}
	return usage
}

func buildCurrentRuntime(ctx context.Context, flowPath string) (*app.RuntimeBundle, string, error) {
	loader := config.NewConfigLoader(".")
	definitions, err := loader.LoadDefinitions(ctx, config.DefinitionsLoadRequest{
		FlowPath: flowPath,
	})
	if err != nil {
		return nil, "", err
	}

	models, err := loadModelsConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load .agents/models.json: %w", err)
	}
	if models == nil || len(models.Models) == 0 {
		return nil, "", fmt.Errorf("models.json has no models")
	}
	for i := range models.Models {
		models.Models[i].APIKey = resolveAPIKey(models.Models[i].APIKey, os.Getenv("OPENAI_API_KEY"))
	}
	defaultProfile, err := resolveModelProfile(models)
	if err != nil {
		return nil, "", err
	}
	if defaultProfile.APIKey == "" {
		return nil, "", fmt.Errorf("API key for default model %q is not set", defaultProfile.Name)
	}

	provider, err := model.NewProviderRouter(models.Model, models.Models)
	if err != nil {
		return nil, "", fmt.Errorf("build model providers: %w", err)
	}
	bundle, err := app.BuildRuntime(ctx, definitions, provider, ".")
	if err != nil {
		return nil, "", err
	}
	return bundle, provider.DefaultModel(), nil
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
			if item.Name == selected || item.ID == selected {
				return item, nil
			}
		}
	}
	return config.Models[0], nil
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
