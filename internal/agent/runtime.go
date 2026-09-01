package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// AgentRunner executes one V1 Agent Call. TeamRuntime owns routing
// and record publication; this package only runs the model/tool loop.
type AgentRunner interface {
	Run(ctx context.Context, agent types.AgentConfig, req types.AgentRequest) (*types.AgentResult, error)
}

// TurnLoop implements one AgentTurn with a bounded Model/Tool loop.
type TurnLoop struct {
	model          types.ModelProvider
	toolExecutor   ToolExecutor
	guardrail      *GuardrailChecker
	routeParser    *RouteParser
	hitl           *HITLGate
	hooks          *HookExecutor
	promptRenderer PromptRenderer
	checkpoints    types.AgentCheckpointStore
	taskRunner     *AsyncToolExecutor
	toolPolicy     ToolPolicy
	contextPolicy  ContextPolicy
}

// ModelContextSizer is an optional provider capability. The Agent package
// keeps the base ModelProvider contract small, while providers that know the
// selected model's input capacity can expose it for ContextManager.
type ModelContextSizer interface {
	MaxInputTokens(config types.ModelConfig) int
}

// ToolExecutor is kept small so the agent package does not depend on the
// concrete tool registry.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error)
}

type ApprovalAwareToolExecutor interface {
	NeedsApproval(name string, args map[string]any) (bool, error)
}

// PromptRenderer renders one Agent prompt.
type PromptRenderer interface {
	Render(agent types.AgentConfig, req types.AgentRequest, rctx RenderContext) ([]types.Message, error)
}

// RenderContext holds the collaboration context visible to an Agent.
type RenderContext struct {
	Variables     map[string]string
	ContextBlocks []types.ContextBlock
}

func NewTurnLoop(
	model types.ModelProvider,
	toolExecutor ToolExecutor,
	guardrail *GuardrailChecker,
	routeParser *RouteParser,
	hitl *HITLGate,
	hooks *HookExecutor,
	promptRenderer PromptRenderer,
) *TurnLoop {
	return &TurnLoop{
		model:          model,
		toolExecutor:   toolExecutor,
		guardrail:      guardrail,
		routeParser:    routeParser,
		hitl:           hitl,
		hooks:          hooks,
		promptRenderer: promptRenderer,
		toolPolicy:     NewDefaultToolPolicy(),
		contextPolicy:  NewDefaultContextPolicy(),
	}
}

func (t *TurnLoop) SetCheckpointStore(store types.AgentCheckpointStore) {
	t.checkpoints = store
}

func (t *TurnLoop) SetTaskRunner(runner *AsyncToolExecutor) {
	t.taskRunner = runner
}

func (t *TurnLoop) SetToolPolicy(policy ToolPolicy) {
	if policy != nil {
		t.toolPolicy = policy
	}
}

func (t *TurnLoop) SetContextPolicy(policy ContextPolicy) {
	if policy != nil {
		t.contextPolicy = policy
	}
}

// Run executes one AgentTurn. An Agent is request/response in V1:
// multiple model/tool rounds are still internal to this one call and no
// background mailbox or active-agent protocol is introduced.
func (t *TurnLoop) Run(ctx context.Context, agent types.AgentConfig, req types.AgentRequest) (result *types.AgentResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var requestStats []types.ModelRequestStats
	var approvalDecision *types.HITLResponse
	defer func() {
		if result != nil {
			result.Requests = append(result.Requests, requestStats...)
			if approvalDecision != nil {
				decision := *approvalDecision
				result.Approval = &decision
			}
		}
	}()
	if t.model == nil {
		return nil, fmt.Errorf("model provider is nil")
	}
	if t.promptRenderer == nil {
		return nil, fmt.Errorf("prompt renderer is nil")
	}
	if t.routeParser == nil {
		t.routeParser = NewRouteParser()
	}

	lastRound := 0
	if t.hooks != nil {
		defer func() {
			payload := hookPayload(agent, req, lastRound, nil, nil)
			if err != nil {
				payload.Error = err.Error()
			} else if result != nil {
				payload.Error = result.Error
			}
			_ = t.hooks.Execute(context.Background(), HookOnEnd, payload)
		}()
		if hookErr := t.hooks.Execute(ctx, HookOnStart, hookPayload(agent, req, 0, nil, nil)); hookErr != nil {
			t.emitErrorHook(ctx, agent, req, 0, hookErr)
			return &types.AgentResult{Status: types.TurnFailed, Reply: hookErr.Error(), Error: hookErr.Error(), Next: &types.Route{Action: types.NextFail, Reason: hookErr.Error()}}, nil
		}
	}

	maxRounds := agent.Loop.MaxRounds
	if maxRounds <= 0 {
		maxRounds = req.MaxAgentRounds
	}
	if maxRounds <= 0 {
		maxRounds = 200
	}
	if req.MaxAgentRounds > 0 && maxRounds > req.MaxAgentRounds {
		maxRounds = req.MaxAgentRounds
	}
	if agent.Budget.MaxModelRounds > 0 && maxRounds > agent.Budget.MaxModelRounds {
		maxRounds = agent.Budget.MaxModelRounds
	}
	toolSchemas := t.buildToolSchemas(agent)
	contextManager := NewContextManager(t.contextConfig(agent))
	contextManager.SetToolSchemas(toolSchemas)
	if req.ResumeCheckpointID != "" && t.checkpoints == nil {
		return nil, fmt.Errorf("agent checkpoint store is not configured")
	}
	budget, err := newBudgetTracker(agent.Budget, 0, time.Now().UTC())
	if err != nil {
		t.emitErrorHook(ctx, agent, req, 0, err)
		return nil, err
	}

	var messages []types.Message
	inputText := contextBlockText(req.ContextBlocks, "input")
	if req.ResumeCheckpointID == "" {
		messages, err = t.promptRenderer.Render(agent, req, RenderContext{
			Variables:     req.Variables,
			ContextBlocks: t.contextPolicy.Build(req),
		})
		if err != nil {
			t.emitErrorHook(ctx, agent, req, 0, err)
			return nil, fmt.Errorf("render prompt: %w", err)
		}
	}

	totalUsage := types.TokenUsage{}
	var lastText string
	var workspaceOps []types.WorkspaceOperation
	toolCalls := 0
	startRound := 0
	observedCompactions := 0
	usedTools := make(map[string]bool)
	successfulTools := make(map[string]bool)
	lastToolSignature := ""
	sameToolCalls := 0
	noProgressRounds := 0
	lastModelText := ""
	sameModelTexts := 0
	structuredRetries := 0
	if req.ResumeCheckpointID != "" {
		checkpoint, loadErr := t.checkpoints.Load(ctx, req.ResumeCheckpointID)
		if loadErr != nil {
			t.emitErrorHook(ctx, agent, req, 0, loadErr)
			return nil, loadErr
		}
		if checkpoint.AgentID != "" && req.AgentID != "" && checkpoint.AgentID != req.AgentID {
			return nil, fmt.Errorf("checkpoint %q belongs to Agent %q", checkpoint.ID, checkpoint.AgentID)
		}
		if compatibilityErr := validateCheckpointCompatibility(checkpoint, agent, contextManager); compatibilityErr != nil {
			return nil, compatibilityErr
		}
		if restoreErr := contextManager.RestoreActive(checkpoint.Messages); restoreErr != nil {
			t.emitErrorHook(ctx, agent, req, 0, restoreErr)
			return nil, restoreErr
		}
		budget.usage = checkpoint.BudgetUsage
		if !checkpoint.BudgetUsage.StartedAt.IsZero() {
			budget.usage.StartedAt = checkpoint.BudgetUsage.StartedAt
		}
		if agent.Budget.MaxWallTime != "" && !budget.usage.StartedAt.IsZero() {
			if duration, parseErr := time.ParseDuration(agent.Budget.MaxWallTime); parseErr == nil {
				budget.deadline = budget.usage.StartedAt.Add(duration)
			}
		}
		totalUsage = checkpoint.Usage
		lastText = checkpoint.LastText
		lastModelText = checkpoint.LastText
		workspaceOps = append([]types.WorkspaceOperation(nil), checkpoint.WorkspaceOps...)
		toolCalls = checkpoint.BudgetUsage.ToolCalls
		startRound = checkpoint.NextRound
		lastToolSignature = checkpoint.LoopState.LastToolSignature
		sameToolCalls = checkpoint.LoopState.SameToolCalls
		noProgressRounds = checkpoint.LoopState.NoProgressRounds
		sameModelTexts = checkpoint.LoopState.SameModelTexts
		for _, name := range checkpoint.LoopState.UsedTools {
			usedTools[name] = true
		}
		for _, name := range checkpoint.LoopState.SuccessfulTools {
			successfulTools[name] = true
		}
		if req.ResumeApprovalID != "" {
			if checkpoint.PendingApproval == nil || checkpoint.PendingApproval.RequestID != req.ResumeApprovalID {
				return nil, fmt.Errorf("checkpoint %q is not waiting for approval %q", checkpoint.ID, req.ResumeApprovalID)
			}
			if req.ResumeApproval == nil {
				return &types.AgentResult{
					Status: types.TurnWaitingApproval,
					Reply:  checkpoint.PendingApproval.Reason,
					Next:   &types.Route{Action: types.NextWaitApproval, Reason: "approval is still pending"},
					Usage:  totalUsage, Checkpoint: checkpoint,
					PendingApproval: checkpoint.PendingApproval,
				}, nil
			}
			pending := checkpoint.PendingApproval
			decisionCopy := *req.ResumeApproval
			approvalDecision = &decisionCopy
			approvedResult := &types.ToolResult{
				Success: req.ResumeApproval.Approved,
			}
			approvalRejected := !req.ResumeApproval.Approved
			policyRejected := false
			if !req.ResumeApproval.Approved {
				approvedResult.Error = "approval denied: " + req.ResumeApproval.Reason
			} else {
				decision, policyReason, policyErr := t.toolDecision(ctx, agent, req, types.ToolCall{
					ID: pending.ToolCallID, Name: pending.ToolName, Arguments: pending.Arguments,
				})
				if policyErr != nil {
					approvedResult.Error = policyErr.Error()
					policyRejected = true
				} else if decision == ToolDeny {
					approvedResult.Error = "approval no longer valid: " + policyReason
					policyRejected = true
				} else {
					toolResult, execErr := t.toolExecutor.Execute(ctx, pending.ToolName, pending.Arguments)
					if execErr != nil {
						approvedResult.Error = execErr.Error()
					} else if toolResult != nil {
						approvedResult = toolResult
					} else {
						approvedResult.Error = "tool returned nil result"
					}
				}
			}
			content := toolResultContent(approvedResult)
			if addErr := contextManager.AddMessage(types.Message{
				Role: "tool", ToolCallID: pending.ToolCallID, Content: content,
			}); addErr != nil {
				return nil, addErr
			}
			workspaceOps = append(workspaceOps, approvedResult.WorkspaceOps...)
			// The Tool call was charged when approval was requested. Resume
			// only adds its result/output usage, so a resumed turn does not
			// double-count the same side effect.
			budget.usage.ToolOutput += len(content)
			budget.usage.FileChanges += countFileChanges(approvedResult.WorkspaceOps)
			if approvedResult.Success {
				successfulTools[pending.ToolName] = true
			}
			if approvalRejected || policyRejected {
				return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
					startRound, lastText, workspaceOps, toolCalls, types.TurnFailed,
					errors.New(content), nil,
					loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools)), nil
			}
		}
		if req.ResumeTaskID != "" {
			if t.taskRunner == nil {
				return nil, errors.New("async Tool task runner is not configured")
			}
			if checkpoint.PendingTool == nil || checkpoint.PendingTool.TaskID != req.ResumeTaskID {
				return nil, fmt.Errorf("checkpoint %q is not waiting for task %q", checkpoint.ID, req.ResumeTaskID)
			}
			task, taskErr := t.taskRunner.Load(ctx, req.ResumeTaskID)
			if taskErr != nil {
				return nil, taskErr
			}
			if task.Status == types.ToolTaskQueued || task.Status == types.ToolTaskRunning {
				result := &types.AgentResult{
					Status:     types.TurnWaitingTool,
					Reply:      fmt.Sprintf("Tool task %q is still running", task.ID),
					Next:       &types.Route{Action: types.NextWaitTool, Reason: "tool task is still running"},
					Usage:      totalUsage,
					ToolCalls:  toolCalls,
					TaskID:     task.ID,
					Checkpoint: checkpoint,
				}
				return result, nil
			}
			content := task.Error
			if task.Result != nil {
				content = toolResultContent(task.Result)
			}
			if addErr := contextManager.AddMessage(types.Message{
				Role: "tool", ToolCallID: checkpoint.PendingTool.ToolCallID, Content: content,
			}); addErr != nil {
				return nil, addErr
			}
			if task.Result != nil {
				workspaceOps = append(workspaceOps, task.Result.WorkspaceOps...)
			}
			if task.Status == types.ToolTaskCompleted {
				successfulTools[checkpoint.PendingTool.ToolName] = true
				t.emitToolEndHook(ctx, agent, req, startRound,
					types.ToolCall{ID: checkpoint.PendingTool.ToolCallID, Name: checkpoint.PendingTool.ToolName},
					task.Result,
				)
			}
			if task.Status != types.ToolTaskCompleted {
				err := fmt.Errorf("async Tool task %q ended with status %s: %s", task.ID, task.Status, task.Error)
				return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
					startRound, lastText, workspaceOps, toolCalls, types.TurnFailed, err, nil,
					loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools)), nil
			}
		}
		if strings.TrimSpace(inputText) != "" {
			if addErr := contextManager.AddMessage(types.Message{
				Role: "user", Content: inputText, Parts: contextBlockParts(req.ContextBlocks, "input"),
			}); addErr != nil {
				t.emitErrorHook(ctx, agent, req, startRound, addErr)
				return nil, addErr
			}
		}
	} else {
		for _, message := range messages {
			if addErr := contextManager.AddMessage(message); addErr != nil {
				t.emitErrorHook(ctx, agent, req, 0, addErr)
				return nil, addErr
			}
		}
	}
	// A resumed approval/task has already appended its Tool result above. It
	// must not be reintroduced as a fresh user turn, but it does need the
	// same completion/state accounting as an ordinary Tool round.
	if req.ResumeCheckpointID != "" && t.checkpoints != nil {
		defer func() {
			if err == nil && result != nil &&
				result.Status != types.TurnWaitingInput &&
				result.Status != types.TurnWaitingTool &&
				result.Status != types.TurnWaitingApproval {
				_ = t.checkpoints.Delete(context.Background(), req.ResumeCheckpointID)
			}
		}()
	}

	for round := startRound; round < maxRounds; round++ {
		lastRound = round
		if budgetErr := budget.beforeModel(ctx); budgetErr != nil {
			t.emitErrorHook(ctx, agent, req, round, budgetErr)
			if errors.Is(budgetErr, context.Canceled) || errors.Is(budgetErr, context.DeadlineExceeded) {
				return nil, budgetErr
			}
			return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage, round, lastText, workspaceOps, toolCalls, types.TurnFailed, budgetErr, nil,
				loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools)), nil
		}
		select {
		case <-ctx.Done():
			err := ctx.Err()
			t.emitErrorHook(ctx, agent, req, round, err)
			return nil, err
		default:
		}
		if round == 0 && t.guardrail != nil {
			if guardErr := t.guardrail.CheckInput(inputText); guardErr != nil {
				t.emitErrorHook(ctx, agent, req, round, guardErr)
				return &types.AgentResult{Status: types.TurnFailed, Reply: guardErr.Error(), Error: guardErr.Error(), Next: &types.Route{Action: types.NextCoordinate, Reason: guardErr.Error()}}, nil
			}
		}

		modelConfig := structuredModelConfig(agent)
		var resp *types.ChatResponse
		var chatErr error
		contextRecovered := false
		modelRetries := 0
		for {
			requestMessages := contextManager.Messages()
			contextStats := contextManager.Stats()
			requestStatIndex := len(requestStats)
			requestStats = append(requestStats, buildModelRequestStats(
				round,
				requestMessages,
				toolSchemas,
				contextStats.EstimatedTokens,
				contextManager.CompactionCount() > observedCompactions || contextRecovered,
			))
			observedCompactions = contextManager.CompactionCount()

			resp, chatErr = t.model.Chat(ctx, requestMessages, toolSchemas, modelConfig)
			if resp != nil {
				requestStats[requestStatIndex].Usage = resp.Usage
				if resp.Model != "" {
					requestStats[requestStatIndex].Model = resp.Model
				}
			}
			if chatErr == nil {
				break
			}
			if isContextLimitError(chatErr) && !contextRecovered {
				if compactErr := contextManager.CompactForce(ctx); compactErr == nil {
					contextRecovered = true
					continue
				}
			}
			maxRetries := agent.Loop.MaxModelRetries
			if maxRetries == 0 {
				maxRetries = 2
			}
			if maxRetries < 0 || !isRetryableModelError(chatErr) || modelRetries >= maxRetries {
				break
			}
			modelRetries++
			if retryErr := waitModelRetry(ctx, modelRetries); retryErr != nil {
				chatErr = retryErr
				break
			}
		}
		if chatErr != nil {
			t.emitErrorHook(ctx, agent, req, round, chatErr)
			return nil, fmt.Errorf("llm chat: %w", chatErr)
		}
		if resp == nil {
			nilErr := fmt.Errorf("model returned nil response")
			t.emitErrorHook(ctx, agent, req, round, nilErr)
			return nil, nilErr
		}
		budget.usage.ModelRounds++
		budget.usage.InputTokens += resp.Usage.PromptTokens
		budget.usage.OutputTokens += resp.Usage.CompletionTokens + resp.Usage.ReasoningTokens
		if budgetErr := budget.checkUsage(); budgetErr != nil {
			t.emitErrorHook(ctx, agent, req, round, budgetErr)
			return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage, round+1, resp.Text, workspaceOps, toolCalls, types.TurnFailed, budgetErr, nil,
				loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools)), nil
		}
		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.ReasoningTokens += resp.Usage.ReasoningTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens
		totalUsage.PromptCacheHitTokens += resp.Usage.PromptCacheHitTokens
		totalUsage.PromptCacheMissTokens += resp.Usage.PromptCacheMissTokens
		totalUsage.CacheReadInputTokens += resp.Usage.CacheReadInputTokens
		totalUsage.CacheCreationInputTokens += resp.Usage.CacheCreationInputTokens
		if agent.Structured != nil && isStructuredOutputTruncated(resp.FinishReason) {
			truncatedErr := errors.New("structured output truncated: model stopped at the output token limit")
			if structuredRetries < 1 && round+1 < maxRounds {
				structuredRetries++
				if addErr := contextManager.AddMessage(types.Message{
					Role: "assistant", Content: resp.Text,
				}); addErr != nil {
					return nil, addErr
				}
				if addErr := contextManager.AddMessage(types.Message{
					Role:    "user",
					Content: "The previous JSON response was truncated. Return one complete JSON object only, with every required field, and no explanation or Markdown.",
				}); addErr != nil {
					return nil, addErr
				}
				lastText = resp.Text
				continue
			}
			t.emitErrorHook(ctx, agent, req, round, truncatedErr)
			return &types.AgentResult{
				Status:       types.TurnFailed,
				Reply:        resp.Text,
				Error:        truncatedErr.Error(),
				Usage:        totalUsage,
				WorkspaceOps: workspaceOps,
				ToolCalls:    toolCalls,
				Next: &types.Route{
					Action: types.NextCoordinate,
					Reason: truncatedErr.Error(),
				},
			}, nil
		}
		lastText = resp.Text
		if strings.TrimSpace(resp.Text) != "" && strings.TrimSpace(resp.Text) == strings.TrimSpace(lastModelText) {
			sameModelTexts++
		} else {
			sameModelTexts = 1
			lastModelText = resp.Text
		}

		if len(resp.ToolCalls) == 0 {
			if t.guardrail != nil {
				if guardErr := t.guardrail.CheckOutput(resp.Text); guardErr != nil {
					t.emitErrorHook(ctx, agent, req, round, guardErr)
					return &types.AgentResult{Status: types.TurnFailed, Reply: resp.Text, Error: guardErr.Error(), Usage: totalUsage, WorkspaceOps: workspaceOps, ToolCalls: toolCalls, Next: &types.Route{Action: types.NextCoordinate, Reason: guardErr.Error()}}, nil
				}
			}
			action, cleanText := t.routeParser.ParseWithMode(resp.Text, maxRounds > 1)
			var parsed any
			if agent.Structured != nil {
				parsed, err = NewStructuredOutputManager().ParseAndValidate(cleanText, agent.Structured)
				if err != nil {
					if structuredRetries < 1 && round+1 < maxRounds {
						structuredRetries++
						if addErr := contextManager.AddMessage(types.Message{
							Role: "assistant", Content: resp.Text,
						}); addErr != nil {
							return nil, addErr
						}
						if addErr := contextManager.AddMessage(types.Message{
							Role:    "user",
							Content: "The previous response was not valid JSON for the required schema. Return one complete JSON object only; do not explain, use Markdown, or omit required fields.",
						}); addErr != nil {
							return nil, addErr
						}
						lastText = resp.Text
						continue
					}
					t.emitErrorHook(ctx, agent, req, round, err)
					return &types.AgentResult{Status: types.TurnFailed, Reply: cleanText, Error: fmt.Sprintf("structured output: %v", err), Usage: totalUsage, WorkspaceOps: workspaceOps, ToolCalls: toolCalls, Next: &types.Route{Action: types.NextCoordinate, Reason: err.Error()}}, nil
				}
			}
			route := routeFromAction(action)
			if parsedRoute := routeFromParsed(parsed); parsedRoute != nil {
				route = parsedRoute
			}
			route = normalizeStructuredRoute(agent.Structured, route)
			reply := cleanText
			if parsedReply := stringFromParsed(parsed, "reply"); parsedReply != "" {
				reply = parsedReply
			}
			if feedback := completionFeedback(agent.Completion, agent.Structured, parsed, usedTools, successfulTools, toolCalls, workspaceOps); feedback != "" {
				if round+1 >= maxRounds {
					return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
						round+1, resp.Text, workspaceOps, toolCalls, types.TurnFailed,
						errors.New(feedback), nil, loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools)), nil
				}
				if addErr := contextManager.AddMessage(types.Message{Role: "assistant", Content: resp.Text}); addErr != nil {
					return nil, addErr
				}
				if addErr := contextManager.AddMessage(types.Message{Role: "user", Content: "## Completion Feedback\n" + feedback}); addErr != nil {
					return nil, addErr
				}
				lastText = resp.Text
				continue
			}
			if agent.Loop.MaxSameModelTexts > 0 && sameModelTexts >= agent.Loop.MaxSameModelTexts {
				stuck := fmt.Errorf("agent stuck: same model response repeated %d times", sameModelTexts)
				return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
					round+1, resp.Text, workspaceOps, toolCalls, types.TurnFailed, stuck, nil,
					loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools)), nil
			}
			return &types.AgentResult{Status: types.TurnCompleted, Reply: reply, Parsed: parsed, Next: route, Usage: totalUsage, WorkspaceOps: workspaceOps, ToolCalls: toolCalls}, nil
		}

		if budgetErr := budget.beforeTool(ctx, len(resp.ToolCalls)); budgetErr != nil {
			t.emitErrorHook(ctx, agent, req, round, budgetErr)
			return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage, round, resp.Text, workspaceOps, toolCalls, types.TurnFailed, budgetErr, nil,
				loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools)), nil
		}
		toolCalls += len(resp.ToolCalls)
		// Charge the model-requested Tool calls before any policy gate. An
		// approval wait or a denied call must not bypass MaxToolCalls.
		budget.addTool(len(resp.ToolCalls), 0, 0)
		for _, call := range resp.ToolCalls {
			usedTools[call.Name] = true
		}
		signature := toolCallSignature(resp.ToolCalls)
		if signature == lastToolSignature {
			sameToolCalls++
		} else {
			sameToolCalls = 1
			lastToolSignature = signature
		}
		if addErr := contextManager.AddMessage(types.Message{Role: "assistant", Content: resp.Text, ToolCalls: resp.ToolCalls}); addErr != nil {
			t.emitErrorHook(ctx, agent, req, round, addErr)
			return nil, addErr
		}
		if asyncCall, ok := t.selectAsyncTool(agent, resp.ToolCalls); ok {
			if len(resp.ToolCalls) != 1 {
				err := errors.New("an asynchronous Tool call must be the only Tool call in a model response")
				return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
					round, resp.Text, workspaceOps, toolCalls, types.TurnFailed, err, nil), nil
			}
			if t.taskRunner == nil {
				err := errors.New("async Tool task runner is not configured")
				return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
					round, resp.Text, workspaceOps, toolCalls, types.TurnFailed, err, nil), nil
			}
			decision, reason, policyErr := t.toolDecision(ctx, agent, req, asyncCall)
			if policyErr != nil || decision == ToolDeny {
				if policyErr == nil {
					policyErr = errors.New(reason)
				}
				return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
					round, resp.Text, workspaceOps, toolCalls, types.TurnFailed, policyErr, nil,
					loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools)), nil
			}
			if decision == ToolRequireApproval {
				pending := &types.AgentPendingApproval{
					RequestID:   approvalID(req, asyncCall),
					CallID:      req.CallID,
					ToolCallID:  asyncCall.ID,
					ToolName:    asyncCall.Name,
					Arguments:   asyncCall.Arguments,
					Reason:      reason,
					RequestedAt: time.Now().UTC(),
					Channel:     "agent",
				}
				waiting := &types.AgentResult{
					Status:          types.TurnWaitingApproval,
					Reply:           reason,
					Next:            &types.Route{Action: types.NextWaitApproval, Reason: reason},
					Usage:           totalUsage,
					ToolCalls:       toolCalls,
					PendingApproval: pending,
				}
				return t.saveApprovalCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
					round+1, resp.Text, workspaceOps, waiting,
					loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools))
			}
			if hookErr := t.executeHook(ctx, HookOnToolStart, hookPayload(agent, req, round, &asyncCall, nil)); hookErr != nil {
				t.emitErrorHook(ctx, agent, req, round, hookErr)
				return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
					round, resp.Text, workspaceOps, toolCalls, types.TurnFailed, hookErr, nil), nil
			}
			task := types.ToolTask{
				ID:            asyncTaskID(req, asyncCall),
				FlowSessionID: req.FlowSessionID,
				TeamID:        req.TeamID,
				TeamTurnID:    req.TeamTurnID,
				CallTurnID:    req.CallTurnID,
				AgentTurnID:   req.AgentTurnID,
				CallID:        req.CallID,
				ToolCallID:    asyncCall.ID,
				ToolName:      asyncCall.Name,
				Arguments:     asyncCall.Arguments,
			}
			if taskErr := t.taskRunner.Start(ctx, task); taskErr != nil {
				t.emitErrorHook(ctx, agent, req, round, taskErr)
				return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
					round, resp.Text, workspaceOps, toolCalls, types.TurnFailed, taskErr, nil), nil
			}
			waitResult := &types.AgentResult{
				Status:    types.TurnWaitingTool,
				Reply:     fmt.Sprintf("Tool task %q queued", task.ID),
				Next:      &types.Route{Action: types.NextWaitTool, Reason: "waiting for asynchronous Tool task"},
				Usage:     totalUsage,
				ToolCalls: toolCalls,
				TaskID:    task.ID,
			}
			checkpoint, checkpointErr := t.saveToolCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
				round+1, resp.Text, workspaceOps, &types.AgentPendingTool{
					TaskID: task.ID, ToolCallID: asyncCall.ID, ToolName: asyncCall.Name,
				})
			if checkpointErr != nil {
				return nil, checkpointErr
			}
			if checkpoint != nil {
				waitResult.Checkpoint = checkpoint
			}
			return waitResult, nil
		}
		results := t.executeToolCalls(ctx, agent, req, round, req.MaxParallelTools, resp.ToolCalls)
		for i, call := range resp.ToolCalls {
			toolResult := results[i]
			if toolResult == nil {
				continue
			}
			if toolResult.Success {
				successfulTools[call.Name] = true
			}
			content := toolResultContent(toolResult)
			if toolResult.Next != nil && toolResult.Next.Action == types.NextWaitApproval {
				continue
			}
			if addErr := contextManager.AddMessage(types.Message{Role: "tool", ToolCallID: call.ID, Content: content}); addErr != nil {
				t.emitErrorHook(ctx, agent, req, round, addErr)
				return nil, addErr
			}
			workspaceOps = append(workspaceOps, toolResult.WorkspaceOps...)
			budget.usage.ToolOutput += len(content)
			budget.usage.FileChanges += countFileChanges(toolResult.WorkspaceOps)
			if budgetErr := budget.checkUsage(); budgetErr != nil {
				t.emitErrorHook(ctx, agent, req, round, budgetErr)
				return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage, round+1, resp.Text, workspaceOps, toolCalls, types.TurnFailed, budgetErr, nil), nil
			}
		}
		progress := false
		for _, toolResult := range results {
			if toolResult != nil && (toolResult.Success || len(toolResult.WorkspaceOps) > 0 || len(toolResult.RecordRefs) > 0) {
				progress = true
				break
			}
		}
		if progress {
			noProgressRounds = 0
		} else {
			noProgressRounds++
		}
		waitingForContinuation := false
		for _, toolResult := range results {
			if toolResult == nil {
				continue
			}
			if toolResult.PendingInput != nil {
				waiting := &types.AgentResult{
					Status:       types.TurnWaitingInput,
					Reply:        toolResultContent(toolResult),
					Next:         toolResult.Next,
					Usage:        totalUsage,
					WorkspaceOps: workspaceOps,
					ToolCalls:    toolCalls,
				}
				return t.saveWaitingCheckpoint(ctx, agent, req, contextManager, budget, totalUsage, round+1, lastText, workspaceOps, toolCalls, waiting,
					loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools))
			}
			if toolResult.Next == nil || toolResult.Next.Action == types.NextProceed {
				continue
			}
			waitingForContinuation = toolResult.Next.Action == types.NextWaitApproval ||
				toolResult.Next.Action == types.NextWaitTool
			agentResult := &types.AgentResult{
				Status:          statusForRoute(toolResult.Next),
				Reply:           toolResultContent(toolResult),
				Next:            toolResult.Next,
				Usage:           totalUsage,
				WorkspaceOps:    workspaceOps,
				ToolCalls:       toolCalls,
				PendingApproval: toolResult.PendingApproval,
			}
			if toolResult.Next.Action == types.NextWaitApproval {
				return t.saveApprovalCheckpoint(ctx, agent, req, contextManager, budget, totalUsage, round+1, lastText, workspaceOps, agentResult,
					loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools))
			}
			return agentResult, nil
		}
		if !waitingForContinuation {
			if stuckReason := stuckReason(agent.Loop, sameToolCalls, noProgressRounds, sameModelTexts); stuckReason != "" {
				if agent.Loop.StuckAction == "ask_user" {
					waiting := &types.AgentResult{
						Status: types.TurnWaitingInput,
						Reply:  "Agent appears stuck: " + stuckReason,
						Next:   &types.Route{Action: types.NextProceed, Reason: stuckReason},
						Usage:  totalUsage, WorkspaceOps: workspaceOps, ToolCalls: toolCalls,
					}
					return t.saveWaitingCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
						round+1, lastText, workspaceOps, toolCalls, waiting,
						loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools))
				}
				return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
					round+1, lastText, workspaceOps, toolCalls, types.TurnFailed,
					errors.New("agent stuck: "+stuckReason), nil,
					loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools)), nil
			}
		}
	}

	action, cleanText := t.routeParser.ParseWithMode(lastText, true)
	if feedback := completionFeedback(agent.Completion, agent.Structured, nil, usedTools, successfulTools, toolCalls, workspaceOps); feedback != "" {
		return t.resultWithCheckpoint(ctx, agent, req, contextManager, budget, totalUsage,
			maxRounds, lastText, workspaceOps, toolCalls, types.TurnFailed,
			errors.New(feedback), nil,
			loopStateSnapshot(lastToolSignature, sameToolCalls, noProgressRounds, sameModelTexts, usedTools, successfulTools)), nil
	}
	if agent.Structured != nil {
		parsed, parseErr := NewStructuredOutputManager().ParseAndValidate(cleanText, agent.Structured)
		if parseErr != nil {
			t.emitErrorHook(ctx, agent, req, maxRounds, parseErr)
			return &types.AgentResult{Status: types.TurnFailed, Reply: cleanText, Error: fmt.Sprintf("structured output: %v", parseErr), Usage: totalUsage, WorkspaceOps: workspaceOps, ToolCalls: toolCalls, Next: &types.Route{Action: types.NextCoordinate, Reason: parseErr.Error()}}, nil
		}
		route := routeFromAction(action)
		if parsedRoute := routeFromParsed(parsed); parsedRoute != nil {
			route = parsedRoute
		}
		route = normalizeStructuredRoute(agent.Structured, route)
		reply := cleanText
		if parsedReply := stringFromParsed(parsed, "reply"); parsedReply != "" {
			reply = parsedReply
		}
		return &types.AgentResult{Status: types.TurnCompleted, Reply: reply, Parsed: parsed, Next: route, Usage: totalUsage, WorkspaceOps: workspaceOps, ToolCalls: toolCalls}, nil
	}
	return &types.AgentResult{Status: types.TurnCompleted, Reply: cleanText, Next: &types.Route{Action: action, Reason: "agent loop limit reached"}, Usage: totalUsage, WorkspaceOps: workspaceOps, ToolCalls: toolCalls}, nil
}

func isStructuredOutputTruncated(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens":
		return true
	default:
		return false
	}
}

func contextBlockText(blocks []types.ContextBlock, kind string) string {
	for _, block := range blocks {
		if block.Kind == kind {
			return block.Text
		}
	}
	return ""
}

func (t *TurnLoop) resultWithCheckpoint(
	ctx context.Context,
	agent types.AgentConfig,
	req types.AgentRequest,
	contextManager *MessageContextManager,
	budget *budgetTracker,
	usage types.TokenUsage,
	nextRound int,
	lastText string,
	workspaceOps []types.WorkspaceOperation,
	toolCalls int,
	status types.TurnStatus,
	runErr error,
	pending *types.AgentPendingInput,
	loopStates ...types.AgentLoopState,
) *types.AgentResult {
	result := &types.AgentResult{
		Status: status, Error: runErr.Error(), Reply: runErr.Error(),
		Next:         &types.Route{Action: types.NextFail, Reason: runErr.Error()},
		Usage:        usage,
		WorkspaceOps: workspaceOps, ToolCalls: toolCalls,
	}
	checkpoint, checkpointErr := t.saveCheckpoint(ctx, agent, req, contextManager, budget, usage, nextRound, lastText, workspaceOps, pending, status, loopStates...)
	if checkpointErr == nil {
		result.Checkpoint = checkpoint
	} else {
		result.Error += "; checkpoint: " + checkpointErr.Error()
	}
	return result
}

func (t *TurnLoop) saveWaitingCheckpoint(
	ctx context.Context,
	agent types.AgentConfig,
	req types.AgentRequest,
	contextManager *MessageContextManager,
	budget *budgetTracker,
	usage types.TokenUsage,
	nextRound int,
	lastText string,
	workspaceOps []types.WorkspaceOperation,
	toolCalls int,
	result *types.AgentResult,
	loopStates ...types.AgentLoopState,
) (*types.AgentResult, error) {
	pending := pendingInputFromResult(result)
	checkpoint, err := t.saveCheckpoint(ctx, agent, req, contextManager, budget, usage, nextRound, lastText, workspaceOps, pending, types.TurnWaitingInput, loopStates...)
	if err != nil {
		return nil, err
	}
	result.Status = types.TurnWaitingInput
	result.Checkpoint = checkpoint
	return result, nil
}

func (t *TurnLoop) saveApprovalCheckpoint(
	ctx context.Context,
	agent types.AgentConfig,
	req types.AgentRequest,
	contextManager *MessageContextManager,
	budget *budgetTracker,
	usage types.TokenUsage,
	nextRound int,
	lastText string,
	workspaceOps []types.WorkspaceOperation,
	result *types.AgentResult,
	loopStates ...types.AgentLoopState,
) (*types.AgentResult, error) {
	if result == nil || result.PendingApproval == nil {
		return nil, errors.New("approval checkpoint requires pending approval")
	}
	checkpoint, err := t.saveCheckpoint(
		ctx, agent, req, contextManager, budget, usage, nextRound,
		lastText, workspaceOps, nil, types.TurnWaitingApproval,
		loopStates...,
	)
	if err != nil {
		return nil, err
	}
	if checkpoint == nil {
		return nil, errors.New("approval checkpoint store is not configured")
	}
	result.PendingApproval.CheckpointID = checkpoint.ID
	checkpoint.PendingApproval = result.PendingApproval
	if err := t.checkpoints.Save(ctx, *checkpoint); err != nil {
		return nil, err
	}
	result.Status = types.TurnWaitingApproval
	result.Checkpoint = checkpoint
	return result, nil
}

func (t *TurnLoop) saveCheckpoint(
	ctx context.Context,
	agent types.AgentConfig,
	req types.AgentRequest,
	contextManager *MessageContextManager,
	budget *budgetTracker,
	usage types.TokenUsage,
	nextRound int,
	lastText string,
	workspaceOps []types.WorkspaceOperation,
	pending *types.AgentPendingInput,
	status types.TurnStatus,
	loopStates ...types.AgentLoopState,
) (*types.AgentCheckpoint, error) {
	if t.checkpoints == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	var loopState types.AgentLoopState
	if len(loopStates) > 0 {
		loopState = loopStates[0]
	}
	checkpoint := &types.AgentCheckpoint{
		Version: types.CurrentAgentCheckpointVersion, ID: newCheckpointID(req), FlowSessionID: req.FlowSessionID,
		TeamID: req.TeamID, TeamTurnID: req.TeamTurnID, CallID: req.CallID,
		AgentID: req.AgentID, AgentTurnID: req.AgentTurnID, Attempt: req.Attempt,
		RecoveryOf: req.RecoveryOf, Status: status, NextRound: nextRound,
		LastText: lastText, Messages: contextManager.Messages(), Usage: usage,
		BudgetUsage: budget.usageSnapshot(), WorkspaceOps: append([]types.WorkspaceOperation(nil), workspaceOps...),
		PendingInput: pending, LoopState: loopState, CreatedAt: now, UpdatedAt: now,
		Compatibility: checkpointCompatibility(agent, contextManager),
	}
	if err := t.checkpoints.Save(ctx, *checkpoint); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

func checkpointCompatibility(agent types.AgentConfig, contextManager *MessageContextManager) types.AgentCheckpointCompatibility {
	return types.AgentCheckpointCompatibility{
		AgentConfigHash: hashJSON(struct {
			Name       string
			Body       string
			Tools      types.ToolConfig
			Skills     []string
			Rules      []string
			Loop       types.LoopConfig
			Context    types.ContextConfig
			Completion types.CompletionConfig
		}{
			Name: agent.Name, Body: agent.Body, Tools: agent.Tools,
			Skills: agent.Skills, Rules: agent.Rules, Loop: agent.Loop,
			Context: agent.Context, Completion: agent.Completion,
		}),
		ModelFingerprint: hashJSON(struct {
			Provider string
			Model    string
		}{agent.Model.Provider, agent.Model.Model}),
		ToolSchemaHash:    contextManager.ToolSchemaHash(),
		PromptVersion:     "heron-agent-prompt-v2",
		ContextPolicyHash: hashJSON(agent.Context),
	}
}

func validateCheckpointCompatibility(
	checkpoint *types.AgentCheckpoint,
	agent types.AgentConfig,
	contextManager *MessageContextManager,
) error {
	if checkpoint == nil {
		return nil
	}
	if checkpoint.Version > types.CurrentAgentCheckpointVersion {
		return fmt.Errorf(
			"checkpoint %q uses unsupported version %d (current %d)",
			checkpoint.ID, checkpoint.Version, types.CurrentAgentCheckpointVersion,
		)
	}
	compat := checkpoint.Compatibility
	if compat.AgentConfigHash == "" && compat.ModelFingerprint == "" &&
		compat.ToolSchemaHash == "" && compat.PromptVersion == "" &&
		compat.ContextPolicyHash == "" {
		return nil
	}
	current := checkpointCompatibility(agent, contextManager)
	if compat.AgentConfigHash != "" && compat.AgentConfigHash != current.AgentConfigHash {
		return fmt.Errorf("checkpoint %q Agent configuration is incompatible", checkpoint.ID)
	}
	if compat.ModelFingerprint != "" && compat.ModelFingerprint != current.ModelFingerprint {
		return fmt.Errorf("checkpoint %q model configuration is incompatible", checkpoint.ID)
	}
	if compat.ToolSchemaHash != "" && compat.ToolSchemaHash != current.ToolSchemaHash {
		return fmt.Errorf("checkpoint %q Tool Schema is incompatible", checkpoint.ID)
	}
	if compat.PromptVersion != "" && compat.PromptVersion != current.PromptVersion {
		return fmt.Errorf("checkpoint %q prompt version is incompatible", checkpoint.ID)
	}
	if compat.ContextPolicyHash != "" && compat.ContextPolicyHash != current.ContextPolicyHash {
		return fmt.Errorf("checkpoint %q context policy is incompatible", checkpoint.ID)
	}
	return nil
}

func (t *TurnLoop) saveToolCheckpoint(
	ctx context.Context,
	agent types.AgentConfig,
	req types.AgentRequest,
	contextManager *MessageContextManager,
	budget *budgetTracker,
	usage types.TokenUsage,
	nextRound int,
	lastText string,
	workspaceOps []types.WorkspaceOperation,
	pending *types.AgentPendingTool,
	loopStates ...types.AgentLoopState,
) (*types.AgentCheckpoint, error) {
	checkpoint, err := t.saveCheckpoint(ctx, agent, req, contextManager, budget, usage, nextRound, lastText, workspaceOps, nil, types.TurnWaitingTool, loopStates...)
	if err != nil || checkpoint == nil {
		return checkpoint, err
	}
	checkpoint.PendingTool = pending
	if err := t.checkpoints.Save(ctx, *checkpoint); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

func pendingInputFromResult(result *types.AgentResult) *types.AgentPendingInput {
	if result == nil || result.Status != types.TurnWaitingInput {
		return nil
	}
	pending := &types.AgentPendingInput{Question: result.Reply}
	if strings.HasPrefix(result.Reply, "{") {
		var raw map[string]any
		if json.Unmarshal([]byte(result.Reply), &raw) == nil {
			if question, ok := raw["question"].(string); ok {
				pending.Question = question
			}
			if header, ok := raw["header"].(string); ok {
				pending.Header = header
			}
			if multi, ok := raw["multi_select"].(bool); ok {
				pending.MultiSelect = multi
			}
			if options, ok := raw["options"].([]any); ok {
				for _, item := range options {
					if value, ok := item.(string); ok {
						pending.Options = append(pending.Options, value)
					}
				}
			}
		}
	}
	return pending
}

func statusForRoute(route *types.Route) types.TurnStatus {
	if route == nil {
		return types.TurnCompleted
	}
	if route.Action == types.NextWaitTool {
		return types.TurnWaitingTool
	}
	if route.Action == types.NextWaitApproval {
		return types.TurnWaitingApproval
	}
	return types.TurnCompleted
}

func (t *TurnLoop) selectAsyncTool(agent types.AgentConfig, calls []types.ToolCall) (types.ToolCall, bool) {
	if len(agent.Loop.AsyncTools) == 0 {
		return types.ToolCall{}, false
	}
	allowed := make(map[string]struct{}, len(agent.Loop.AsyncTools))
	for _, name := range agent.Loop.AsyncTools {
		allowed[name] = struct{}{}
	}
	for _, call := range calls {
		if _, ok := allowed[call.Name]; ok {
			return call, true
		}
	}
	return types.ToolCall{}, false
}

func asyncTaskID(req types.AgentRequest, call types.ToolCall) string {
	base := req.AgentTurnID
	if base == "" {
		base = req.CallID
	}
	if base == "" {
		base = "agent"
	}
	return fmt.Sprintf("%s:%s", base, call.ID)
}

func countFileChanges(operations []types.WorkspaceOperation) int {
	count := 0
	for _, operation := range operations {
		if operation.Kind == "write" {
			count++
		}
	}
	return count
}

func (t *TurnLoop) executeToolCalls(ctx context.Context, agent types.AgentConfig, req types.AgentRequest, round, maxParallel int, calls []types.ToolCall) []*types.ToolResult {
	allowed := make(map[string]struct{}, len(agent.Tools.Builtin)+len(agent.Tools.Custom)+len(agent.Tools.MCP))
	for _, name := range agent.Tools.Builtin {
		allowed[name] = struct{}{}
	}
	for _, name := range agent.Tools.Custom {
		allowed[name] = struct{}{}
	}
	for _, name := range agent.Tools.MCP {
		allowed[name] = struct{}{}
	}
	filtered := make([]types.ToolCall, len(calls))
	copy(filtered, calls)
	for i, call := range filtered {
		if _, ok := allowed[call.Name]; !ok {
			filtered[i].Name = ""
		}
	}
	for i, call := range filtered {
		if call.Name == "" {
			continue
		}
		decision, reason, policyErr := t.toolDecision(ctx, agent, req, call)
		if policyErr != nil {
			filtered[i].Name = ""
			filtered[i].Arguments = map[string]any{"_policy_error": policyErr.Error()}
			continue
		}
		if decision == ToolDeny {
			filtered[i].Name = ""
			filtered[i].Arguments = map[string]any{"_policy_error": reason}
			continue
		}
		if decision == ToolRequireApproval {
			return approvalResults(filtered, i, req, reason)
		}
	}
	if batch, ok := t.toolExecutor.(BatchToolExecutor); ok && parallelToolsEnabled(agent) {
		results := make([]*types.ToolResult, len(filtered))
		parallelCalls := make([]types.ToolCall, 0, len(filtered))
		parallelIndexes := make([]int, 0, len(filtered))
		for i, call := range filtered {
			if call.Name == "" {
				results[i] = &types.ToolResult{Success: false, Error: "tool is not allowed for this Agent"}
				continue
			}
			if err := t.executeHook(ctx, HookOnToolStart, hookPayload(agent, req, round, &call, nil)); err != nil {
				results[i] = &types.ToolResult{Success: false, Error: err.Error()}
				t.emitErrorHook(ctx, agent, req, round, err)
				continue
			}
			parallelCalls = append(parallelCalls, call)
			parallelIndexes = append(parallelIndexes, i)
		}
		batchResults := NewToolBatchExecutor(batch, maxParallel, true).Execute(ctx, parallelCalls)
		for i, result := range batchResults {
			results[parallelIndexes[i]] = result
			t.emitToolEndHook(ctx, agent, req, round, parallelCalls[i], result)
		}
		return results
	}

	results := make([]*types.ToolResult, len(filtered))
	for i, call := range filtered {
		if call.Name == "" {
			results[i] = &types.ToolResult{Success: false, Error: "tool is not allowed for this Agent"}
			continue
		}
		if t.toolExecutor == nil {
			results[i] = &types.ToolResult{Success: false, Error: "tool executor is nil"}
			continue
		}
		if err := t.executeHook(ctx, HookOnToolStart, hookPayload(agent, req, round, &call, nil)); err != nil {
			results[i] = &types.ToolResult{Success: false, Error: err.Error()}
			t.emitErrorHook(ctx, agent, req, round, err)
			continue
		}
		result, err := t.toolExecutor.Execute(ctx, call.Name, call.Arguments)
		if err != nil {
			result = &types.ToolResult{Success: false, Error: err.Error()}
		}
		if result == nil {
			result = &types.ToolResult{Success: false, Error: "tool returned nil result"}
		}
		results[i] = result
		t.emitToolEndHook(ctx, agent, req, round, call, result)
	}
	return results
}

func (t *TurnLoop) toolDecision(ctx context.Context, agent types.AgentConfig, req types.AgentRequest, call types.ToolCall) (ToolDecision, string, error) {
	decision, reason, err := t.toolPolicy.Check(ctx, ToolPolicyRequest{
		Agent: agent, Call: call, FlowID: req.FlowSessionID, TeamID: req.TeamID, CallID: req.CallID,
	})
	if err != nil {
		return decision, reason, err
	}
	if decision == ToolDeny {
		return decision, reason, nil
	}
	if decision == ToolRequireApproval {
		if agent.HITL == nil || !agent.HITL.Enabled {
			return ToolDeny, "Tool requires approval but Agent HITL is disabled", nil
		}
		return decision, reason, nil
	}
	if aware, ok := t.toolExecutor.(ApprovalAwareToolExecutor); ok {
		required, approvalErr := aware.NeedsApproval(call.Name, call.Arguments)
		if approvalErr != nil {
			return ToolDeny, approvalErr.Error(), nil
		}
		if required {
			if agent.HITL == nil || !agent.HITL.Enabled {
				return ToolDeny, "tool requires approval but Agent HITL is disabled", nil
			}
			return ToolRequireApproval, "tool requires human approval", nil
		}
	}
	return decision, reason, nil
}

func approvalResults(calls []types.ToolCall, index int, req types.AgentRequest, reason string) []*types.ToolResult {
	results := make([]*types.ToolResult, len(calls))
	call := calls[index]
	pending := &types.AgentPendingApproval{
		RequestID:   approvalID(req, call),
		CallID:      req.CallID,
		ToolCallID:  call.ID,
		ToolName:    call.Name,
		Arguments:   call.Arguments,
		Reason:      reason,
		RequestedAt: time.Now().UTC(),
		Channel:     "agent",
	}
	results[index] = &types.ToolResult{
		Success:         false,
		Error:           "tool approval required",
		Next:            &types.Route{Action: types.NextWaitApproval, Reason: reason},
		PendingApproval: pending,
	}
	return results
}

func approvalID(req types.AgentRequest, call types.ToolCall) string {
	base := req.AgentTurnID
	if base == "" {
		base = req.CallID
	}
	if base == "" {
		base = "agent"
	}
	return fmt.Sprintf("%s:approval:%s", base, call.ID)
}

func (t *TurnLoop) contextConfig(agent types.AgentConfig) types.ContextConfig {
	config := agent.Context
	if config.MaxInputTokens <= 0 {
		if sizer, ok := t.model.(ModelContextSizer); ok {
			config.MaxInputTokens = sizer.MaxInputTokens(agent.Model)
		}
	}
	return config
}

func (t *TurnLoop) executeHook(ctx context.Context, event string, payload types.HookPayload) error {
	if t.hooks == nil {
		return nil
	}
	return t.hooks.Execute(ctx, event, payload)
}

func (t *TurnLoop) emitErrorHook(ctx context.Context, agent types.AgentConfig, req types.AgentRequest, round int, err error) {
	if err == nil || t.hooks == nil {
		return
	}
	payload := hookPayload(agent, req, round, nil, nil)
	payload.Error = err.Error()
	_ = t.hooks.Execute(ctx, HookOnError, payload)
}

func (t *TurnLoop) emitToolEndHook(ctx context.Context, agent types.AgentConfig, req types.AgentRequest, round int, call types.ToolCall, result *types.ToolResult) {
	if t.hooks == nil {
		return
	}
	payload := hookPayload(agent, req, round, &call, result)
	if result != nil && result.Error != "" {
		payload.Error = result.Error
	}
	_ = t.hooks.Execute(ctx, HookOnToolEnd, payload)
}

func hookPayload(agent types.AgentConfig, req types.AgentRequest, round int, call *types.ToolCall, result *types.ToolResult) types.HookPayload {
	payload := types.HookPayload{
		CallID:      req.CallID,
		CallType:    types.CallAgent,
		AgentID:     req.AgentID,
		AgentTurnID: req.AgentTurnID,
		Round:       round,
		ToolResult:  result,
	}
	if call != nil {
		payload.ToolName = call.Name
		payload.ToolCallID = call.ID
		payload.ToolArgs = call.Arguments
	}
	return payload
}

func toolResultContent(result *types.ToolResult) string {
	content := result.Content
	if len(result.Metadata) > 0 {
		metadata, err := json.Marshal(result.Metadata)
		if err == nil {
			if content != "" {
				content += "\n"
			}
			content += "Metadata: " + string(metadata)
		}
	}
	if content != "" {
		return content
	}
	if result.Error != "" {
		return result.Error
	}
	return ""
}

func parallelToolsEnabled(agent types.AgentConfig) bool {
	return agent.Loop.ToolExecution == "parallel_safe"
}

func isContextLimitError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, phrase := range []string{
		"context length",
		"context window",
		"maximum context",
		"max context",
		"too many tokens",
		"input too long",
		"prompt is too long",
		"token limit",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

func isRetryableModelError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, phrase := range []string{
		"timeout",
		"timed out",
		"temporarily unavailable",
		"service unavailable",
		"connection reset",
		"connection refused",
		"eof",
		"rate limit",
		"too many requests",
		"429",
		"502",
		"503",
		"504",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

func waitModelRetry(ctx context.Context, attempt int) error {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Duration(1<<minInt(attempt-1, 4)) * 100 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func loopStateSnapshot(
	lastToolSignature string,
	sameToolCalls, noProgressRounds, sameModelTexts int,
	usedTools, successfulTools map[string]bool,
) types.AgentLoopState {
	return types.AgentLoopState{
		LastToolSignature: lastToolSignature,
		SameToolCalls:     sameToolCalls,
		NoProgressRounds:  noProgressRounds,
		SameModelTexts:    sameModelTexts,
		UsedTools:         sortedBoolKeys(usedTools),
		SuccessfulTools:   sortedBoolKeys(successfulTools),
	}
}

func sortedBoolKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if value {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func toolCallSignature(calls []types.ToolCall) string {
	type stableToolCall struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments,omitempty"`
	}
	stable := make([]stableToolCall, 0, len(calls))
	for _, call := range calls {
		stable = append(stable, stableToolCall{Name: call.Name, Arguments: call.Arguments})
	}
	return hashJSON(stable)
}

func stuckReason(loop types.LoopConfig, sameToolCalls, noProgressRounds, sameModelTexts int) string {
	if loop.MaxSameToolCalls > 0 && sameToolCalls >= loop.MaxSameToolCalls {
		return fmt.Sprintf("same Tool call repeated %d times", sameToolCalls)
	}
	if loop.MaxNoProgressRounds > 0 && noProgressRounds >= loop.MaxNoProgressRounds {
		return fmt.Sprintf("no measurable progress for %d rounds", noProgressRounds)
	}
	if loop.MaxSameModelTexts > 0 && sameModelTexts >= loop.MaxSameModelTexts {
		return fmt.Sprintf("same model response repeated %d times", sameModelTexts)
	}
	return ""
}

func completionFeedback(
	config types.CompletionConfig,
	structured *types.StructuredOutput,
	parsed any,
	usedTools map[string]bool,
	successfulTools map[string]bool,
	toolCalls int,
	workspaceOps []types.WorkspaceOperation,
) string {
	var missing []string
	if config.RequireTool && toolCalls == 0 {
		missing = append(missing, "at least one Tool call is required")
	}
	for _, name := range config.RequiredTools {
		if !usedTools[name] {
			missing = append(missing, fmt.Sprintf("Tool %q must be called", name))
		}
		if config.RequireToolSuccess && !successfulTools[name] {
			missing = append(missing, fmt.Sprintf("Tool %q must succeed", name))
		}
	}
	if config.RequireWorkspaceRead && !hasWorkspaceOperation(workspaceOps, "read") {
		missing = append(missing, "a Workspace read is required")
	}
	if config.RequireWorkspaceChange && !hasWorkspaceOperation(workspaceOps, "write") {
		missing = append(missing, "a Workspace write is required")
	}
	if config.RequireStructuredOutput && (structured == nil || parsed == nil) {
		missing = append(missing, "structured output is required")
	}
	return strings.Join(missing, "; ")
}

func hasWorkspaceOperation(operations []types.WorkspaceOperation, kind string) bool {
	for _, operation := range operations {
		if operation.Kind == kind {
			return true
		}
	}
	return false
}

func routeFromAction(action types.NextAction) *types.Route {
	if action == "" {
		action = types.NextProceed
	}
	return &types.Route{Action: action}
}

func routeFromParsed(parsed any) *types.Route {
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil
	}
	value, ok := object["next"]
	if !ok {
		value = object
	}
	next, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	action, _ := next["action"].(string)
	if action == "" {
		return nil
	}
	route := &types.Route{
		Action:     normalizeRouteAction(action),
		Reason:     stringFromMap(next, "reason"),
		CallerTeam: stringFromMap(next, "caller_team"),
	}
	if rawTeams, ok := next["teams"].([]any); ok {
		for _, raw := range rawTeams {
			if team, ok := raw.(string); ok {
				route.Teams = append(route.Teams, team)
			}
		}
	}
	return route
}

func normalizeStructuredRoute(structured *types.StructuredOutput, route *types.Route) *types.Route {
	if structured == nil || route == nil {
		return route
	}
	if route.Action == types.NextAction("complete") ||
		route.Action == types.NextAction("wait_input") {
		return &types.Route{Action: types.NextProceed, Reason: "structured Agent completed"}
	}
	return route
}

// normalizeRouteAction preserves compatibility with older Agent prompts while
// keeping lifecycle terms out of Flow/Team orchestration.
func normalizeRouteAction(action string) types.NextAction {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "complete", "wait_input":
		return types.NextProceed
	default:
		return types.NextAction(action)
	}
}

func stringFromParsed(parsed any, key string) string {
	object, ok := parsed.(map[string]any)
	if !ok {
		return ""
	}
	return stringFromMap(object, key)
}

func stringFromMap(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func (t *TurnLoop) buildToolSchemas(agent types.AgentConfig) []types.JSONSchema {
	var schemas []types.JSONSchema

	builtinSchemas := map[string]types.JSONSchema{
		"Read": {
			Name: "Read", Type: "object",
			Properties: map[string]types.JSONProperty{
				"file":       {Type: "string", Description: "Path to the file to read"},
				"line_start": {Type: "integer", Description: "1-based first line to read"},
				"line_end":   {Type: "integer", Description: "1-based last line to read"},
				"max_bytes":  {Type: "integer", Description: "Maximum bytes to return"},
			},
			Required: []string{"file"},
		},
		"Write": {
			Name: "Write", Type: "object",
			Properties: map[string]types.JSONProperty{
				"file":          {Type: "string", Description: "Path to the file to write"},
				"content":       {Type: "string", Description: "Full content for replace mode"},
				"mode":          {Type: "string", Description: "create, replace, or edit", Enum: []string{"create", "replace", "edit"}},
				"base_revision": {Type: "string", Description: "Revision returned by Read"},
				"old_text":      {Type: "string", Description: "Exact text to replace in edit mode"},
				"new_text":      {Type: "string", Description: "Replacement text in edit mode"},
			},
			Required: []string{"file"},
		},
		"Bash": {
			Name: "Bash", Type: "object",
			Properties: map[string]types.JSONProperty{
				"command":          {Type: "string", Description: "Shell command to execute"},
				"stdin":            {Type: "string", Description: "Optional standard input"},
				"timeout_ms":       {Type: "integer", Description: "Optional timeout in milliseconds"},
				"max_output_bytes": {Type: "integer", Description: "Maximum stdout/stderr bytes returned"},
			},
			Required: []string{"command"},
		},
		"WebSearch": {
			Name: "WebSearch", Type: "object",
			Properties: map[string]types.JSONProperty{
				"query":        {Type: "string", Description: "Search query"},
				"domains":      {Type: "array", Description: "Optional domains to restrict the search to"},
				"max_results":  {Type: "integer", Description: "Maximum number of results"},
				"recency_days": {Type: "integer", Description: "Optional recency window in days"},
			},
			Required: []string{"query"},
		},
		"WebFetch": {
			Name: "WebFetch", Type: "object",
			Properties: map[string]types.JSONProperty{
				"url":       {Type: "string", Description: "HTTP or HTTPS URL"},
				"max_bytes": {Type: "integer", Description: "Maximum response bytes"},
			},
			Required: []string{"url"},
		},
		"CodeNav": {
			Name: "CodeNav", Type: "object",
			Properties: map[string]types.JSONProperty{
				"operation": {Type: "string", Description: "definition, references, symbols, hover, or diagnostics", Enum: []string{"definition", "references", "symbols", "hover", "diagnostics"}},
				"file":      {Type: "string", Description: "Workspace-relative source file"},
				"line":      {Type: "integer", Description: "1-based line"},
				"column":    {Type: "integer", Description: "1-based column"},
				"symbol":    {Type: "string", Description: "Optional symbol name"},
			},
			Required: []string{"operation"},
		},
		"AskUserQuestion": {
			Name: "AskUserQuestion", Type: "object",
			Properties: map[string]types.JSONProperty{
				"question":     {Type: "string", Description: "Question to show the user"},
				"options":      {Type: "array", Description: "Optional answer choices"},
				"header":       {Type: "string", Description: "Optional short label"},
				"multi_select": {Type: "boolean", Description: "Whether multiple choices may be selected"},
			},
			Required: []string{"question"},
		},
		"Grep": {
			Name: "Grep", Type: "object",
			Properties: map[string]types.JSONProperty{
				"pattern":     {Type: "string", Description: "Text or regular expression to search for"},
				"path":        {Type: "string", Description: "File or directory to search in"},
				"include":     {Type: "string", Description: "Optional filename pattern such as *.go"},
				"regex":       {Type: "boolean", Description: "Interpret pattern as a regular expression"},
				"ignore_case": {Type: "boolean", Description: "Ignore case when matching"},
				"max_results": {Type: "integer", Description: "Maximum number of matching lines"},
				"max_chars":   {Type: "integer", Description: "Maximum result characters"},
			},
			Required: []string{"pattern"},
		},
		"Glob": {
			Name: "Glob", Type: "object",
			Properties: map[string]types.JSONProperty{
				"pattern":      {Type: "string", Description: "Glob pattern (e.g., **/*.go)"},
				"max_results":  {Type: "integer", Description: "Maximum number of paths"},
				"include_dirs": {Type: "boolean", Description: "Include directories"},
			},
			Required: []string{"pattern"},
		},
		"TodoWrite": {Name: "TodoWrite", Type: "object", Properties: map[string]types.JSONProperty{"items": {Type: "array", Description: "List of todo items"}}},
		"TodoRead":  {Name: "TodoRead", Type: "object", Properties: map[string]types.JSONProperty{}},
	}

	toolNames := append([]string(nil), agent.Tools.Builtin...)
	sort.Strings(toolNames)
	seen := make(map[string]struct{}, len(toolNames))
	for _, toolName := range toolNames {
		if _, ok := seen[toolName]; ok {
			continue
		}
		seen[toolName] = struct{}{}
		if schema, ok := builtinSchemas[toolName]; ok {
			schemas = append(schemas, schema)
		}
	}
	return schemas
}
