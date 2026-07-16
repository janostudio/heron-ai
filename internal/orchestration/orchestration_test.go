package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// mockAgentRuntime implements AgentRuntime for testing
type mockAgentRuntime struct {
	results map[string]*types.AgentResult
}

func (m *mockAgentRuntime) Run(ctx context.Context, agent types.AgentConfig, task types.TaskConfig, input string) (*types.AgentResult, error) {
	if r, ok := m.results[task.Name]; ok {
		return r, nil
	}
	return &types.AgentResult{
		Raw:    "default result",
		Signal: types.SignalContinue,
	}, nil
}

// --- SignalRouter Tests ---

func TestSignalRouter_FirstStage(t *testing.T) {
	flow := types.FlowConfig{
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
			{Name: "stage2", Team: "team2"},
		},
	}
	router := NewSignalRouter(flow)

	if got := router.FirstStage(); got != "stage1" {
		t.Errorf("FirstStage() = %q, want %q", got, "stage1")
	}
}

func TestSignalRouter_FirstStage_Empty(t *testing.T) {
	flow := types.FlowConfig{}
	router := NewSignalRouter(flow)

	if got := router.FirstStage(); got != "" {
		t.Errorf("FirstStage() = %q, want empty", got)
	}
}

func TestSignalRouter_RouteContinueToNextStage(t *testing.T) {
	flow := types.FlowConfig{
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
			{Name: "stage2", Team: "team2"},
		},
	}
	router := NewSignalRouter(flow)

	next, action := router.Route("stage1", types.SignalContinue)
	if next != "stage2" {
		t.Errorf("Route() next = %q, want %q", next, "stage2")
	}
	if action != types.ActionContinue {
		t.Errorf("Route() action = %v, want ActionContinue", action)
	}
}

func TestSignalRouter_RouteContinueFromLastStage_NonLoop(t *testing.T) {
	flow := types.FlowConfig{
		LoopMaxRounds: 0,
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
			{Name: "stage2", Team: "team2"},
		},
	}
	router := NewSignalRouter(flow)

	next, action := router.Route("stage2", types.SignalContinue)
	if next != "" {
		t.Errorf("Route() next = %q, want empty", next)
	}
	if action != types.ActionEnd {
		t.Errorf("Route() action = %v, want ActionEnd", action)
	}
}

func TestSignalRouter_RouteContinueFromLastStage_Loop(t *testing.T) {
	flow := types.FlowConfig{
		LoopMaxRounds: 3,
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
			{Name: "stage2", Team: "team2"},
		},
	}
	router := NewSignalRouter(flow)

	next, action := router.Route("stage2", types.SignalContinue)
	if next != "stage1" {
		t.Errorf("Route() next = %q, want %q", next, "stage1")
	}
	if action != types.ActionContinue {
		t.Errorf("Route() action = %v, want ActionContinue", action)
	}
}

func TestSignalRouter_RouteWaitInput(t *testing.T) {
	flow := types.FlowConfig{
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
		},
	}
	router := NewSignalRouter(flow)

	next, action := router.Route("stage1", types.SignalWaitInput)
	if next != "" {
		t.Errorf("Route() next = %q, want empty", next)
	}
	if action != types.ActionWaitInput {
		t.Errorf("Route() action = %v, want ActionWaitInput", action)
	}
}

func TestSignalRouter_RouteGoalAchieved(t *testing.T) {
	flow := types.FlowConfig{
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
		},
	}
	router := NewSignalRouter(flow)

	next, action := router.Route("stage1", types.SignalGoalAchieved)
	if next != "" {
		t.Errorf("Route() next = %q, want empty", next)
	}
	if action != types.ActionEnd {
		t.Errorf("Route() action = %v, want ActionEnd", action)
	}
}

func TestSignalRouter_RouteGoalFailed(t *testing.T) {
	flow := types.FlowConfig{
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
		},
	}
	router := NewSignalRouter(flow)

	next, action := router.Route("stage1", types.SignalGoalFailed)
	if next != "" {
		t.Errorf("Route() next = %q, want empty", next)
	}
	if action != types.ActionEnd {
		t.Errorf("Route() action = %v, want ActionEnd", action)
	}
}

func TestSignalRouter_RouteGoalImpossible(t *testing.T) {
	flow := types.FlowConfig{
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
		},
	}
	router := NewSignalRouter(flow)

	next, action := router.Route("stage1", types.SignalGoalImpossible)
	if next != "" {
		t.Errorf("Route() next = %q, want empty", next)
	}
	if action != types.ActionEnd {
		t.Errorf("Route() action = %v, want ActionEnd", action)
	}
}

func TestSignalRouter_RouteOnSignalContinue(t *testing.T) {
	target := "stage3"
	flow := types.FlowConfig{
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
			{Name: "stage2", Team: "team2", OnSignal: types.FlowStageSignals{Continue: &target}},
			{Name: "stage3", Team: "team3"},
		},
	}
	router := NewSignalRouter(flow)

	next, action := router.Route("stage2", types.SignalContinue)
	if next != "stage3" {
		t.Errorf("Route() next = %q, want %q", next, "stage3")
	}
	if action != types.ActionContinue {
		t.Errorf("Route() action = %v, want ActionContinue", action)
	}
}

func TestSignalRouter_RouteOnSignalWaitInput(t *testing.T) {
	target := "stage1"
	flow := types.FlowConfig{
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
			{Name: "stage2", Team: "team2", OnSignal: types.FlowStageSignals{WaitInput: &target}},
		},
	}
	router := NewSignalRouter(flow)

	next, action := router.Route("stage2", types.SignalWaitInput)
	if next != "stage1" {
		t.Errorf("Route() next = %q, want %q", next, "stage1")
	}
	if action != types.ActionWaitInput {
		t.Errorf("Route() action = %v, want ActionWaitInput", action)
	}
}

func TestSignalRouter_RouteUnknownStage(t *testing.T) {
	flow := types.FlowConfig{
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
		},
	}
	router := NewSignalRouter(flow)

	next, action := router.Route("nonexistent", types.SignalContinue)
	if next != "" {
		t.Errorf("Route() next = %q, want empty", next)
	}
	if action != types.ActionEnd {
		t.Errorf("Route() action = %v, want ActionEnd", action)
	}
}

// --- LoopGuard Tests ---

func TestLoopGuard_Unlimited(t *testing.T) {
	g := NewLoopGuard(0)

	for i := 0; i < 100; i++ {
		if !g.CanContinue() {
			t.Errorf("CanContinue() = false after %d iterations, want true (unlimited)", i)
		}
		g.Increment()
	}
}

func TestLoopGuard_Limited(t *testing.T) {
	g := NewLoopGuard(3)

	for i := 0; i < 3; i++ {
		if !g.CanContinue() {
			t.Errorf("CanContinue() = false at iteration %d, want true", i)
		}
		g.Increment()
	}

	if g.CanContinue() {
		t.Error("CanContinue() = true after max rounds, want false")
	}
}

func TestLoopGuard_Reset(t *testing.T) {
	g := NewLoopGuard(2)
	g.Increment()
	g.Increment()

	if g.CanContinue() {
		t.Error("CanContinue() = true after max rounds, want false")
	}

	g.Reset()

	if !g.CanContinue() {
		t.Error("CanContinue() = false after reset, want true")
	}
}

func TestLoopGuard_Current(t *testing.T) {
	g := NewLoopGuard(5)
	g.Increment()
	g.Increment()

	if got := g.Current(); got != 2 {
		t.Errorf("Current() = %d, want 2", got)
	}
}

func TestLoopGuard_Max(t *testing.T) {
	g := NewLoopGuard(5)

	if got := g.Max(); got != 5 {
		t.Errorf("Max() = %d, want 5", got)
	}
}

// --- TeamScheduler Tests ---

func makeAgent(name string) types.AgentConfig {
	return types.AgentConfig{Name: name}
}

func TestTeamScheduler_Sequential(t *testing.T) {
	mock := &mockAgentRuntime{
		results: map[string]*types.AgentResult{
			"task1": {Raw: "result1", Signal: types.SignalContinue},
			"task2": {Raw: "result2", Signal: types.SignalContinue},
		},
	}
	scheduler := NewTeamScheduler(mock)

	stages := []types.StageConfig{
		{
			Process: "sequential",
			Tasks: []types.TaskConfig{
				{Name: "task1", Agent: "agent1"},
				{Name: "task2", Agent: "agent2"},
			},
		},
	}

	agents := map[string]types.AgentConfig{
		"agent1": makeAgent("agent1"),
		"agent2": makeAgent("agent2"),
	}

	results, err := scheduler.Schedule(context.Background(), stages, agents, "input")
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}

	if results[0].Raw != "result1" {
		t.Errorf("results[0].Raw = %q, want %q", results[0].Raw, "result1")
	}
	if results[1].Raw != "result2" {
		t.Errorf("results[1].Raw = %q, want %q", results[1].Raw, "result2")
	}
}

func TestTeamScheduler_Parallel(t *testing.T) {
	mock := &mockAgentRuntime{
		results: map[string]*types.AgentResult{
			"task1": {Raw: "parallel1", Signal: types.SignalContinue},
			"task2": {Raw: "parallel2", Signal: types.SignalContinue},
		},
	}
	scheduler := NewTeamScheduler(mock)

	stages := []types.StageConfig{
		{
			Process: "parallel",
			Tasks: []types.TaskConfig{
				{Name: "task1", Agent: "agent1"},
				{Name: "task2", Agent: "agent2"},
			},
		},
	}

	agents := map[string]types.AgentConfig{
		"agent1": makeAgent("agent1"),
		"agent2": makeAgent("agent2"),
	}

	results, err := scheduler.Schedule(context.Background(), stages, agents, "input")
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
}

func TestTeamScheduler_UnknownAgent(t *testing.T) {
	mock := &mockAgentRuntime{
		results: map[string]*types.AgentResult{},
	}
	scheduler := NewTeamScheduler(mock)

	stages := []types.StageConfig{
		{
			Process: "sequential",
			Tasks: []types.TaskConfig{
				{Name: "task1", Agent: "nonexistent"},
			},
		},
	}

	agents := map[string]types.AgentConfig{}

	_, err := scheduler.Schedule(context.Background(), stages, agents, "input")
	if err == nil {
		t.Error("Schedule() expected error for unknown agent, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Schedule() error = %q, want 'not found'", err.Error())
	}
}

func TestTeamScheduler_EmptyStages(t *testing.T) {
	mock := &mockAgentRuntime{}
	scheduler := NewTeamScheduler(mock)

	results, err := scheduler.Schedule(context.Background(), nil, nil, "input")
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}

func TestTeamScheduler_UnknownProcess(t *testing.T) {
	mock := &mockAgentRuntime{}
	scheduler := NewTeamScheduler(mock)

	stages := []types.StageConfig{
		{
			Process: "invalid",
			Tasks:   []types.TaskConfig{},
		},
	}

	_, err := scheduler.Schedule(context.Background(), stages, nil, "input")
	if err == nil {
		t.Error("Schedule() expected error for unknown process, got nil")
	}
	if !strings.Contains(err.Error(), "unknown process type") {
		t.Errorf("Schedule() error = %q, want 'unknown process type'", err.Error())
	}
}

// --- TeamRunner Tests ---

func TestTeamRunner_SingleSequential(t *testing.T) {
	mock := &mockAgentRuntime{
		results: map[string]*types.AgentResult{
			"task1": {Raw: "hello world", Signal: types.SignalContinue},
		},
	}
	scheduler := NewTeamScheduler(mock)
	runner := NewTeamRunner(scheduler)

	team := types.TeamConfig{
		Name: "test-team",
		Stages: []types.StageConfig{
			{
				Process: "sequential",
				Tasks: []types.TaskConfig{
					{Name: "task1", Agent: "agent1"},
				},
			},
		},
	}

	agents := map[string]types.AgentConfig{
		"agent1": makeAgent("agent1"),
	}

	result, err := runner.Run(context.Background(), team, agents, "input")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Raw != "hello world" {
		t.Errorf("result.Raw = %q, want %q", result.Raw, "hello world")
	}
	if result.Signal != types.SignalContinue {
		t.Errorf("result.Signal = %v, want SignalContinue", result.Signal)
	}
}

func TestTeamRunner_Parallel(t *testing.T) {
	mock := &mockAgentRuntime{
		results: map[string]*types.AgentResult{
			"task1": {Raw: "p1", Signal: types.SignalContinue},
			"task2": {Raw: "p2", Signal: types.SignalGoalAchieved},
		},
	}
	scheduler := NewTeamScheduler(mock)
	runner := NewTeamRunner(scheduler)

	team := types.TeamConfig{
		Name: "parallel-team",
		Stages: []types.StageConfig{
			{
				Process: "parallel",
				Tasks: []types.TaskConfig{
					{Name: "task1", Agent: "agent1"},
					{Name: "task2", Agent: "agent2"},
				},
			},
		},
	}

	agents := map[string]types.AgentConfig{
		"agent1": makeAgent("agent1"),
		"agent2": makeAgent("agent2"),
	}

	result, err := runner.Run(context.Background(), team, agents, "input")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Last result signal is used
	if result.Signal != types.SignalGoalAchieved {
		t.Errorf("result.Signal = %v, want SignalGoalAchieved", result.Signal)
	}
}

func TestTeamRunner_NoStages(t *testing.T) {
	mock := &mockAgentRuntime{}
	scheduler := NewTeamScheduler(mock)
	runner := NewTeamRunner(scheduler)

	team := types.TeamConfig{
		Name: "empty-team",
	}

	result, err := runner.Run(context.Background(), team, nil, "input")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Signal != types.SignalContinue {
		t.Errorf("result.Signal = %v, want SignalContinue", result.Signal)
	}
}

// --- FlowEngine Tests ---

func TestFlowEngine_SingleStage(t *testing.T) {
	mock := &mockAgentRuntime{
		results: map[string]*types.AgentResult{
			"task1": {Raw: "done", Signal: types.SignalContinue},
		},
	}
	scheduler := NewTeamScheduler(mock)
	teamRunner := NewTeamRunner(scheduler)

	flow := types.FlowConfig{
		Name: "test-flow",
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
		},
	}

	teams := map[string]types.TeamConfig{
		"team1": {
			Name: "team1",
			Stages: []types.StageConfig{
				{
					Process: "sequential",
					Tasks:   []types.TaskConfig{{Name: "task1", Agent: "agent1"}},
				},
			},
		},
	}

	agents := map[string]types.AgentConfig{
		"agent1": makeAgent("agent1"),
	}

	engine := NewFlowEngine(flow, teams, agents, teamRunner)
	result, err := engine.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(result.Stages) != 1 {
		t.Fatalf("len(Stages) = %d, want 1", len(result.Stages))
	}
	if result.Stages[0].StageName != "stage1" {
		t.Errorf("StageName = %q, want %q", result.Stages[0].StageName, "stage1")
	}
}

func TestFlowEngine_MultiStage(t *testing.T) {
	mock := &mockAgentRuntime{
		results: map[string]*types.AgentResult{
			"task1": {Raw: "first", Signal: types.SignalContinue},
			"task2": {Raw: "second", Signal: types.SignalGoalAchieved},
		},
	}
	scheduler := NewTeamScheduler(mock)
	teamRunner := NewTeamRunner(scheduler)

	flow := types.FlowConfig{
		Name: "multi-flow",
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
			{Name: "stage2", Team: "team2"},
		},
	}

	teams := map[string]types.TeamConfig{
		"team1": {
			Name: "team1",
			Stages: []types.StageConfig{
				{
					Process: "sequential",
					Tasks:   []types.TaskConfig{{Name: "task1", Agent: "agent1"}},
				},
			},
		},
		"team2": {
			Name: "team2",
			Stages: []types.StageConfig{
				{
					Process: "sequential",
					Tasks:   []types.TaskConfig{{Name: "task2", Agent: "agent1"}},
				},
			},
		},
	}

	agents := map[string]types.AgentConfig{
		"agent1": makeAgent("agent1"),
	}

	engine := NewFlowEngine(flow, teams, agents, teamRunner)
	result, err := engine.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(result.Stages) != 2 {
		t.Fatalf("len(Stages) = %d, want 2", len(result.Stages))
	}
	if result.Stages[0].StageName != "stage1" {
		t.Errorf("StageName[0] = %q, want %q", result.Stages[0].StageName, "stage1")
	}
	if result.Stages[1].StageName != "stage2" {
		t.Errorf("StageName[1] = %q, want %q", result.Stages[1].StageName, "stage2")
	}
}

func TestFlowEngine_SignalRouting(t *testing.T) {
	mock := &mockAgentRuntime{
		results: map[string]*types.AgentResult{
			"task1": {Raw: "done", Signal: types.SignalWaitInput},
		},
	}
	scheduler := NewTeamScheduler(mock)
	teamRunner := NewTeamRunner(scheduler)

	flow := types.FlowConfig{
		Name: "signal-flow",
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
		},
	}

	teams := map[string]types.TeamConfig{
		"team1": {
			Name: "team1",
			Stages: []types.StageConfig{
				{
					Process: "sequential",
					Tasks:   []types.TaskConfig{{Name: "task1", Agent: "agent1"}},
				},
			},
		},
	}

	agents := map[string]types.AgentConfig{
		"agent1": makeAgent("agent1"),
	}

	engine := NewFlowEngine(flow, teams, agents, teamRunner)
	result, err := engine.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Signal != types.SignalWaitInput {
		t.Errorf("result.Signal = %v, want SignalWaitInput", result.Signal)
	}
}

func TestFlowEngine_LoopGuardLimit(t *testing.T) {
	mock := &mockAgentRuntime{
		results: map[string]*types.AgentResult{
			"task1": {Raw: "round", Signal: types.SignalContinue},
		},
	}
	scheduler := NewTeamScheduler(mock)
	teamRunner := NewTeamRunner(scheduler)

	flow := types.FlowConfig{
		Name:          "loop-flow",
		LoopMaxRounds: 2,
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "team1"},
		},
	}

	teams := map[string]types.TeamConfig{
		"team1": {
			Name: "team1",
			Stages: []types.StageConfig{
				{
					Process: "sequential",
					Tasks:   []types.TaskConfig{{Name: "task1", Agent: "agent1"}},
				},
			},
		},
	}

	agents := map[string]types.AgentConfig{
		"agent1": makeAgent("agent1"),
	}

	engine := NewFlowEngine(flow, teams, agents, teamRunner)
	result, err := engine.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Should have 2 stages (one per round)
	if len(result.Stages) != 2 {
		t.Fatalf("len(Stages) = %d, want 2", len(result.Stages))
	}
}

func TestFlowEngine_UnknownTeam(t *testing.T) {
	mock := &mockAgentRuntime{}
	scheduler := NewTeamScheduler(mock)
	teamRunner := NewTeamRunner(scheduler)

	flow := types.FlowConfig{
		Name: "bad-flow",
		Stages: []types.FlowStage{
			{Name: "stage1", Team: "nonexistent"},
		},
	}

	engine := NewFlowEngine(flow, nil, nil, teamRunner)
	_, err := engine.Run(context.Background(), "hello")
	if err == nil {
		t.Error("Run() expected error for unknown team, got nil")
	}
}

func TestFlowEngine_EmptyStages(t *testing.T) {
	mock := &mockAgentRuntime{}
	scheduler := NewTeamScheduler(mock)
	teamRunner := NewTeamRunner(scheduler)

	flow := types.FlowConfig{
		Name: "empty-flow",
	}

	engine := NewFlowEngine(flow, nil, nil, teamRunner)
	_, err := engine.Run(context.Background(), "hello")
	if err == nil {
		t.Error("Run() expected error for empty stages, got nil")
	}
}
