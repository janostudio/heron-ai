package team

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/internal/knowledge"
	"github.com/heron-ai/heron-engine/internal/memory"
	"github.com/heron-ai/heron-engine/internal/runtime/member"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Runtime executes the members of one TeamTurn. It deliberately keeps the
// first implementation small: dependencies are all-of, members without
// dependencies run in parallel, and Team output is promoted explicitly.
type Runtime struct {
	executors *member.Registry
	agents    map[string]types.AgentConfig
	memories  *memory.Store
	knowledge *knowledge.KnowledgeInjector
	sessions  storage.SessionWriter
}

func NewRuntime(executors *member.Registry, agentDefinitions ...map[string]types.AgentConfig) *Runtime {
	agents := make(map[string]types.AgentConfig)
	if len(agentDefinitions) > 0 && agentDefinitions[0] != nil {
		agents = agentDefinitions[0]
	}
	return &Runtime{executors: executors, agents: agents}
}

// SetMemoryStore wires the optional Team/Subagent memory extension without
// making memory a hard dependency of the core Team scheduler.
func (r *Runtime) SetMemoryStore(store *memory.Store) {
	r.memories = store
}

// SetKnowledgeInjector wires the optional long-term Knowledge extension.
// Knowledge is queried at the member boundary; the Team scheduler does not
// otherwise depend on its storage format.
func (r *Runtime) SetKnowledgeInjector(injector *knowledge.KnowledgeInjector) {
	r.knowledge = injector
}

func (r *Runtime) SetSessionWriter(writer storage.SessionWriter) {
	r.sessions = writer
}

func (r *Runtime) Run(ctx context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	result := types.TeamTurnResult{
		Turn:          req.TeamTurn,
		MemberResults: make(map[string]types.MemberResult),
	}
	if r.executors == nil {
		result.Error = "member executor registry is nil"
		result.Next = &types.Route{Action: types.NextCoordinate, Reason: result.Error}
		return result, fmt.Errorf("%s", result.Error)
	}
	if err := req.Team.Validate(); err != nil {
		result.Error = err.Error()
		result.Next = &types.Route{Action: types.NextFail, Reason: result.Error}
		return result, err
	}

	var teamMemory types.MemorySnapshot
	if r.memories != nil && req.Team.Memory.Enabled {
		loaded, err := r.memories.LoadTeam(ctx, req.FlowSession.ID, req.Team.ID)
		if err != nil {
			result.Error = err.Error()
			result.Next = &types.Route{Action: types.NextCoordinate, Reason: err.Error()}
			return result, err
		}
		teamMemory = loaded
	}

	remaining := make(map[string]types.Member, len(req.Team.Members))
	for name, configured := range req.Team.Members {
		remaining[name] = configured
	}

	completed := make(map[string]bool, len(req.Team.Members))
	var allRecords []types.SharedRecord
	var allReply []string

	for len(remaining) > 0 {
		if err := contextErr(ctx); err != nil {
			result.Error = err.Error()
			result.Next = &types.Route{Action: types.NextFail, Reason: result.Error}
			return result, err
		}

		ready := readyMembers(remaining, completed)
		ready = limitMembers(ready, req.Limits.WithDefaults().MaxParallelMembers)
		if len(ready) == 0 {
			result.Error = "team member dependency graph cannot make progress"
			result.Next = &types.Route{Action: types.NextFail, Reason: result.Error}
			return result, fmt.Errorf("%s", result.Error)
		}

		batchResults, err := r.runBatch(ctx, req, ready, result.MemberResults)
		if err != nil {
			result.Error = err.Error()
			result.Next = &types.Route{Action: types.NextCoordinate, Reason: result.Error}
			return result, err
		}

		for name, memberResult := range batchResults {
			result.MemberResults[name] = memberResult
			delete(remaining, name)
			completed[name] = memberResult.Status == types.TurnCompleted
			allRecords = append(allRecords, memberResult.Records...)
			if strings.TrimSpace(memberResult.Reply) != "" {
				allReply = append(allReply, memberResult.Reply)
			}
			result.Usage.PromptTokens += memberResult.Usage.PromptTokens
			result.Usage.CompletionTokens += memberResult.Usage.CompletionTokens
			result.Usage.TotalTokens += memberResult.Usage.TotalTokens

			if memberResult.Status != types.TurnCompleted {
				result.Error = fmt.Sprintf("member %q failed: %s", name, memberResult.Error)
				result.Next = &types.Route{Action: types.NextCoordinate, Reason: result.Error}
				return result, fmt.Errorf("%s", result.Error)
			}
		}
	}

	result.Records = selectTeamRecords(req.Team, result.MemberResults, allRecords)
	result.Reply = strings.Join(allReply, "\n\n")
	result.Next = resolveNext(req.Team, result.MemberResults)
	result.Turn.Status = types.TurnCompleted
	result.Turn.RecordIDs = recordIDs(result.Records)
	if r.memories != nil && req.Team.Memory.Enabled {
		teamMemory.RecordIDs = append(teamMemory.RecordIDs, result.Turn.RecordIDs...)
		if reply := strings.TrimSpace(result.Reply); reply != "" {
			teamMemory.NextSteps = append(teamMemory.NextSteps, reply)
		}
		if err := r.memories.SaveTeam(ctx, teamMemory); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (r *Runtime) runBatch(
	ctx context.Context,
	req types.TeamTurnRequest,
	ready map[string]types.Member,
	previous map[string]types.MemberResult,
) (map[string]types.MemberResult, error) {
	results := make(map[string]types.MemberResult, len(ready))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for name, configured := range ready {
		wg.Add(1)
		go func(name string, configured types.Member) {
			defer wg.Done()

			memberReq := types.MemberRequest{
				FlowSession:   req.FlowSession,
				FlowTurn:      req.FlowTurn,
				TeamSession:   req.TeamSession,
				TeamTurn:      req.TeamTurn,
				Member:        configured,
				Input:         buildMemberInput(req.Input, req.Records, previous, configured),
				Records:       selectMemberRecords(req.Records, previous, configured),
				MemberTurnID:  fmt.Sprintf("%s:%s", req.TeamTurn.ID, configured.ID),
				WorkspaceRoot: req.WorkspaceRoot,
				Limits:        req.Limits,
			}
			if err := r.appendMemberStarted(ctx, memberReq); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				results[name] = types.MemberResult{Status: types.TurnFailed, Error: err.Error()}
				mu.Unlock()
				return
			}
			if r.memories != nil && req.Team.Memory.Enabled {
				teamSnapshot, memoryErr := r.memories.LoadTeam(ctx, req.FlowSession.ID, req.Team.ID)
				if memoryErr != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = memoryErr
					}
					results[name] = types.MemberResult{Status: types.TurnFailed, Error: memoryErr.Error()}
					mu.Unlock()
					return
				}
				memberReq.TeamMemory = renderMemory(teamSnapshot)
			}
			if r.knowledge != nil && configured.Type == types.MemberSubagent {
				query := strings.TrimSpace(configured.Responsibility + "\n" + req.Input)
				if query != "" {
					knowledgeText, knowledgeErr := r.knowledge.Inject(ctx, query, configured.AgentID, req.Team.ID)
					if knowledgeErr != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = knowledgeErr
						}
						results[name] = types.MemberResult{Status: types.TurnFailed, Error: knowledgeErr.Error()}
						mu.Unlock()
						return
					}
					memberReq.KnowledgeText = knowledgeText
				}
			}
			if configured.Type == types.MemberSubagent {
				agent, ok := r.agents[configured.AgentID]
				if !ok {
					memberResult := types.MemberResult{
						Status: types.TurnFailed,
						Error:  fmt.Sprintf("agent definition %q not found", configured.AgentID),
					}
					mu.Lock()
					results[name] = memberResult
					if firstErr == nil {
						firstErr = fmt.Errorf("member %q: %s", name, memberResult.Error)
					}
					mu.Unlock()
					return
				}
				memberReq.AgentDefinition = &agent
				if r.memories != nil && req.Team.Memory.Enabled {
					snapshot, memoryErr := r.memories.LoadSubagent(ctx, req.FlowSession.ID, req.Team.ID, configured.ID)
					if memoryErr != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = memoryErr
						}
						results[name] = types.MemberResult{Status: types.TurnFailed, Error: memoryErr.Error()}
						mu.Unlock()
						return
					}
					memberReq.SubagentMemory = renderMemory(snapshot)
				}
			}

			memberResult, err := r.executors.Execute(ctx, memberReq)
			if err == nil && memberResult.Status == types.TurnCompleted && r.memories != nil && req.Team.Memory.Enabled {
				if memoryErr := r.saveSubagentMemory(ctx, req, configured, memberReq.SubagentMemory, memberResult); memoryErr != nil {
					err = memoryErr
					memberResult.Status = types.TurnFailed
					memberResult.Error = memoryErr.Error()
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = fmt.Errorf("member %q: %w", name, err)
			}
			results[name] = memberResult
			if eventErr := r.appendMemberCompleted(ctx, memberReq, memberResult); eventErr != nil && firstErr == nil {
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

func (r *Runtime) appendMemberStarted(ctx context.Context, req types.MemberRequest) error {
	if r.sessions == nil {
		return nil
	}
	eventType := types.EventCommandTurnStarted
	if req.Member.Type == types.MemberSubagent {
		eventType = types.EventSubagentTurnStarted
	} else if req.Member.Type == types.MemberWebhook {
		eventType = types.EventWebhookTurnStarted
	}
	if req.Member.Type == types.MemberSubagent {
		subagentSession := types.SubagentSession{
			ID:            fmt.Sprintf("%s:%s", req.TeamSession.ID, req.Member.ID),
			TeamSessionID: req.TeamSession.ID,
			MemberID:      req.Member.ID,
			AgentID:       req.Member.AgentID,
			Status:        types.SessionRunning,
			CreatedAt:     req.TeamSession.CreatedAt,
			UpdatedAt:     time.Now().UTC(),
		}
		if _, err := r.sessions.Append(ctx, req.FlowSession.ID, types.SessionEvent{
			Type:          types.EventSubagentSessionCreated,
			FlowSessionID: req.FlowSession.ID,
			FlowTurnID:    req.FlowTurn.ID,
			TeamSessionID: req.TeamSession.ID,
			TeamTurnID:    req.TeamTurn.ID,
			MemberID:      req.Member.ID,
			MemberTurnID:  req.MemberTurnID,
			MemberType:    req.Member.Type,
			Payload:       map[string]any{"subagent_session": subagentSession},
		}); err != nil {
			return err
		}
	}
	_, err := r.sessions.Append(ctx, req.FlowSession.ID, types.SessionEvent{
		Type:          eventType,
		FlowSessionID: req.FlowSession.ID,
		FlowTurnID:    req.FlowTurn.ID,
		TeamSessionID: req.TeamSession.ID,
		TeamTurnID:    req.TeamTurn.ID,
		MemberID:      req.Member.ID,
		MemberTurnID:  req.MemberTurnID,
		MemberType:    req.Member.Type,
		Payload:       map[string]any{"member": req.Member},
	})
	return err
}

func (r *Runtime) appendMemberCompleted(ctx context.Context, req types.MemberRequest, result types.MemberResult) error {
	if r.sessions == nil {
		return nil
	}
	eventType := types.EventCommandTurnCompleted
	if req.Member.Type == types.MemberSubagent {
		eventType = types.EventSubagentTurnCompleted
	} else if req.Member.Type == types.MemberWebhook {
		eventType = types.EventWebhookTurnCompleted
	}
	_, err := r.sessions.Append(ctx, req.FlowSession.ID, types.SessionEvent{
		Type:          eventType,
		FlowSessionID: req.FlowSession.ID,
		FlowTurnID:    req.FlowTurn.ID,
		TeamSessionID: req.TeamSession.ID,
		TeamTurnID:    req.TeamTurn.ID,
		MemberID:      req.Member.ID,
		MemberTurnID:  req.MemberTurnID,
		MemberType:    req.Member.Type,
		Payload:       map[string]any{"member_result": result},
	})
	return err
}

func (r *Runtime) saveSubagentMemory(
	ctx context.Context,
	req types.TeamTurnRequest,
	configured types.Member,
	previousText string,
	result types.MemberResult,
) error {
	if configured.Type != types.MemberSubagent || r.memories == nil {
		return nil
	}
	snapshot, err := r.memories.LoadSubagent(ctx, req.FlowSession.ID, req.Team.ID, configured.ID)
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
		snapshot.Workspace = append(snapshot.Workspace, types.MemoryWorkspaceRef{
			Path:     operation.Path,
			Revision: operation.Revision,
		})
	}
	return r.memories.SaveSubagent(ctx, snapshot)
}

func renderMemory(snapshot types.MemorySnapshot) string {
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

func readyMembers(remaining map[string]types.Member, completed map[string]bool) map[string]types.Member {
	ready := make(map[string]types.Member)
	for name, configured := range remaining {
		ok := true
		for _, dependency := range configured.DependsOn {
			if !completed[dependency] {
				ok = false
				break
			}
		}
		if ok {
			ready[name] = configured
		}
	}
	return ready
}

func limitMembers(members map[string]types.Member, max int) map[string]types.Member {
	if max <= 0 || len(members) <= max {
		return members
	}
	limited := make(map[string]types.Member, max)
	for name, configured := range members {
		if len(limited) >= max {
			break
		}
		limited[name] = configured
	}
	return limited
}

func buildMemberInput(
	input string,
	records []types.SharedRecord,
	previous map[string]types.MemberResult,
	configured types.Member,
) string {
	var sections []string
	if strings.TrimSpace(input) != "" && shouldReceiveInput(configured.Inputs) {
		sections = append(sections, "## Input\n"+input)
	}
	selected := selectMemberRecords(records, previous, configured)
	if len(selected) > 0 {
		sections = append(sections, "## Shared Records\n"+summarizeRecords(selected))
	}
	return strings.Join(sections, "\n\n")
}

func shouldReceiveInput(inputs types.InputSpec) bool {
	return inputs.UserMessage ||
		inputs.TeamUserMessage ||
		inputs.FlowRecords != nil ||
		inputs.TeamRecords != nil ||
		inputs.Records != nil ||
		inputs.TeamMemory != ""
}

func summarizeRecords(records []types.SharedRecord) string {
	var lines []string
	for _, record := range records {
		lines = append(lines, fmt.Sprintf("- %s (%s): %s", record.Name, record.Kind, record.Summary))
	}
	return strings.Join(lines, "\n")
}

func summarizeMemberResults(results map[string]types.MemberResult) string {
	var lines []string
	for name, result := range results {
		lines = append(lines, fmt.Sprintf("- %s: %s", name, strings.TrimSpace(result.Reply)))
	}
	return strings.Join(lines, "\n")
}

func selectMemberRecords(
	flowRecords []types.SharedRecord,
	previous map[string]types.MemberResult,
	configured types.Member,
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
		if memberResult, ok := previous[binding.From]; ok {
			for _, record := range memberResult.Records {
				if binding.Record == "" || record.Name == binding.Record {
					selected = append(selected, record)
				}
			}
			continue
		}
		// A binding from a Flow Team (for example `from: research`) is
		// already promoted into req.Records by FlowRuntime. It does not
		// appear in the Team-local previous member map.
		for _, record := range flowRecords {
			if binding.Record == "" || record.Name == binding.Record {
				selected = append(selected, record)
			}
		}
	}
	return deduplicateRecords(selected)
}

func selectTeamRecords(team types.Team, results map[string]types.MemberResult, all []types.SharedRecord) []types.SharedRecord {
	output := team.Output
	if output.IsZero() && !team.Outputs.IsZero() {
		output = team.Outputs
	}
	if len(output.Records) == 0 {
		if output.From != "" && output.Record != "" {
			if memberResult, ok := results[output.From]; ok {
				for _, record := range memberResult.Records {
					if record.Name == output.Record {
						applyOutputScope(&record, output.Scope)
						return []types.SharedRecord{record}
					}
				}
			}
		}
		return all
	}

	selected := make([]types.SharedRecord, 0, len(output.Records))
	for _, binding := range output.Records {
		memberResult, ok := results[binding.From]
		if !ok {
			continue
		}
		for _, record := range memberResult.Records {
			if record.Name == binding.Record {
				applyOutputScope(&record, binding.Scope)
				selected = append(selected, record)
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

func resolveNext(team types.Team, results map[string]types.MemberResult) *types.Route {
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
