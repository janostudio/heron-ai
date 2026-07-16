package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/heron-ai/heron-engine/internal/config"
	"github.com/heron-ai/heron-engine/internal/model"
	"github.com/heron-ai/heron-engine/internal/orchestration"
	"github.com/heron-ai/heron-engine/internal/tool"
	"github.com/heron-ai/heron-engine/internal/view"
	"github.com/heron-ai/heron-engine/pkg/types"
)

var version = "dev"

func main() {
	prompt := flag.String("prompt", "", "Run a single prompt and exit (non-interactive)")
	flow := flag.String("flow", "", "Flow config path (default: .agents/flows/default.yml)")
	runID := flag.String("run", "", "Resume a previous run by ID")
	port := flag.String("port", "", "HTTP server port (default: 8080)")
	serve := flag.Bool("serve", false, "Start HTTP server mode")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("Heron AI v%s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	if *runID != "" {
		runResume(*runID, *prompt)
		return
	}

	if *serve {
		startServer(*port)
		return
	}

	if *prompt != "" {
		runNonInteractive(resolveFlowPath(*flow), *prompt)
		return
	}

	// Default: TUI mode
	runTUI(resolveFlowPath(*flow))
}

// resolveFlowPath resolves the flow path with fallback defaults
func resolveFlowPath(flowPath string) string {
	if flowPath != "" {
		return flowPath
	}
	// Try common default paths
	defaults := []string{
		".agents/flows/default.yml",
		".agents/default.yml",
	}
	for _, p := range defaults {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// If no flow found, use code-builtin default config
	return "" // will be handled by the loader with builtin defaults
}

func startServer(port string) {
	// HTTP server mode
	fmt.Println("Heron AI - Generic Multi-Agent Engine")

	handler := view.NewHandler()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/run", handler.HandleRun)
	mux.HandleFunc("/api/status", handler.HandleStatus)
	mux.HandleFunc("/api/stream", handler.HandleStream)
	mux.HandleFunc("/api/resume", handler.HandleResume)
	mux.HandleFunc("/api/cancel", handler.HandleCancel)

	if port == "" {
		port = os.Getenv("PORT")
	}
	if port == "" {
		port = "8080"
	}

	fmt.Printf("Server starting on :%s\n", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func runNonInteractive(flowPath, prompt string) {
	if flowPath == "" {
		fmt.Fprintln(os.Stderr, "Error: --flow is required for --prompt mode")
		os.Exit(1)
	}

	ctx := context.Background()

	// 1. Load config or use builtin defaults
	var runReq *types.RunRequest
	if flowPath != "" {
		loader := config.NewConfigLoader(".")
		var err error
		runReq, err = loader.Load(ctx, config.LoadRequest{
			FlowPath: flowPath,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}
	} else {
		runReq = builtinDefaultConfig()
	}

	fmt.Printf("Flow: %s\n", runReq.Flow.Name)
	fmt.Printf("Teams: %d, Agents: %d\n", len(runReq.Teams), len(runReq.Agents))

	// 2. Load models config
	modelsCfg, _ := loadModelsConfig()
	modelName, baseURL, apiKey := resolveModel(modelsCfg)

	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: OPENAI_API_KEY not set")
		fmt.Fprintln(os.Stderr, "Set it via environment variable: export OPENAI_API_KEY=sk-xxx")
		os.Exit(1)
	}

	provider := model.NewOpenAIProvider(apiKey, baseURL, modelName)
	modelRegistry := model.NewModelRegistry()
	modelRegistry.Register("default", provider)

	fmt.Printf("Model: %s (%s)\n", modelName, baseURL)

	// 3. Set up tool executor
	toolRegistry := tool.NewToolRegistry()
	baseDir, _ := os.Getwd()
	toolRegistry.Register(tool.NewReadTool(baseDir))
	toolRegistry.Register(tool.NewWriteTool(baseDir))
	toolRegistry.Register(tool.NewGrepTool(baseDir))
	toolRegistry.Register(tool.NewGlobTool(baseDir))
	toolRegistry.Register(tool.NewTodoWriteTool())
	toolRegistry.Register(tool.NewTodoReadTool())
	toolExecutor := tool.NewToolExecutor(toolRegistry)

	// 4. Set up agent runtime
	agentRuntime := &simpleAgentRuntime{
		model:        provider,
		toolExecutor: toolExecutor,
		modelName:    modelName,
	}

	// 5. Set up orchestration
	scheduler := orchestration.NewTeamScheduler(agentRuntime)
	teamRunner := orchestration.NewTeamRunner(scheduler)
	engine := orchestration.NewFlowEngine(runReq.Flow, runReq.Teams, runReq.Agents, teamRunner)

	// 6. Run
	fmt.Printf("\n--- Running: %s ---\n\n", prompt)
	result, err := engine.Run(ctx, prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// 7. Generate runID and save runtime data
	runID := generateRunID()
	dataDir := filepath.Join(".agents", "data", runID)
	os.MkdirAll(filepath.Join(dataDir, "sessions"), 0755)

	saveRunState(dataDir, runReq, prompt, result)
	saveRunLog(dataDir, prompt, result)
	saveAgentSessions(dataDir, runReq, result)

	fmt.Printf("\nData saved to: %s\n", dataDir)

	// 8. Print results
	for _, stage := range result.Stages {
		fmt.Printf("\n=== Stage: %s (Team: %s) ===\n", stage.StageName, stage.TeamName)
		if stage.TeamResult != nil {
			fmt.Println(stage.TeamResult.Content)
		}
	}

	fmt.Printf("\nSignal: %s\n", result.Signal)
	fmt.Printf("Tokens: %d prompt + %d completion = %d total\n",
		result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens)
}

func runResume(runID, prompt string) {
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "Error: --prompt is required with --run")
		os.Exit(1)
	}

	// Load run state
	statePath := filepath.Join(".agents", "data", runID, "run_state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: run %q not found at %s\n", runID, statePath)
		os.Exit(1)
	}

	var state map[string]interface{}
	if err := json.Unmarshal(data, &state); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid run state: %v\n", err)
		os.Exit(1)
	}

	flowName, _ := state["flow_name"].(string)
	fmt.Printf("Resuming run: %s (flow: %s)\n", runID, flowName)
	fmt.Printf("Prompt: %s\n", prompt)

	// TODO: Load flow config and rebuild engine, then call engine.Resume()
	// For now, show that the run state was found
	fmt.Println("Run resume functionality requires flow config reloading - coming soon")
	fmt.Printf("Run state loaded successfully from: %s\n", statePath)
}

// simpleAgentRuntime is a minimal agent runtime that uses TurnLoop
type simpleAgentRuntime struct {
	model        *model.OpenAIProvider
	toolExecutor *tool.ToolExecutor
	modelName    string
}

func (r *simpleAgentRuntime) Run(ctx context.Context, agentCfg types.AgentConfig, task types.TaskConfig, input string) (*types.AgentResult, error) {
	// Build messages from agent config
	systemPrompt := buildSystemPrompt(agentCfg)
	messages := []types.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: input},
	}

	maxRounds := agentCfg.Loop.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 3
	}

	modelCfg := types.ModelConfig{
		Model:       r.modelName,
		Temperature: agentCfg.Model.Temperature,
		MaxTokens:   agentCfg.Model.MaxTokens,
	}

	totalUsage := types.TokenUsage{}
	allContent := ""

	for round := 0; round < maxRounds; round++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		resp, err := r.model.Chat(ctx, messages, nil, modelCfg)
		if err != nil {
			return nil, fmt.Errorf("llm chat (round %d): %w", round, err)
		}

		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens

		// No tool calls -> final answer
		if len(resp.ToolCalls) == 0 {
			return &types.AgentResult{
				Raw:    resp.Text,
				Signal: types.SignalContinue,
				Usage:  totalUsage,
			}, nil
		}

		// Execute tool calls
		messages = append(messages, types.Message{
			Role:      "assistant",
			Content:   resp.Text,
			ToolCalls: resp.ToolCalls,
		})

		for _, tc := range resp.ToolCalls {
			result, err := r.toolExecutor.Execute(ctx, tc.Name, tc.Arguments)
			toolContent := ""
			if err != nil {
				toolContent = fmt.Sprintf("Error: %v", err)
			} else if result != nil {
				toolContent = result.Content
			}

			messages = append(messages, types.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    toolContent,
			})
		}

		allContent = resp.Text
	}

	return &types.AgentResult{
		Raw:    allContent,
		Signal: types.SignalWaitInput,
		Usage:  totalUsage,
	}, nil
}

func buildSystemPrompt(agent types.AgentConfig) string {
	var parts []string

	if agent.Persona.Role != "" {
		parts = append(parts, fmt.Sprintf("You are %s.", agent.Persona.Role))
	}
	if agent.Persona.Goal != "" {
		parts = append(parts, fmt.Sprintf("Goal: %s", agent.Persona.Goal))
	}
	if agent.Body != "" {
		parts = append(parts, agent.Body)
	}

	return strings.Join(parts, "\n\n")
}

func runTUI(flowPath string) {
	ctx := context.Background()

	// 1. Load config or use builtin defaults
	var runReq *types.RunRequest
	if flowPath != "" {
		loader := config.NewConfigLoader(".")
		var err error
		runReq, err = loader.Load(ctx, config.LoadRequest{
			FlowPath: flowPath,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}
	} else {
		runReq = builtinDefaultConfig()
	}

	// 2. Load models config
	modelsCfg, _ := loadModelsConfig()
	modelName, baseURL, apiKey := resolveModel(modelsCfg)

	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: OPENAI_API_KEY not set")
		fmt.Fprintln(os.Stderr, "Set it via environment variable: export OPENAI_API_KEY=sk-xxx")
		os.Exit(1)
	}

	provider := model.NewOpenAIProvider(apiKey, baseURL, modelName)
	modelRegistry := model.NewModelRegistry()
	modelRegistry.Register("default", provider)

	fmt.Printf("Model: %s (%s)\n", modelName, baseURL)

	// 3. Set up tool executor (TUI)
	toolRegistry := tool.NewToolRegistry()
	baseDir, _ := os.Getwd()
	toolRegistry.Register(tool.NewReadTool(baseDir))
	toolRegistry.Register(tool.NewWriteTool(baseDir))
	toolRegistry.Register(tool.NewGrepTool(baseDir))
	toolRegistry.Register(tool.NewGlobTool(baseDir))
	toolRegistry.Register(tool.NewTodoWriteTool())
	toolRegistry.Register(tool.NewTodoReadTool())
	toolExecutor := tool.NewToolExecutor(toolRegistry)

	// 4. Set up agent runtime
	agentRuntime := &simpleAgentRuntime{
		model:        provider,
		toolExecutor: toolExecutor,
		modelName:    modelName,
	}

	// 5. Set up orchestration
	scheduler := orchestration.NewTeamScheduler(agentRuntime)
	teamRunner := orchestration.NewTeamRunner(scheduler)
	engine := orchestration.NewFlowEngine(runReq.Flow, runReq.Teams, runReq.Agents, teamRunner)

	// 6. Create TUI runner adapter
	runner := &tuiFlowRunner{engine: engine, runReq: runReq}

	// 7. Start TUI
	model := view.NewTUIModel(runReq.Flow.Name, modelName, len(runReq.Agents), len(runReq.Teams), runner)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}

// tuiFlowRunner adapts the FlowEngine to the view.FlowRunner interface
type tuiFlowRunner struct {
	engine *orchestration.FlowEngine
	runReq *types.RunRequest
}

func (r *tuiFlowRunner) Run(ctx context.Context, input string) (*view.FlowResult, error) {
	result, err := r.engine.Run(ctx, input)
	if err != nil {
		return nil, err
	}

	// Generate runID and save runtime data
	runID := generateRunID()
	dataDir := filepath.Join(".agents", "data", runID)
	os.MkdirAll(filepath.Join(dataDir, "sessions"), 0755)

	saveRunState(dataDir, r.runReq, input, result)
	saveRunLog(dataDir, input, result)
	saveAgentSessions(dataDir, r.runReq, result)

	fmt.Printf("\nData saved to: %s\n", dataDir)

	var stages []view.StageOutput
	for _, s := range result.Stages {
		content := ""
		if s.TeamResult != nil {
			content = s.TeamResult.Content
		}
		stages = append(stages, view.StageOutput{
			StageName: s.StageName,
			TeamName:  s.TeamName,
			Content:   content,
		})
	}

	return &view.FlowResult{
		Stages: stages,
		Signal: result.Signal,
		Usage:  result.Usage,
	}, nil
}

// ModelEntry represents a model in models.json
type ModelEntry struct {
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	MaxTokens int    `json:"max_tokens"`
}

// ModelsConfig represents the models.json structure
type ModelsConfig struct {
	Model  string       `json:"model"`
	Models []ModelEntry `json:"models"`
}

// loadModelsConfig loads models.json from .agents/ directory
func loadModelsConfig() (*ModelsConfig, error) {
	data, err := os.ReadFile(filepath.Join(".agents", "models.json"))
	if err != nil {
		return nil, err
	}
	var cfg ModelsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// resolveModel resolves the active model entry from models.json
// Priority: models.json > agent config > builtin default
func resolveModel(cfg *ModelsConfig) (name string, baseURL string, apiKey string) {
	// Default values
	name = "gpt-4o-mini"
	baseURL = "https://api.openai.com/v1"
	apiKey = os.Getenv("OPENAI_API_KEY")

	if cfg == nil {
		return
	}

	// Find the default model in the models list
	for _, m := range cfg.Models {
		if m.Name == cfg.Model {
			name = m.Name
			if m.BaseURL != "" {
				baseURL = m.BaseURL
			}
			// api_key can reference env var: ${OPENAI_API_KEY}
			if strings.HasPrefix(m.APIKey, "${") && strings.HasSuffix(m.APIKey, "}") {
				envKey := m.APIKey[2 : len(m.APIKey)-1]
				if val := os.Getenv(envKey); val != "" {
					apiKey = val
				}
			} else if m.APIKey != "" {
				apiKey = m.APIKey
			}
			return
		}
	}

	// Default model not found, use first available
	if len(cfg.Models) > 0 {
		name = cfg.Models[0].Name
		if cfg.Models[0].BaseURL != "" {
			baseURL = cfg.Models[0].BaseURL
		}
	}
	return
}

// generateRunID generates a unique run identifier
func generateRunID() string {
	return time.Now().UTC().Format("20060102-150405") + "-" + randomString(6)
}

// randomString generates a random alphanumeric string of length n
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		num, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		b[i] = letters[num.Int64()]
	}
	return string(b)
}

// saveRunState writes the run state snapshot to disk
func saveRunState(dataDir string, runReq *types.RunRequest, prompt string, result *orchestration.RunResult) {
	state := map[string]interface{}{
		"run_id":            filepath.Base(dataDir),
		"flow_name":         runReq.Flow.Name,
		"prompt":            prompt,
		"signal":            string(result.Signal),
		"round_num":         len(result.Stages),
		"total_tokens":      result.Usage.TotalTokens,
		"prompt_tokens":     result.Usage.PromptTokens,
		"completion_tokens": result.Usage.CompletionTokens,
		"created_at":        time.Now().UTC().Format(time.RFC3339),
		"stages":            buildStageSummary(result),
	}

	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(filepath.Join(dataDir, "run_state.json"), data, 0644)
}

// saveRunLog writes the conversation history as JSONL
func saveRunLog(dataDir string, prompt string, result *orchestration.RunResult) {
	f, err := os.OpenFile(filepath.Join(dataDir, "run.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	// User prompt
	userMsg := map[string]interface{}{
		"role":       "user",
		"content":    prompt,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}
	line, _ := json.Marshal(userMsg)
	f.Write(append(line, '\n'))

	// Agent responses - log each individual agent output
	for _, stage := range result.Stages {
		if stage.TeamResult == nil {
			continue
		}
		
		// Log each agent's individual result
		for _, ar := range stage.TeamResult.AgentResults {
			agentMsg := map[string]interface{}{
				"role":       "agent",
				"team_name":  stage.TeamName,
				"stage_name": stage.StageName,
				"content":    ar.Raw,
				"signal":     string(ar.Signal),
				"usage": map[string]int{
					"prompt_tokens":     ar.Usage.PromptTokens,
					"completion_tokens": ar.Usage.CompletionTokens,
					"total_tokens":      ar.Usage.TotalTokens,
				},
				"created_at": time.Now().UTC().Format(time.RFC3339),
			}
			line, _ := json.Marshal(agentMsg)
			f.Write(append(line, '\n'))
		}

		// Also log team summary
		teamMsg := map[string]interface{}{
			"role":       "team",
			"team_name":  stage.TeamName,
			"stage_name": stage.StageName,
			"signal":     string(stage.TeamResult.Signal),
			"usage": map[string]int{
				"prompt_tokens":     stage.TeamResult.Usage.PromptTokens,
				"completion_tokens": stage.TeamResult.Usage.CompletionTokens,
				"total_tokens":      stage.TeamResult.Usage.TotalTokens,
			},
			"agent_count": len(stage.TeamResult.AgentResults),
			"created_at":  time.Now().UTC().Format(time.RFC3339),
		}
		line, _ = json.Marshal(teamMsg)
		f.Write(append(line, '\n'))
	}
}

// saveAgentSessions writes per-team-agent session state files
func saveAgentSessions(dataDir string, runReq *types.RunRequest, result *orchestration.RunResult) {
	for _, stage := range result.Stages {
		if stage.TeamResult == nil {
			continue
		}

		team, ok := runReq.Teams[stage.TeamName]
		if !ok {
			continue
		}

		for _, teamStage := range team.Stages {
			for _, task := range teamStage.Tasks {
				sessionDir := filepath.Join(dataDir, "sessions", stage.TeamName+"-"+task.Agent)
				os.MkdirAll(sessionDir, 0755)

				stateFile := filepath.Join(sessionDir, "state.json")
				if _, err := os.Stat(stateFile); os.IsNotExist(err) {
					state := map[string]interface{}{
						"team_name":  stage.TeamName,
						"agent_name": task.Agent,
						"task_name":  task.Name,
						"signal":     string(stage.TeamResult.Signal),
						"updated_at": time.Now().UTC().Format(time.RFC3339),
					}
					data, _ := json.MarshalIndent(state, "", "  ")
					os.WriteFile(stateFile, data, 0644)
				}
			}
		}
	}
}

// buildStageSummary builds a summary of flow stages for the run state
func buildStageSummary(result *orchestration.RunResult) []map[string]interface{} {
	var stages []map[string]interface{}
	for _, s := range result.Stages {
		summary := map[string]interface{}{
			"stage_name": s.StageName,
			"team_name":  s.TeamName,
		}
		if s.TeamResult != nil {
			summary["signal"] = string(s.TeamResult.Signal)
			summary["usage"] = map[string]int{
				"total_tokens": s.TeamResult.Usage.TotalTokens,
			}
		}
		stages = append(stages, summary)
	}
	return stages
}

// builtinDefaultConfig returns a code-builtin default configuration
// when no flow config file is found. This enables heron to run without
// any user configuration files.
func builtinDefaultConfig() *types.RunRequest {
	return &types.RunRequest{
		Flow: types.FlowConfig{
			Name:          "default",
			LoopMaxRounds: 0,
			Stages: []types.FlowStage{
				{
					Name: "chat",
					Team: "default",
					OnSignal: types.FlowStageSignals{},
				},
			},
		},
		Teams: map[string]types.TeamConfig{
			"default": {
				Name: "default",
				Stages: []types.StageConfig{
					{
						Process: "sequential",
						Tasks: []types.TaskConfig{
							{
								Name:        "chat",
								Agent:       "default",
								Description: "回答用户的问题",
							},
						},
					},
				},
			},
		},
		Agents: map[string]types.AgentConfig{
			"default": {
				Name: "default",
				Persona: types.PersonaConfig{
					Role:      "助手",
					Goal:      "回答用户问题，提供帮助",
					Backstory: "一个乐于助人的 AI 助手",
				},
				Model: types.ModelConfig{
					Provider:    "openai",
					Temperature: 0.7,
					MaxTokens:   2048,
				},
				Tools: types.ToolConfig{
					Builtin: []string{"Read", "Write", "Grep"},
				},
				Loop: types.LoopConfig{
					MaxRounds: 3,
					ToolMode:  "sequential",
				},
				Body: "你是通用问答助手。用简洁的语言回答用户问题。\n\n## 规则\n- 用简洁清晰的语言回答\n- 不知道就说不知道，不要编造",
			},
		},
	}
}
