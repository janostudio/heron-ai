package team

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/internal/knowledge"
	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/internal/runtime/call"
	"github.com/heron-ai/heron-engine/internal/skill"
	"github.com/heron-ai/heron-engine/internal/state"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Runtime executes the calls of one TeamTurn. It deliberately keeps the
// first implementation small: dependencies are all-of, calls without
// dependencies run in parallel, and Team output is promoted explicitly.
type Runtime struct {
	executors  *call.Registry
	agents     map[string]types.AgentConfig
	states     *state.Store
	knowledge  *knowledge.KnowledgeInjector
	skills     *skill.SkillInjector
	rules      map[string]types.RuleItem
	sessions   storage.SessionWriter
	entityLock *agentstore.EntityLocks
}

func NewRuntime(executors *call.Registry, agentDefinitions ...map[string]types.AgentConfig) *Runtime {
	agents := make(map[string]types.AgentConfig)
	if len(agentDefinitions) > 0 && agentDefinitions[0] != nil {
		agents = agentDefinitions[0]
	}
	return &Runtime{executors: executors, agents: agents}
}

// SetStateStore wires the optional Team/Agent state extension without
// making state a hard dependency of the core Team scheduler.
func (r *Runtime) SetStateStore(store *state.Store) {
	r.states = store
}

// SetKnowledgeInjector wires the optional long-term Knowledge extension.
// Knowledge is queried at the call boundary; the Team scheduler does not
// otherwise depend on its storage format.
func (r *Runtime) SetKnowledgeInjector(injector *knowledge.KnowledgeInjector) {
	r.knowledge = injector
}

// SetSkillInjector wires the optional reusable prompt/tool bundle extension.
// Skills remain an Agent concern; TeamRuntime only resolves them at the
// call boundary.
func (r *Runtime) SetSkillInjector(injector *skill.SkillInjector) {
	r.skills = injector
}

// SetRuleDefinitions wires the global rule definitions. Rules are rendered
// into the Agent prompt without becoming a second orchestration protocol.
func (r *Runtime) SetRuleDefinitions(rules map[string]types.RuleItem) {
	r.rules = rules
}

func (r *Runtime) SetSessionWriter(writer storage.SessionWriter) {
	r.sessions = writer
}

// SetEntityLocks shares one entity lock set with the Spawn tool so inline
// spawned children and synthetic Team calls of the same dynamic entity never
// run concurrently (design 20: one entity is one agent). Each Run falls back
// to a private lock set when none is wired.
func (r *Runtime) SetEntityLocks(locks *agentstore.EntityLocks) {
	if locks != nil {
		r.entityLock = locks
	}
}

func (r *Runtime) Run(ctx context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	result := types.TeamTurnResult{
		Turn:        req.TeamTurn,
		CallResults: make(map[string]types.CallResult),
	}
	if r.executors == nil {
		result.Error = "call executor registry is nil"
		result.Next = &types.Route{Action: types.NextCoordinate, Reason: result.Error}
		return result, fmt.Errorf("%s", result.Error)
	}
	if err := req.Team.Validate(); err != nil {
		result.Error = err.Error()
		result.Next = &types.Route{Action: types.NextFail, Reason: result.Error}
		return result, err
	}

	var teamState types.StateSnapshot
	states := r.states
	if states != nil && req.Team.State.Enabled {
		states = states.ForConfig(req.Team.State)
	}
	if states != nil && req.Team.State.Enabled {
		loaded, err := states.LoadTeam(ctx, req.FlowSession.ID, req.Team.ID)
		if err != nil {
			result.Error = err.Error()
			result.Next = &types.Route{Action: types.NextCoordinate, Reason: err.Error()}
			return result, err
		}
		teamState = loaded
	}

	remaining := make(map[string]types.Call, len(req.Team.Calls))
	for name, configured := range req.Team.Calls {
		remaining[name] = configured
	}

	completed := make(map[string]bool, len(req.Team.Calls))
	var allRecords []types.SharedRecord
	var allReply []string
	teamCalls := 0
	limits := req.Limits.WithDefaults()

	// A resumed TeamTurn may already contain completed sibling Calls. They
	// are inputs to the Team, not work to be executed again.
	for name, callResult := range req.ResumeResults {
		if _, ok := remaining[name]; !ok {
			continue
		}
		result.CallResults[name] = callResult
		delete(remaining, name)
		completed[name] = callResult.Status == types.TurnCompleted
		allRecords = append(allRecords, callResult.Records...)
		if strings.TrimSpace(callResult.Reply) != "" {
			allReply = append(allReply, callResult.Reply)
		}
		addUsage(&result.Usage, callResult.Usage)
	}
	for _, name := range req.ResumeCompletedCalls {
		if _, ok := remaining[name]; !ok {
			continue
		}
		delete(remaining, name)
		completed[name] = true
	}

	// Spawned child insertion (design 21 §4.4): from here the run state owns
	// the scheduling sets, so Spawn(wait=false, deliver=downstream) inside a
	// parent call can append synthetic calls under the run mutex while the
	// main loop is blocked in runBatch.
	state := newRunState(remaining, completed, r.entityLock)
	defer state.close()
	ctx = agentstore.WithChildInserter(ctx, state)

	for state.hasRemaining() {
		if err := contextErr(ctx); err != nil {
			result.Error = err.Error()
			result.Next = &types.Route{Action: types.NextFail, Reason: result.Error}
			return result, err
		}

		ready := state.readyCalls()
		if req.ResumeCallID != "" {
			resumeCall, ok := state.call(req.ResumeCallID)
			if !ok {
				return result, fmt.Errorf("resume call %q not found in Team %q", req.ResumeCallID, req.Team.ID)
			}
			ready = map[string]types.Call{req.ResumeCallID: resumeCall}
		}
		ready = limitCalls(ready, limits.MaxParallelCalls)
		if len(ready) == 0 {
			result.Error = "team call dependency graph cannot make progress"
			result.Next = &types.Route{Action: types.NextFail, Reason: result.Error}
			logging.Error("team call dependency graph cannot make progress", map[string]any{
				"flow_session_id": req.FlowSession.ID,
				"team_id":         req.Team.ID,
				"team_turn_id":    req.TeamTurn.ID,
				"error":           result.Error,
			})
			return result, fmt.Errorf("%s", result.Error)
		}
		if teamCalls+len(ready) > limits.MaxCallsPerTeamTurn {
			result.Error = fmt.Sprintf("team turn exceeded max calls: %d", limits.MaxCallsPerTeamTurn)
			result.Next = &types.Route{Action: types.NextCoordinate, Reason: result.Error}
			return result, fmt.Errorf("%s", result.Error)
		}
		teamCalls += len(ready)

		batchResults, err := r.runBatch(ctx, states, req, state, ready, result.CallResults)
		if err != nil {
			for _, name := range sortedCallResultNames(batchResults) {
				callResult := batchResults[name]
				result.CallResults[name] = callResult
				allRecords = append(allRecords, callResult.Records...)
				if strings.TrimSpace(callResult.Reply) != "" {
					allReply = append(allReply, callResult.Reply)
				}
				addUsage(&result.Usage, callResult.Usage)
				if callResult.Status == types.TurnWaitingApproval && callResult.PendingApproval != nil {
					result.PendingApprovals = append(result.PendingApprovals, *callResult.PendingApproval)
				}
			}
			result.Error = err.Error()
			result.Next = &types.Route{Action: types.NextCoordinate, Reason: result.Error}
			result.Turn.Status = types.TurnFailed
			logging.Error("team batch failed", map[string]any{
				"flow_session_id": req.FlowSession.ID,
				"team_id":         req.Team.ID,
				"team_turn_id":    req.TeamTurn.ID,
				"error":           err.Error(),
			})
			return result, err
		}

		hasWaitingInput := false
		hasWaitingTool := false
		hasWaitingApproval := false
		var waitingInput *types.CallResult
		for _, name := range sortedCallResultNames(batchResults) {
			callResult := batchResults[name]
			result.CallResults[name] = callResult
			if callResult.Status == types.TurnCompleted {
				state.complete(name)
			}
			allRecords = append(allRecords, callResult.Records...)
			if strings.TrimSpace(callResult.Reply) != "" {
				allReply = append(allReply, callResult.Reply)
			}
			addUsage(&result.Usage, callResult.Usage)

			if callResult.Status == types.TurnWaitingInput {
				hasWaitingInput = true
				if waitingInput == nil {
					copy := callResult
					waitingInput = &copy
				}
				continue
			}
			if callResult.Status == types.TurnWaitingTool {
				hasWaitingTool = true
				result.PendingToolTasks = append(result.PendingToolTasks,
					pendingToolTaskForCall(req, name, callResult))
				continue
			}
			if callResult.Status == types.TurnWaitingApproval {
				hasWaitingApproval = true
				if callResult.PendingApproval != nil {
					result.PendingApprovals = append(result.PendingApprovals, *callResult.PendingApproval)
				}
				continue
			}
			if callResult.Status != types.TurnCompleted {
				result.Error = fmt.Sprintf("call %q failed: %s", name, callResult.Error)
				result.Next = &types.Route{Action: types.NextCoordinate, Reason: result.Error}
				result.Turn.Status = types.TurnFailed
				logging.Error("team call failed", map[string]any{
					"flow_session_id": req.FlowSession.ID,
					"team_id":         req.Team.ID,
					"team_turn_id":    req.TeamTurn.ID,
					"call_id":         name,
					"error":           result.Error,
				})
				return result, fmt.Errorf("%s", result.Error)
			}
		}
		if hasWaitingTool || hasWaitingInput || hasWaitingApproval {
			// The whole ready batch is now represented by this TeamTurn.
			// Keep all pending calls together so Flow.Resume can wait for and
			// resume them as one aggregate rather than selecting one event.
			switch {
			case hasWaitingApproval:
				result.Turn.Status = types.TurnWaitingApproval
				result.Next = &types.Route{Action: types.NextWaitApproval, Reason: "one or more Agent Tool calls require approval"}
			case hasWaitingTool:
				result.Turn.Status = types.TurnWaitingTool
				result.Next = &types.Route{Action: types.NextWaitTool, Reason: "one or more Agent Tool tasks are still running"}
			case waitingInput != nil:
				result.Turn.Status = types.TurnWaitingInput
				result.Next = waitingInput.Next
			}
			result.Reply = strings.Join(allReply, "\n\n")
			return result, nil
		}
	}

	result.Records = selectTeamRecords(req.Team, result.CallResults, allRecords, state)
	result.Reply = strings.Join(allReply, "\n\n")
	result.Next = resolveNext(req.Team, result.CallResults)
	result.Turn.Status = types.TurnCompleted
	result.Turn.RecordIDs = recordIDs(result.Records)
	if states != nil && req.Team.State.Enabled {
		teamState.RecordIDs = append(teamState.RecordIDs, result.Turn.RecordIDs...)
		if reply := strings.TrimSpace(result.Reply); reply != "" {
			teamState.NextSteps = append(teamState.NextSteps, reply)
		}
		if err := states.SaveTeam(ctx, teamState); err != nil {
			return result, err
		}
	}
	return result, nil
}

func sortedCallResultNames(results map[string]types.CallResult) []string {
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Runtime) runBatch(
	ctx context.Context,
	states *state.Store,
	req types.TeamTurnRequest,
	state *runState,
	ready map[string]types.Call,
	previous map[string]types.CallResult,
) (map[string]types.CallResult, error) {
	results := make(map[string]types.CallResult, len(ready))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for name, configured := range ready {
		wg.Add(1)
		go func(name string, configured types.Call) {
			defer wg.Done()

			spec, isSpawned := state.specOf(configured.ID)
			callReq := types.CallRequest{
				FlowSession:        req.FlowSession,
				FlowTurn:           req.FlowTurn,
				TeamSession:        req.TeamSession,
				TeamTurn:           req.TeamTurn,
				Call:               configured,
				Input:              buildCallInput(req.Input, req.Records, previous, configured),
				ContextBlocks:      append([]types.ContextBlock(nil), req.ContextBlocks...),
				Records:            selectCallRecords(req.Records, previous, configured, state),
				CallTurnID:         fmt.Sprintf("%s:%s", req.TeamTurn.ID, configured.ID),
				AgentTurnID:        fmt.Sprintf("%s:%s", req.TeamTurn.ID, configured.ID),
				Attempt:            req.TeamTurn.Attempt,
				RecoveryOf:         req.TeamTurn.RecoveryOf,
				ResumeCheckpointID: req.ResumeCheckpointID,
				ResumeTaskID:       req.ResumeTaskID,
				WorkspaceRoot:      req.WorkspaceRoot,
				Limits:             req.Limits,
			}
			if isSpawned {
				// "## Your Item" (design 21 §4.4): a synthetic call receives
				// its spawn item as the fanout_item block, exactly like an
				// inline spawned child.
				itemJSON, marshalErr := json.Marshal(spec.Item)
				if marshalErr != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("call %q: encode spawn item: %w", name, marshalErr)
					}
					results[name] = types.CallResult{Status: types.TurnFailed, Error: marshalErr.Error()}
					mu.Unlock()
					return
				}
				callReq.ContextBlocks = append([]types.ContextBlock{{
					Kind: "fanout_item", Text: string(itemJSON), Source: "spawn",
					Stability: "dynamic", Priority: 85,
				}}, callReq.ContextBlocks...)
			}
			if callReq.Input != "" && !hasContextBlock(callReq.ContextBlocks, "input") {
				callReq.ContextBlocks = append(callReq.ContextBlocks, types.ContextBlock{
					Kind: "input", Text: callReq.Input, Source: "user",
					Stability: "dynamic", Priority: 80, Compressible: true,
				})
			}
			resume := req.ResumeCalls[name]
			if resume.CallTurnID == "" && req.ResumeCallID == name {
				resume.CallTurnID = req.ResumeCallTurnID
				resume.CheckpointID = req.ResumeCheckpointID
				resume.TaskID = req.ResumeTaskID
				resume.ApprovalID = req.ResumeApprovalID
				resume.Approval = req.ResumeApproval
			}
			if resume.CallTurnID != "" {
				callReq.CallTurnID = resume.CallTurnID
				callReq.AgentTurnID = resume.CallTurnID
			}
			if req.ResumeCallID == name && req.ResumeInput != "" {
				callReq.Input = req.ResumeInput
			}
			callReq.ResumeCheckpointID = resume.CheckpointID
			callReq.ResumeTaskID = resume.TaskID
			callReq.ResumeApprovalID = resume.ApprovalID
			callReq.ResumeApproval = resume.Approval
			if states != nil && req.Team.State.Enabled {
				teamSnapshot, stateErr := states.LoadTeam(ctx, req.FlowSession.ID, req.Team.ID)
				if stateErr != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = stateErr
					}
					results[name] = types.CallResult{Status: types.TurnFailed, Error: stateErr.Error()}
					mu.Unlock()
					return
				}
				callReq.ContextBlocks = append(callReq.ContextBlocks, types.ContextBlock{
					Kind: "team_state", Text: renderState(teamSnapshot),
					Source: "team_state", Stability: "dynamic", Priority: 70,
					Compressible: true,
				})
			}
			if configured.Type == types.CallAgent {
				agent, ok := r.agents[configured.AgentID]
				if !ok {
					callResult := types.CallResult{
						Status: types.TurnFailed,
						Error:  fmt.Sprintf("agent definition %q not found", configured.AgentID),
					}
					mu.Lock()
					results[name] = callResult
					if firstErr == nil {
						firstErr = fmt.Errorf("call %q: %s", name, callResult.Error)
					}
					mu.Unlock()
					return
				}
				if r.knowledge != nil {
					query := strings.TrimSpace(configured.Responsibility + "\n" + req.Input)
					if query != "" {
						knowledgeText, knowledgeErr := r.knowledge.InjectWithAllowlist(
							ctx,
							query,
							configured.AgentID,
							req.Team.ID,
							agent.Knowledge,
						)
						if knowledgeErr != nil {
							mu.Lock()
							if firstErr == nil {
								firstErr = knowledgeErr
							}
							results[name] = types.CallResult{Status: types.TurnFailed, Error: knowledgeErr.Error()}
							mu.Unlock()
							return
						}
						callReq.ContextBlocks = append(callReq.ContextBlocks, types.ContextBlock{
							Kind: "knowledge", Text: knowledgeText,
							Source: "knowledge", Stability: "semi_stable", Priority: 90,
							Compressible: true,
						})
					}
				}
				if r.skills != nil {
					prompts, tools := r.skills.Inject(agent.Skills)
					if text := strings.TrimSpace(strings.Join(prompts, "\n\n")); text != "" {
						callReq.ContextBlocks = append(callReq.ContextBlocks, types.ContextBlock{
							Kind: "skills", Text: text, Source: "skill",
							Placement: "system", Stability: "stable", Priority: 80,
						})
					}
					agent.Tools.Builtin = appendUnique(agent.Tools.Builtin, tools...)
				}
				if text := renderRules(r.rules, agent.Rules, configured.AgentID, req.Team.ID); text != "" {
					callReq.ContextBlocks = append(callReq.ContextBlocks, types.ContextBlock{
						Kind: "rules", Text: text, Source: "rule",
						Placement: "system", Stability: "stable", Priority: 90,
					})
				}
				callReq.AgentDefinition = &agent
				if isSpawned {
					// Entity state routing (design 20 §5): a synthetic call reads
					// its entity's persistent state, not the session-scoped
					// per-call state — the same scope inline spawned children
					// use.
					if states != nil {
						snapshot, stateErr := states.LoadEntity(ctx, spec.AgentID, spec.Key)
						if stateErr != nil {
							mu.Lock()
							if firstErr == nil {
								firstErr = stateErr
							}
							results[name] = types.CallResult{Status: types.TurnFailed, Error: stateErr.Error()}
							mu.Unlock()
							return
						}
						if text := renderState(snapshot); text != "" {
							callReq.ContextBlocks = append(callReq.ContextBlocks, types.ContextBlock{
								Kind: "entity_state", Text: text, Source: "entity_state",
								Stability: "dynamic", Priority: 60, Compressible: true,
							})
						}
					}
				} else if states != nil && req.Team.State.Enabled {
					snapshot, stateErr := states.LoadAgent(ctx, req.FlowSession.ID, req.Team.ID, configured.ID)
					if stateErr != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = stateErr
						}
						results[name] = types.CallResult{Status: types.TurnFailed, Error: stateErr.Error()}
						mu.Unlock()
						return
					}
					callReq.ContextBlocks = append(callReq.ContextBlocks, types.ContextBlock{
						Kind: "agent_state", Text: renderState(snapshot),
						Source: "agent_state", Stability: "dynamic", Priority: 60,
						Compressible: true,
					})
				}
			}
			callReq.ContextBlocks = buildContextBlocks(callReq)

			// Depth and entity serialization (runChild parity): the synthetic
			// call's own Spawn calls must see the spawn depth, and one entity
			// never runs two concurrent turns. The TryLock happens before the
			// started event so a busy entity fails without emitting events,
			// matching the other pre-execution failure paths.
			execCtx := ctx
			unlockEntity := func() {}
			if isSpawned {
				execCtx = agentstore.WithSpawnDepth(ctx, spec.Depth)
				var acquired bool
				unlockEntity, acquired = state.tryLockEntity(spec.AgentID, spec.Key)
				if !acquired {
					busyErr := fmt.Errorf("entity %q is already executing", spec.Key)
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("call %q: %w", name, busyErr)
					}
					results[name] = types.CallResult{Status: types.TurnFailed, Error: busyErr.Error()}
					mu.Unlock()
					return
				}
			}
			if err := r.appendCallStarted(ctx, callReq, spec); err != nil {
				unlockEntity()
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				results[name] = types.CallResult{Status: types.TurnFailed, Error: err.Error()}
				mu.Unlock()
				return
			}
			callResult, err := r.executors.Execute(execCtx, callReq)
			unlockEntity()
			if err != nil {
				logging.Error("call executor failed", map[string]any{
					"flow_session_id": req.FlowSession.ID,
					"team_id":         req.Team.ID,
					"team_turn_id":    req.TeamTurn.ID,
					"call_id":         name,
					"call_type":       string(configured.Type),
					"error":           err.Error(),
				})
			}
			if callReq.ResumeApproval != nil {
				decision := *callReq.ResumeApproval
				callResult.Approval = &decision
			}
			if err == nil && callResult.Status == types.TurnCompleted && states != nil {
				if isSpawned {
					// Entity state routing: persist to the entity scope with
					// the same deterministic update inline children apply.
					if stateErr := saveEntityState(ctx, states, *spec, contextBlockText(callReq.ContextBlocks, "entity_state"), callResult); stateErr != nil {
						err = stateErr
						callResult.Status = types.TurnFailed
						callResult.Error = stateErr.Error()
					}
				} else if req.Team.State.Enabled {
					if stateErr := r.saveAgentState(ctx, states, req, configured, contextBlockText(callReq.ContextBlocks, "agent_state"), callResult); stateErr != nil {
						err = stateErr
						callResult.Status = types.TurnFailed
						callResult.Error = stateErr.Error()
					}
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = fmt.Errorf("call %q: %w", name, err)
			}
			results[name] = callResult
			if callResult.Status == types.TurnWaitingInput ||
				callResult.Status == types.TurnWaitingTool ||
				callResult.Status == types.TurnWaitingApproval {
				if eventErr := r.appendCallWaiting(ctx, callReq, callResult, spec); eventErr != nil && firstErr == nil {
					firstErr = eventErr
				}
				return
			}
			if eventErr := r.appendCallCompleted(ctx, callReq, callResult, spec); eventErr != nil && firstErr == nil {
				firstErr = eventErr
			}
		}(name, configured)
	}
	wg.Wait()
	if firstErr != nil {
		return results, firstErr
	}
	return results, nil
}

func addUsage(total *types.TokenUsage, usage types.TokenUsage) {
	if total == nil {
		return
	}
	total.PromptTokens += usage.PromptTokens
	total.CompletionTokens += usage.CompletionTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.TotalTokens += usage.TotalTokens
	total.PromptCacheHitTokens += usage.PromptCacheHitTokens
	total.PromptCacheMissTokens += usage.PromptCacheMissTokens
	total.CacheReadInputTokens += usage.CacheReadInputTokens
	total.CacheCreationInputTokens += usage.CacheCreationInputTokens
}

func pendingToolTaskForCall(req types.TeamTurnRequest, callID string, result types.CallResult) types.PendingToolTask {
	pending := types.PendingToolTask{
		CallID:       callID,
		CallTurnID:   result.CallTurnID,
		AgentTurnID:  result.CallTurnID,
		AgentID:      result.AgentID,
		TaskID:       result.TaskID,
		CheckpointID: result.CheckpointID,
		Status:       result.Status,
	}
	if result.Checkpoint != nil {
		if pending.CallTurnID == "" {
			pending.CallTurnID = result.Checkpoint.AgentTurnID
		}
		if pending.AgentTurnID == "" {
			pending.AgentTurnID = result.Checkpoint.AgentTurnID
		}
		pending.AgentID = result.Checkpoint.AgentID
	}
	if pending.CallTurnID == "" {
		pending.CallTurnID = fmt.Sprintf("%s:%s", req.TeamTurn.ID, callID)
	}
	if pending.AgentTurnID == "" {
		pending.AgentTurnID = pending.CallTurnID
	}
	return pending
}

// spawnEventPayload decorates one session event payload with the spawn
// identity of a synthetic call, mirroring the payload.spawn block the Spawn
// tool emits for inline children so event consumers see one shape.
func spawnEventPayload(payload map[string]any, spawn *agentstore.SpawnedCallSpec) map[string]any {
	if spawn == nil {
		return payload
	}
	payload["spawn"] = map[string]any{
		"agent":          spawn.AgentID,
		"key":            spawn.Key,
		"parent_call_id": spawn.ParentCallID,
	}
	return payload
}

func (r *Runtime) appendCallStarted(ctx context.Context, req types.CallRequest, spawn *agentstore.SpawnedCallSpec) error {
	if r.sessions == nil {
		return nil
	}
	eventType := types.EventCommandTurnStarted
	switch req.Call.Type {
	case types.CallAgent:
		eventType = types.EventAgentTurnStarted
	case types.CallWebhook:
		eventType = types.EventWebhookTurnStarted
	}
	if req.Call.Type == types.CallAgent {
		agentSession := types.AgentSession{
			ID:            fmt.Sprintf("%s:%s", req.TeamSession.ID, req.Call.ID),
			TeamSessionID: req.TeamSession.ID,
			CallID:        req.Call.ID,
			AgentID:       req.Call.AgentID,
			Status:        types.SessionRunning,
			CreatedAt:     req.TeamSession.CreatedAt,
			UpdatedAt:     time.Now().UTC(),
		}
		if _, err := r.sessions.Append(ctx, req.FlowSession.ID, storage.LayerTeam, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventAgentSessionCreated,
				FlowSessionID: req.FlowSession.ID,
				FlowTurnID:    req.FlowTurn.ID,
				TeamSessionID: req.TeamSession.ID,
				TeamTurnID:    req.TeamTurn.ID,
				TeamID:        req.TeamTurn.TeamID,
				CallID:        req.Call.ID,
				CallTurnID:    req.CallTurnID,
				CallType:      req.Call.Type,
				Attempt:       req.Attempt,
				RecoveryOf:    req.RecoveryOf,
			},
			Payload: map[string]any{"agent_session": agentSession},
		}); err != nil {
			return err
		}
	}
	_, err := r.sessions.Append(ctx, req.FlowSession.ID, storage.LayerTeam, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          eventType,
			FlowSessionID: req.FlowSession.ID,
			FlowTurnID:    req.FlowTurn.ID,
			TeamSessionID: req.TeamSession.ID,
			TeamTurnID:    req.TeamTurn.ID,
			TeamID:        req.TeamTurn.TeamID,
			CallID:        req.Call.ID,
			CallTurnID:    req.CallTurnID,
			CallType:      req.Call.Type,
			Attempt:       req.Attempt,
			RecoveryOf:    req.RecoveryOf,
		},
		Payload: spawnEventPayload(map[string]any{
			"call":  req.Call,
			"input": req.Input,
		}, spawn),
	})
	return err
}

func (r *Runtime) appendCallCompleted(ctx context.Context, req types.CallRequest, result types.CallResult, spawn *agentstore.SpawnedCallSpec) error {
	if r.sessions == nil {
		return nil
	}
	eventType := types.EventCommandTurnCompleted
	switch req.Call.Type {
	case types.CallAgent:
		eventType = types.EventAgentTurnCompleted
	case types.CallWebhook:
		eventType = types.EventWebhookTurnCompleted
	}
	if _, err := r.sessions.Append(ctx, req.FlowSession.ID, storage.LayerTeam, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          eventType,
			FlowSessionID: req.FlowSession.ID,
			FlowTurnID:    req.FlowTurn.ID,
			TeamSessionID: req.TeamSession.ID,
			TeamTurnID:    req.TeamTurn.ID,
			TeamID:        req.TeamTurn.TeamID,
			CallID:        req.Call.ID,
			CallTurnID:    req.CallTurnID,
			CallType:      req.Call.Type,
		},
		Payload: spawnEventPayload(map[string]any{"call_result": result}, spawn),
	}); err != nil {
		return err
	}
	if result.Approval != nil {
		_, err := r.sessions.Append(ctx, req.FlowSession.ID, storage.LayerTeam, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventApprovalResolved,
				FlowSessionID: req.FlowSession.ID,
				FlowTurnID:    req.FlowTurn.ID,
				TeamSessionID: req.TeamSession.ID,
				TeamTurnID:    req.TeamTurn.ID,
				TeamID:        req.TeamTurn.TeamID,
				CallID:        req.Call.ID,
				CallTurnID:    req.CallTurnID,
				CallType:      req.Call.Type,
				Attempt:       req.Attempt,
				RecoveryOf:    req.RecoveryOf,
			},
			Payload: map[string]any{"approval": result.Approval},
		})
		if err != nil {
			return err
		}
	}
	return r.appendAgentSessionUpdated(ctx, req, result.Status)
}

func (r *Runtime) appendCallWaiting(ctx context.Context, req types.CallRequest, result types.CallResult, spawn *agentstore.SpawnedCallSpec) error {
	if r.sessions == nil {
		return nil
	}
	if req.Call.Type != types.CallAgent {
		return r.appendCallCompleted(ctx, req, result, spawn)
	}
	eventType := types.EventAgentTurnWaitingInput
	switch result.Status {
	case types.TurnWaitingTool:
		eventType = types.EventAgentTurnWaitingTool
	case types.TurnWaitingApproval:
		eventType = types.EventAgentTurnWaitingApproval
	}
	if _, err := r.sessions.Append(ctx, req.FlowSession.ID, storage.LayerTeam, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          eventType,
			FlowSessionID: req.FlowSession.ID,
			FlowTurnID:    req.FlowTurn.ID,
			TeamSessionID: req.TeamSession.ID,
			TeamTurnID:    req.TeamTurn.ID,
			TeamID:        req.TeamTurn.TeamID,
			CallID:        req.Call.ID,
			CallTurnID:    req.CallTurnID,
			CallType:      req.Call.Type,
			Attempt:       req.Attempt,
			RecoveryOf:    req.RecoveryOf,
		},
		Payload: spawnEventPayload(map[string]any{
			"call_result":   result,
			"checkpoint_id": result.CheckpointID,
			"task_id":       result.TaskID,
		}, spawn),
	}); err != nil {
		return err
	}
	if result.PendingApproval != nil {
		_, err := r.sessions.Append(ctx, req.FlowSession.ID, storage.LayerTeam, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventApprovalRequested,
				FlowSessionID: req.FlowSession.ID,
				FlowTurnID:    req.FlowTurn.ID,
				TeamSessionID: req.TeamSession.ID,
				TeamTurnID:    req.TeamTurn.ID,
				TeamID:        req.TeamTurn.TeamID,
				CallID:        req.Call.ID,
				CallTurnID:    req.CallTurnID,
				CallType:      req.Call.Type,
				Attempt:       req.Attempt,
				RecoveryOf:    req.RecoveryOf,
			},
			Payload: map[string]any{"approval": result.PendingApproval},
		})
		if err != nil {
			return err
		}
	}
	return r.appendAgentSessionUpdated(ctx, req, result.Status)
}

func (r *Runtime) appendAgentSessionUpdated(
	ctx context.Context,
	req types.CallRequest,
	status types.TurnStatus,
) error {
	if r.sessions == nil || req.Call.Type != types.CallAgent {
		return nil
	}
	agentStatus := sessionStatusForCall(status)
	agentSession := types.AgentSession{
		ID:            fmt.Sprintf("%s:%s", req.TeamSession.ID, req.Call.ID),
		TeamSessionID: req.TeamSession.ID,
		CallID:        req.Call.ID,
		AgentID:       req.Call.AgentID,
		Status:        agentStatus,
		CreatedAt:     req.TeamSession.CreatedAt,
		UpdatedAt:     time.Now().UTC(),
	}
	_, err := r.sessions.Append(ctx, req.FlowSession.ID, storage.LayerTeam, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventAgentSessionUpdated,
			FlowSessionID: req.FlowSession.ID,
			FlowTurnID:    req.FlowTurn.ID,
			TeamSessionID: req.TeamSession.ID,
			TeamTurnID:    req.TeamTurn.ID,
			TeamID:        req.TeamTurn.TeamID,
			CallID:        req.Call.ID,
			CallTurnID:    req.CallTurnID,
			CallType:      req.Call.Type,
			Attempt:       req.Attempt,
			RecoveryOf:    req.RecoveryOf,
		},
		Payload: map[string]any{"agent_session": agentSession},
	})
	return err
}

func sessionStatusForCall(status types.TurnStatus) types.SessionStatus {
	switch status {
	case types.TurnWaitingInput:
		return types.SessionWaitingInput
	case types.TurnWaitingTool:
		return types.SessionWaitingTool
	case types.TurnWaitingApproval:
		return types.SessionWaitingApproval
	case types.TurnCompleted:
		return types.SessionCompleted
	case types.TurnCancelled:
		return types.SessionCancelled
	case types.TurnFailed:
		return types.SessionFailed
	default:
		return types.SessionRunning
	}
}

func (r *Runtime) saveAgentState(
	ctx context.Context,
	states *state.Store,
	req types.TeamTurnRequest,
	configured types.Call,
	previousText string,
	result types.CallResult,
) error {
	if configured.Type != types.CallAgent || states == nil {
		return nil
	}
	snapshot, err := states.LoadAgent(ctx, req.FlowSession.ID, req.Team.ID, configured.ID)
	if err != nil {
		return err
	}
	if snapshot.Goal == "" {
		snapshot.Goal = configured.Responsibility
	}
	if strings.TrimSpace(previousText) == "" && strings.TrimSpace(result.Reply) != "" {
		snapshot.Confirmed = append(snapshot.Confirmed, result.Reply)
	} else if strings.TrimSpace(result.Reply) != "" {
		snapshot.NextSteps = append(snapshot.NextSteps, result.Reply)
	}
	snapshot.RecordIDs = append(snapshot.RecordIDs, recordIDs(result.Records)...)
	for _, operation := range result.WorkspaceOps {
		if operation.Path == "" {
			continue
		}
		snapshot.Workspace = append(snapshot.Workspace, types.StateWorkspaceRef{
			Path:     operation.Path,
			Revision: operation.Revision,
		})
	}
	return states.SaveAgent(ctx, snapshot)
}

func renderState(snapshot types.StateSnapshot) string {
	var sections []string
	if snapshot.Goal != "" {
		sections = append(sections, "Goal: "+snapshot.Goal)
	}
	appendList := func(title string, values []string) {
		if len(values) == 0 {
			return
		}
		sections = append(sections, title+":\n- "+strings.Join(values, "\n- "))
	}
	appendList("Confirmed", snapshot.Confirmed)
	appendList("Open Questions", snapshot.OpenQuestions)
	appendList("Decisions", snapshot.Decisions)
	appendList("Next Steps", snapshot.NextSteps)
	return strings.Join(sections, "\n\n")
}

func renderRules(definitions map[string]types.RuleItem, names []string, agentID, teamID string) string {
	if len(definitions) == 0 || len(names) == 0 {
		return ""
	}

	var sections []string
	for _, name := range names {
		rule, ok := definitions[name]
		if !ok || !ruleVisible(rule, agentID, teamID) || strings.TrimSpace(rule.Content) == "" {
			continue
		}
		label := rule.ID
		if rule.Type != "" {
			label += " (" + rule.Type + ")"
		}
		sections = append(sections, "### "+label+"\n"+strings.TrimSpace(rule.Content))
	}
	return strings.Join(sections, "\n\n")
}

func ruleVisible(rule types.RuleItem, agentID, teamID string) bool {
	switch rule.Scope.Type {
	case "", "all":
		return true
	case "team":
		return contains(rule.Scope.Teams, teamID)
	case "agents":
		return contains(rule.Scope.Agents, agentID)
	default:
		return false
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func appendUnique(values []string, extra ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(extra))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range extra {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		values = append(values, value)
		seen[value] = struct{}{}
	}
	return values
}

func limitCalls(calls map[string]types.Call, max int) map[string]types.Call {
	if max <= 0 || len(calls) <= max {
		return calls
	}
	limited := make(map[string]types.Call, max)
	for name, configured := range calls {
		if len(limited) >= max {
			break
		}
		limited[name] = configured
	}
	return limited
}

func buildCallInput(
	input string,
	records []types.SharedRecord,
	previous map[string]types.CallResult,
	configured types.Call,
) string {
	// Records are passed separately through CallRequest. Keep this value as
	// raw user input; PromptRenderer is the single owner of Agent section
	// headings and formatting. This prevents "## Input" and
	// "## Shared Records" from being wrapped twice.
	if strings.TrimSpace(input) != "" && shouldReceiveInput(configured.Inputs) {
		return input
	}
	return ""
}

func buildContextBlocks(req types.CallRequest) []types.ContextBlock {
	blocks := make([]types.ContextBlock, 0, len(req.ContextBlocks)+3)
	hasInput := false
	for _, block := range req.ContextBlocks {
		if block.Kind == "input" {
			hasInput = true
			blocks = append(blocks, block)
			continue
		}
		if block.Kind != "responsibility" && block.Kind != "records" {
			blocks = append(blocks, block)
		}
	}
	add := func(kind, text, source, stability string, priority int, compressible bool) {
		if strings.TrimSpace(text) == "" {
			return
		}
		blocks = append(blocks, types.ContextBlock{
			Kind: kind, Text: text, Source: source, Stability: stability,
			Priority: priority, Compressible: compressible,
		})
	}
	add("responsibility", req.Call.Responsibility, "call", "semi_stable", 100, true)
	if !hasInput {
		add("input", req.Input, "user", "dynamic", 80, true)
	}
	if len(req.Records) > 0 {
		data, err := json.Marshal(req.Records)
		if err == nil {
			add("records", string(data), "shared_records", "dynamic", 50, true)
		}
	}
	return blocks
}

func contextBlockText(blocks []types.ContextBlock, kind string) string {
	for _, block := range blocks {
		if block.Kind == kind {
			return block.Text
		}
	}
	return ""
}

func hasContextBlock(blocks []types.ContextBlock, kind string) bool {
	for _, block := range blocks {
		if block.Kind == kind {
			return true
		}
	}
	return false
}

func shouldReceiveInput(inputs types.InputSpec) bool {
	return inputs.UserMessage || inputs.TeamUserMessage
}

// selectCallRecords resolves one call's record bindings. A binding from B
// collects the records of B itself plus every member of B's spawn group
// (design 21 §4.4): synthetic children publish under the parent call's
// output.record name, and RecordID keeps the child call id so multiple
// children stay distinct. With an empty group registry the expansion is
// exactly the legacy single-producer lookup.
func selectCallRecords(
	flowRecords []types.SharedRecord,
	previous map[string]types.CallResult,
	configured types.Call,
	groups *runState,
) []types.SharedRecord {
	inputs := configured.Inputs
	if inputs.FlowRecords == nil &&
		inputs.TeamRecords == nil &&
		inputs.Records == nil {
		return nil
	}

	allowedNames := make(map[string]struct{})
	for _, name := range inputs.FlowRecords {
		allowedNames[name] = struct{}{}
	}
	for _, name := range inputs.TeamRecords {
		allowedNames[name] = struct{}{}
	}

	var selected []types.SharedRecord
	for _, record := range flowRecords {
		if _, ok := allowedNames[record.Name]; ok {
			selected = append(selected, record)
		}
	}
	for _, binding := range inputs.Records {
		if binding.From == "" {
			for _, record := range flowRecords {
				if binding.Record == "" || record.Name == binding.Record {
					selected = append(selected, record)
				}
			}
			continue
		}
		if _, ok := previous[binding.From]; !ok {
			// A binding from a Flow Team (for example `from: research`) is
			// already promoted into req.Records by FlowRuntime. It does not
			// appear in the Team-local previous call map.
			for _, record := range flowRecords {
				if binding.Record == "" || record.Name == binding.Record {
					selected = append(selected, record)
				}
			}
			continue
		}
		for _, producerID := range groups.producersFrom(binding.From) {
			callResult, ok := previous[producerID]
			if !ok {
				continue
			}
			for _, record := range callResult.Records {
				if binding.Record == "" || record.Name == binding.Record {
					selected = append(selected, record)
				}
			}
		}
	}
	return deduplicateRecords(selected)
}

// selectTeamRecords promotes call results into the Team output. Output
// bindings from B aggregate B itself plus every member of B's spawn group
// (design 21 §4.4). With no group members registered, both output forms keep
// their exact legacy single-producer semantics.
func selectTeamRecords(team types.Team, results map[string]types.CallResult, all []types.SharedRecord, groups *runState) []types.SharedRecord {
	output := team.Output
	if output.IsZero() && !team.Outputs.IsZero() {
		output = team.Outputs
	}
	if len(output.Records) == 0 {
		if output.From != "" && output.Record != "" {
			producers := groups.producersFrom(output.From)
			if len(producers) == 1 {
				// No group members: keep the exact current single-record
				// semantics (first matching record of the one producer).
				if callResult, ok := results[output.From]; ok {
					for _, record := range callResult.Records {
						if record.Name == output.Record {
							applyOutputScope(&record, output.Scope)
							return []types.SharedRecord{record}
						}
					}
				}
				return all
			}
			var selected []types.SharedRecord
			for _, producerID := range producers {
				callResult, ok := results[producerID]
				if !ok {
					continue
				}
				for _, record := range callResult.Records {
					if record.Name == output.Record {
						applyOutputScope(&record, output.Scope)
						selected = append(selected, record)
					}
				}
			}
			if len(selected) > 0 {
				return selected
			}
			return all
		}
		return all
	}

	selected := make([]types.SharedRecord, 0, len(output.Records))
	for _, binding := range output.Records {
		for _, producerID := range groups.producersFrom(binding.From) {
			callResult, ok := results[producerID]
			if !ok {
				continue
			}
			for _, record := range callResult.Records {
				if record.Name == binding.Record {
					applyOutputScope(&record, binding.Scope)
					selected = append(selected, record)
				}
			}
		}
	}
	return selected
}

func applyOutputScope(record *types.SharedRecord, scope string) {
	if scope == "flow" {
		record.Scope = types.RecordScopeFlow
	}
}

func resolveNext(team types.Team, results map[string]types.CallResult) *types.Route {
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result := results[name]
		if result.Next != nil && result.Next.Action != types.NextProceed {
			return result.Next
		}
	}
	return &types.Route{Action: types.NextProceed}
}

func deduplicateRecords(records []types.SharedRecord) []types.SharedRecord {
	seen := make(map[string]struct{}, len(records))
	result := make([]types.SharedRecord, 0, len(records))
	for _, record := range records {
		key := record.RecordID
		if key == "" {
			key = record.Name + "\x00" + record.Summary
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, record)
	}
	return result
}

func recordIDs(records []types.SharedRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.RecordID)
	}
	return ids
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
