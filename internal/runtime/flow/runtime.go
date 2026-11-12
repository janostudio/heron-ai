package flow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Runtime owns FlowSession and FlowTurn lifecycle for the new Flow/Team
// runtime. It does not know about Stage or Task.
//
// It carries no workspace root: every Agent's workspace is resolved once at
// config load by resolveWorkspace (internal/config/definitions.go), which
// inherits agent → team → flow. A root threaded down per turn from here
// could only shadow that resolution, so nothing in the runtime takes one.
type Runtime struct {
	// definitions is the defining store rather than a bare tree: a turn must
	// resolve against the definitions in force when that turn started, and a
	// new tree may be published between turns (an Agent creating definitions
	// mid-conversation). Each turn entry point takes one Snapshot and threads
	// it down; holding a *types.Definitions here would pin the runtime to the
	// tree that existed at build time.
	definitions *types.DefinitionStore
	teams       types.TeamRuntime
	sessions    storage.SessionWriter
	evidence    storage.EvidenceStore
	limits      types.RuntimeLimits
	tasks       types.ToolTaskStore
	// taskControl stops a durable Tool task. The store can only record what a
	// task is doing; stopping it needs the executor that owns its context.
	// Optional, and nil-safe: without it Cancel reports what it could not stop
	// instead of pretending the work ended.
	taskControl types.ToolTaskCanceller
	media       types.MediaStore
	// transcripts is the per-Agent conversation store. A finished Team call
	// enters the caller's conversation through it as a tool result.
	transcripts types.AgentTranscriptStore
	// resumeLocks serialises the resume entry points of one session. It is
	// keyed by session rather than held once for the runtime: a runtime serves
	// every conversation in the process, and one conversation's slow resume
	// must not delay another's.
	resumeLocks sync.Map

	// dispatchMu guards busyTeams, the in-process record of which Teams are
	// executing right now. A Team is a single execution unit, so two messages
	// for the same Team must not run at once: the scheduler marks a Team busy
	// before launching it and clears the mark when its round finishes. This is
	// the live counterpart of the persisted TeamSession.Status a scheduler
	// reads after a restart.
	dispatchMu sync.Mutex
	busyTeams  map[teamKey]bool
	// turnMu guards runningTurns: the sessions this process is executing a turn
	// for right now. It is what tells "the turn is still working" apart from
	// "a crash left the session marked running" — the event stream cannot, and
	// getting it wrong means either refusing a live turn or resuming a dead one.
	turnMu       sync.Mutex
	runningTurns map[string]bool
	// seqMu serializes per_task instance ordinal allocation. The ordinal is read
	// from the event stream and then incremented, so without it two dispatches
	// from one model response can pick the same number and collide.
	seqMu sync.Mutex
	// allocatedSeq remembers the highest per_task ordinal handed out per session
	// in this process, so an instance whose created-event has not been written
	// yet still reserves its number.
	allocatedSeq map[string]int
	// turnCancels holds the cancel function of the turn running in each session,
	// so Cancel can stop the work instead of only labelling the session. A
	// status written while the turn keeps going is a lie the user sees as an
	// answer arriving after they cancelled.
	turnCancels map[string]context.CancelFunc
	// closedInterrupts records the sessions whose dangling executions have
	// already been given a terminal event by this process. One pass is enough:
	// the terminal it writes is what subsequent replays read.
	closedInterrupts map[string]bool
	// pendingWakes de-duplicates wake retries for sessions that were still
	// running when their wake-up arrived.
	pendingWakes map[string]bool

	// wakeupMu guards wakeupIndex: the self-scheduled wake-ups this process
	// knows about, per session. The index is a cache over the event stream
	// (session.wakeup.created / fired / skipped / cancelled / expired), which stays
	// the source of truth so a restart rebuilds what is still due.
	wakeupMu    sync.Mutex
	wakeupIndex map[string]*wakeupIndex
	wakeupCfg   types.WakeupLimits
	// wakeupSignal nudges the scheduler loop to recompute its next deadline
	// after a registration or a cancellation. It is buffered and written
	// non-blocking: a nudge that arrives while one is already pending is the
	// same nudge.
	wakeupSignal chan struct{}
	// wakeupInflight marks the records the scheduler has started firing, so
	// the loop cannot start the same one twice before its state event lands.
	// Guarded by wakeupMu.
	wakeupInflight map[string]bool
	// wakeupHold pins a record until a retry is allowed, when the reschedule
	// itself could not be persisted. The loop takes the earliest scheduled
	// FireAt, so a record left at a FireAt it already passed is picked up
	// again immediately — without this the loop spins at full CPU whenever a
	// state event cannot be written. Guarded by wakeupMu.
	wakeupHold map[string]time.Time

	// cancelMu guards sessionWork: the cancel functions of background
	// executions this process is running for a session. A dispatched Team
	// outlives the turn that started it, so it cannot inherit that turn's
	// context — it was given context.Background() instead, which also meant
	// nothing could ever stop it. turnCancels covers the turn itself;
	// this covers the work the turn handed off before it ended.
	cancelMu    sync.Mutex
	sessionWork map[string]map[int]context.CancelFunc
	nextWorkID  int
	// wakeupSessionLister enumerates sessions for startup recovery. It is
	// injected because the sessions directory is app-layer layout.
	wakeupSessionLister func(context.Context) ([]string, error)
	// wakeupOnce starts the scheduler loop at most once. The loop is started on
	// demand rather than at build time: a process with no timers should not
	// hold a goroutine that does nothing, and a test that never schedules a
	// wake-up should not leave one behind.
	wakeupOnce sync.Once
	// wakeRetryInterval / wakeRetryAttempts override the wake-retry knobs.
	// Zero means "use the package default". They are per-runtime so the retry
	// goroutine never reads a value a test is rewriting.
	wakeRetryInterval time.Duration
	wakeRetryAttempts int

	// asyncDispatch opts the runtime into P0 mailbox dispatch: when the entry
	// team activates a team, the work is handed to a background goroutine and
	// the current FlowTurn ends immediately instead of waiting for the
	// activated team. It defaults to false, in which case
	// the activate path is byte-for-byte the synchronous behaviour it has
	// always had.
	asyncDispatch bool
	// wakeFn is the app-layer wake-up hook (usually go ResumeForMailbox).
	// It is called after a background dispatch finishes and the session is
	// idle enough to be resumed. nil means "no wake": the result message is
	// still recorded, the caller is expected to poll.
	wakeFn func(sessionID string)
}

func NewRuntime(
	definitions *types.DefinitionStore,
	teamRuntime types.TeamRuntime,
	sessionWriter storage.SessionWriter,
	evidenceStore storage.EvidenceStore,
) *Runtime {
	return &Runtime{
		definitions:    definitions,
		teams:          teamRuntime,
		sessions:       sessionWriter,
		evidence:       evidenceStore,
		limits:         types.RuntimeLimits{}.WithDefaults(),
		busyTeams:      make(map[teamKey]bool),
		pendingWakes:   make(map[string]bool),
		wakeupIndex:    make(map[string]*wakeupIndex),
		wakeupCfg:      types.DefaultWakeupLimits(),
		wakeupSignal:   make(chan struct{}, 1),
		wakeupInflight: make(map[string]bool),
	}
}

func (r *Runtime) SetLimits(limits types.RuntimeLimits) {
	r.limits = limits.WithDefaults()
}

func (r *Runtime) SetTaskStore(tasks types.ToolTaskStore) {
	r.tasks = tasks
}

// SetTaskControl wires the cancel side of durable Tool tasks.
//
// The store alone cannot stop work: it records what a task is doing. A durable
// task runs on its own context, deliberately detached from the turn that
// created it, so cancelling a session reaches it through this handle or not at
// all. Optional: a runtime built without one still cancels turns and Teams,
// and cancelling a Task is refused rather than silently ignored.
func (r *Runtime) SetTaskControl(control types.ToolTaskCanceller) {
	r.taskControl = control
}

func (r *Runtime) SetMediaStore(store types.MediaStore) {
	r.media = store
}

// SetAsyncDispatch enables or disables asynchronous mailbox dispatch. When
// enabled, the entry team's activate hands the work to a background goroutine
// and the FlowTurn ends immediately; the activated team's result is posted
// back to the entry team's mailbox and the session is woken when idle. When
// disabled (the default) the activate path is fully synchronous and unchanged.
func (r *Runtime) SetAsyncDispatch(enabled bool) {
	r.asyncDispatch = enabled
}

// SetWaker injects the app-layer wake-up callback, usually
// `go ResumeForMailbox(sessionID)`. It may be nil; a nil waker means the
// background dispatch records the result but never wakes the session.
func (r *Runtime) SetWaker(fn func(sessionID string)) {
	r.wakeFn = fn
}

func (r *Runtime) persistContextBlocks(ctx context.Context, blocks []types.ContextBlock) ([]types.ContextBlock, error) {
	if len(blocks) == 0 || r.media == nil {
		return blocks, nil
	}
	result := make([]types.ContextBlock, len(blocks))
	for i, block := range blocks {
		result[i] = block
		if len(block.Parts) == 0 {
			continue
		}
		result[i].Parts = make([]types.ContentPart, len(block.Parts))
		for j, part := range block.Parts {
			result[i].Parts[j] = part
			if part.Media == nil {
				continue
			}
			stored, err := r.media.Store(ctx, *part.Media)
			if err != nil {
				return nil, err
			}
			result[i].Parts[j].Media = &stored
		}
	}
	return result, nil
}

func (r *Runtime) Start(ctx context.Context, req types.StartFlowRequest) (types.FlowTurnResult, error) {
	if err := r.validateDependencies(req.FlowID); err != nil {
		return types.FlowTurnResult{}, err
	}
	// One FlowTurn resolves against one definitions tree (see Runtime.definitions).
	defs := r.definitions.Snapshot()
	if strings.TrimSpace(req.FlowID) == "" {
		req.FlowID = defs.Flow.ID
	}
	blocks, err := r.persistContextBlocks(ctx, req.ContextBlocks)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	session, createErr := r.createSession(ctx, req.FlowID)
	if createErr != nil {
		return types.FlowTurnResult{}, createErr
	}
	return r.runTurnWithContext(ctx, session, req.Input, blocks)
}

// CreateSession opens a session with no turn in it.
//
// The page needs a conversation to navigate into before the user has typed
// anything; creating it through the first turn would mean waiting for that turn
// (a dispatch can take minutes) with nothing on screen.
func (r *Runtime) CreateSession(ctx context.Context, flowID string) (types.FlowSession, error) {
	if err := r.validateDependencies(flowID); err != nil {
		return types.FlowSession{}, err
	}
	defs := r.definitions.Snapshot()
	if strings.TrimSpace(flowID) == "" {
		flowID = defs.Flow.ID
	}
	return r.createSession(ctx, flowID)
}

// createSession writes the creation event and returns the session. It starts
// nothing: the first turn is a separate step, whether it comes immediately
// (Start) or after the user types into the page.
func (r *Runtime) createSession(ctx context.Context, flowID string) (types.FlowSession, error) {
	now := time.Now().UTC()
	session := types.FlowSession{
		ID:        newID("fs"),
		FlowID:    flowID,
		Status:    types.SessionCreated,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventFlowSessionCreated,
			FlowSessionID: session.ID,
		},
		Payload: map[string]any{"session": session},
	}); err != nil {
		return types.FlowSession{}, err
	}
	return session, nil
}

func (r *Runtime) HandleInput(ctx context.Context, sessionID string, input string) (types.FlowTurnResult, error) {
	return r.HandleInputWithContext(ctx, sessionID, input, nil)
}

func (r *Runtime) HandleInputWithContext(ctx context.Context, sessionID, input string, blocks []types.ContextBlock) (types.FlowTurnResult, error) {
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	// A turn of this process is running: this is the queueing case, and it has to
	// be decided before the recovery check. That check reads the event log, where
	// "running right now" and "died mid-flight" look identical except for the
	// live index — and the index covers the flow turn, not the team and call
	// turns inside it, so a healthy turn read as an unfinished execution and the
	// second message was refused instead of queued.
	if session.Status == types.SessionRunning && r.isTurnRunning(sessionID) {
		return r.queueUserInput(ctx, session, input, blocks)
	}
	if err := r.checkNewTurnAllowed(ctx, session); err != nil {
		return types.FlowTurnResult{}, err
	}
	persisted, err := r.persistContextBlocks(ctx, blocks)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	return r.runTurnWithContext(ctx, session, input, persisted)
}

// SubmitInput accepts a message and returns before the turn runs. It is the
// asynchronous counterpart of HandleInput: the caller learns that the message
// was taken, and the turn's events arrive on the session stream like any other
// turn.
//
// The message is always queued. A session with a turn in flight drains the
// queue when that turn ends; an idle one has nothing to end, so the drain
// starts here. Both paths are the same one the synchronous entry point uses
// when it finds the session busy, which is what keeps a queued message from
// being handled two different ways depending on how it arrived.
func (r *Runtime) SubmitInput(ctx context.Context, sessionID, input string, blocks []types.ContextBlock) (types.FlowTurnResult, error) {
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	busy := session.Status == types.SessionRunning && r.isTurnRunning(sessionID)
	if !busy {
		if err := r.checkNewTurnAllowed(ctx, session); err != nil {
			return types.FlowTurnResult{}, err
		}
	}
	queued, err := r.queueUserInput(ctx, session, input, blocks)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if !busy {
		// Detached from the caller's context on purpose: the acceptance has
		// already been written, so cancelling the HTTP request must not cancel
		// the turn it just accepted.
		go r.drainQueuedInput(context.Background(), sessionID)
	}
	return queued, nil
}

// checkNewTurnAllowed rejects the states where accepting another message would
// strand it. Callers must have handled "a turn of this process is running"
// first: that case is resumable and must not be read as an unfinished
// execution, and only the caller knows whether the message should be queued.
func (r *Runtime) checkNewTurnAllowed(ctx context.Context, session types.FlowSession) error {
	// Completed is deliberately absent from the rejection list: it only
	// exists in sessions recorded before the lifecycle refactor, and those
	// legacy sessions stay continuable with the same session_id.
	//
	// Failed, cancelled and interrupted are absent for the same reason: they
	// describe an earlier turn (or an earlier process), never the conversation.
	// A new turn starts from the Flow entry team and rewrites the session status
	// to running, so continuing is safe; blocking here would make one bad turn
	// permanently end the conversation, which is not what the user sees — they
	// see that turn's error and then send the next message.
	if session.Status == types.SessionWaitingApproval {
		return fmt.Errorf("flow session %q is already %s", session.ID, session.Status)
	}
	// The recovery check comes before the waiting_tool one on purpose. A session
	// parked on a Tool whose task record is gone is still status waiting_tool,
	// but "resume the pending Tool task" is advice that cannot be followed: the
	// resume fails on the missing record. Reporting it as an unfinished
	// execution points at the one path that does work — recovery.
	if interrupted, err := r.RecoveryStatus(ctx, session.ID); err == nil && len(interrupted.Interrupted) > 0 {
		return fmt.Errorf("flow session %q has unfinished execution; use recovery status first", session.ID)
	}
	// waiting_tool is rejected for the same reason waiting_approval is: this
	// entry point starts a brand-new FlowTurn from the Flow entry team and
	// never consults pendingTeamResume, so a turn started here has no way to
	// collect the async Tool results the session is parked on. The pending
	// tasks keep running, their completions wake a session that is no longer
	// in waiting_tool (the wake-up is then dropped), and every attempt stacks
	// another open turn on top. Resume is the path that resumes pending Tool
	// tasks; use it instead.
	if session.Status == types.SessionWaitingTool {
		return fmt.Errorf(
			"flow session %q is waiting for a Tool: resume the pending Tool task instead of starting a new turn",
			session.ID,
		)
	}
	if running, err := r.runningTeams(ctx, session.ID); err != nil {
		return err
	} else if len(running) > 0 {
		return fmt.Errorf(
			"flow session %q still has running teams %v: wait for them to finish or cancel the session before starting a new turn",
			session.ID, running,
		)
	}
	return nil
}

func (r *Runtime) Resume(ctx context.Context, sessionID string, input string) (types.FlowTurnResult, error) {
	return r.ResumeWithContext(ctx, sessionID, input, nil)
}

func (r *Runtime) ResumeWithContext(ctx context.Context, sessionID, input string, blocks []types.ContextBlock) (types.FlowTurnResult, error) {
	defer r.resumeLock(sessionID)()

	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if session.Status == types.SessionWaitingApproval {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q is waiting for approval; approval resume is not configured", session.ID)
	}
	if session.Status == types.SessionRunning && r.isTurnRunning(sessionID) {
		return r.queueUserInput(ctx, session, input, blocks)
	}
	if session.Status != types.SessionIdle &&
		session.Status != types.SessionWaitingTool {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q is not waiting for input or tool", session.ID)
	}
	if interrupted, err := r.RecoveryStatus(ctx, sessionID); err == nil && len(interrupted.Interrupted) > 0 {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q has unfinished execution; use recovery status first", sessionID)
	}
	persistedBlocks, err := r.persistContextBlocks(ctx, blocks)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	pending, ok := pendingAgentResume(replay.Events)
	pendingTeams := pendingTeamResumes(replay.Events)
	if len(pendingTeams) > 1 {
		// Several Teams are paused at once, which the single-resume path
		// cannot resolve without dropping one of them. Refuse rather than
		// silently resuming only the most recent.
		return types.FlowTurnResult{}, fmt.Errorf(
			"flow session %q has %d Teams paused (waiting for a Tool or approval); resume them individually",
			sessionID, len(pendingTeams),
		)
	}
	if len(pendingTeams) == 1 {
		pendingTeam := pendingTeams[0]
		if pendingTeam.FlowTurnID != "" {
			if ready, err := r.pendingTasksReady(ctx, pendingTeam.PendingToolTasks); err != nil {
				return types.FlowTurnResult{}, err
			} else if !ready {
				return types.FlowTurnResult{
					Session:          session,
					PendingToolTasks: pendingTeam.PendingToolTasks,
					Turn: types.FlowTurn{
						FlowSessionID: session.ID,
						Status:        types.TurnWaitingTool,
					},
				}, nil
			}
		}
		resumeResults := make(map[string]types.CallResult)
		for callID, callResult := range pendingTeam.CallResults {
			if callResult.Status == types.TurnCompleted {
				resumeResults[callID] = callResult
			}
		}
		resumeCalls := make(map[string]types.TeamCallResume, len(pendingTeam.PendingToolTasks))
		for _, pendingTask := range pendingTeam.PendingToolTasks {
			resumeCalls[pendingTask.CallID] = types.TeamCallResume{
				CallTurnID:   pendingTask.CallTurnID,
				CheckpointID: pendingTask.CheckpointID,
				TaskID:       pendingTask.TaskID,
			}
		}
		return r.runTurnWithOrigin(ctx, r.definitions.Snapshot(), session, input, persistedBlocks, []activation{{
			teamID:        pendingTeam.TeamID,
			callerTeam:    pendingTeam.CallerTeam,
			force:         true,
			attempt:       pendingTeam.Attempt + 1,
			resumeResults: resumeResults,
			resumeCalls:   resumeCalls,
		}}, turnOriginResume, time.Time{}, nil, nil)
	}
	if !ok {
		return r.runTurnWithContext(ctx, session, input, persistedBlocks)
	}
	return r.runTurnWithOrigin(ctx, r.definitions.Snapshot(), session, input, persistedBlocks, []activation{{
		teamID:             pending.TeamID,
		callerTeam:         pending.CallerTeam,
		force:              true,
		attempt:            pending.Attempt + 1,
		resumeCallID:       pending.CallID,
		resumeCheckpointID: pending.CheckpointID,
		resumeInput:        input,
		resumeCallTurnID:   pending.CallTurnID,
		resumeTaskID:       pending.TaskID,
	}}, turnOriginResume, time.Time{}, nil, nil)
}

func (r *Runtime) ResumeApproval(ctx context.Context, sessionID, approvalID string, approved bool, reason string) (types.FlowTurnResult, error) {
	return r.ResumeApprovalWithResponse(ctx, sessionID, types.HITLResponse{
		RequestID: approvalID,
		Approved:  approved,
		Reason:    reason,
		Channel:   "http",
	})
}

// ResumeApprovalWithResponse is the auditable approval entry point. Approval
// requests are durable and intentionally never expire; callers decide in
// order by passing the request ID found in the latest waiting state.
func (r *Runtime) ResumeApprovalWithResponse(ctx context.Context, sessionID string, decision types.HITLResponse) (types.FlowTurnResult, error) {
	defer r.resumeLock(sessionID)()

	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if session.Status != types.SessionWaitingApproval {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q is not waiting for approval", sessionID)
	}
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	pendingTeams := pendingTeamResumes(replay.Events)
	if len(pendingTeams) == 0 {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q has no pending approval", sessionID)
	}
	// An approval is addressed by RequestID, so the set is narrowed to the Team
	// that owns the requested approval rather than taking whichever paused
	// first.
	pending, ok := pendingTeamForApproval(pendingTeams, decision.RequestID)
	if !ok {
		return types.FlowTurnResult{}, fmt.Errorf("approval %q is not pending in this session", decision.RequestID)
	}
	for callID, callResult := range pending.CallResults {
		if callResult.Status != types.TurnWaitingApproval ||
			callResult.PendingApproval == nil ||
			callResult.PendingApproval.RequestID != decision.RequestID {
			continue
		}
		resumeResults := make(map[string]types.CallResult)
		for siblingID, sibling := range pending.CallResults {
			if siblingID != callID && sibling.Status == types.TurnCompleted {
				resumeResults[siblingID] = sibling
			}
		}
		if decision.DecidedAt.IsZero() {
			decision.DecidedAt = time.Now().UTC()
		}
		return r.runTurnWithActivations(ctx, session, "", []activation{{
			teamID:             pending.TeamID,
			callerTeam:         pending.CallerTeam,
			force:              true,
			attempt:            pending.Attempt + 1,
			resumeCallID:       callID,
			resumeCheckpointID: callResult.CheckpointID,
			resumeCallTurnID:   callResult.CallTurnID,
			resumeApprovalID:   decision.RequestID,
			resumeApproval:     &decision,
			resumeResults:      resumeResults,
			resumeCalls: map[string]types.TeamCallResume{
				callID: {
					CallTurnID:   callResult.CallTurnID,
					CheckpointID: callResult.CheckpointID,
					ApprovalID:   decision.RequestID,
					Approval:     &decision,
				},
			},
		}})
	}
	return types.FlowTurnResult{}, fmt.Errorf("approval %q is not pending in flow session %q", decision.RequestID, sessionID)
}

func (r *Runtime) pendingTasksReady(ctx context.Context, pending []types.PendingToolTask) (bool, error) {
	if len(pending) == 0 {
		return true, nil
	}
	if r.tasks == nil {
		return false, errors.New("tool task store is not configured")
	}
	for _, item := range pending {
		if item.TaskID == "" {
			return false, fmt.Errorf("pending Tool task for call %q has no task id", item.CallID)
		}
		task, err := r.tasks.Load(ctx, item.TaskID)
		if err != nil {
			return false, err
		}
		if task.Status == types.ToolTaskQueued || task.Status == types.ToolTaskRunning {
			return false, nil
		}
	}
	return true, nil
}

func (r *Runtime) RecoveryStatus(ctx context.Context, sessionID string) (types.RecoveryStatus, error) {
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.RecoveryStatus{}, err
	}
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return types.RecoveryStatus{}, err
	}
	interrupted := interruptedExecutions(replay.Events, r.liveIndexFor(sessionID))
	// A Team parked on a Tool whose task record is gone is not covered by the
	// check above: that check treats every waiting turn as resumable, which is
	// right until the thing it is waiting for no longer exists. Left out, such a
	// session has no path at all — Resume fails with "task not found",
	// HandleInput refuses a session waiting on a Tool, and Recover reports
	// nothing to recover.
	interrupted = append(interrupted, r.orphanedWaitingExecutions(ctx, replay.Events)...)
	sortInterrupted(interrupted)
	history := recoveryHistory(replay.Events)
	if len(interrupted) > 0 &&
		session.Status != types.SessionCompleted &&
		session.Status != types.SessionFailed &&
		session.Status != types.SessionCancelled {
		session.Status = types.SessionInterrupted
	}
	return types.RecoveryStatus{
		Session:         session,
		Interrupted:     interrupted,
		RecoveryHistory: history,
	}, nil
}

func (r *Runtime) Recover(ctx context.Context, sessionID string, req types.RecoveryRequest) (types.FlowTurnResult, error) {
	status, err := r.RecoveryStatus(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if req.Action == types.RecoveryInspect {
		return types.FlowTurnResult{
			Session: status.Session,
			Error:   recoverySummary(status.Interrupted),
		}, nil
	}
	if req.Action != types.RecoveryWait &&
		req.Action != types.RecoveryCoordinate &&
		req.Action != types.RecoveryRetry {
		return types.FlowTurnResult{}, fmt.Errorf("unsupported recovery action %q", req.Action)
	}
	if len(status.Interrupted) == 0 {
		return types.FlowTurnResult{Session: status.Session}, nil
	}

	target, err := selectInterrupted(status.Interrupted, req.TargetTurnID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if err := r.appendSessionEvent(ctx, sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventRecoveryRequested,
			FlowSessionID: sessionID,
			TeamID:        target.TeamID,
			TeamTurnID:    target.TeamTurnID,
			CallID:        target.CallID,
			CallTurnID:    target.CallTurnID,
			Attempt:       target.Attempt + 1,
			RecoveryOf:    recoveryTargetID(target),
		},
		Payload: map[string]any{"request": req, "target": target},
	}); err != nil {
		return types.FlowTurnResult{}, err
	}

	switch req.Action {
	case types.RecoveryWait:
		session, err := r.loadSession(ctx, sessionID)
		if err != nil {
			return types.FlowTurnResult{}, err
		}
		session.Status = types.SessionInterrupted
		session.UpdatedAt = time.Now().UTC()
		if err := r.appendSessionEvent(ctx, sessionID, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventFlowSessionUpdated,
				FlowSessionID: sessionID,
			},
			Payload: map[string]any{"session": session},
		}); err != nil {
			return types.FlowTurnResult{}, err
		}
		return types.FlowTurnResult{Session: session, Error: target.RetryReason}, nil
	case types.RecoveryCoordinate:
		if target.TeamID == "" {
			return types.FlowTurnResult{}, fmt.Errorf("interrupted %s cannot coordinate because no Team was recorded", target.Kind)
		}
		// Snapshot once here so the entry team this activation names and the
		// tree the turn resolves against come from the same generation.
		defs := r.definitions.Snapshot()
		result, runErr := r.runTurnWithActivationsAndContext(ctx, defs, status.Session, recoveryInput(req, target), nil, []activation{{
			teamID:     defs.Flow.EntryTeamID,
			callerTeam: target.CallerTeam,
			force:      true,
			attempt:    target.Attempt + 1,
			recoveryOf: recoveryTargetID(target),
		}})
		if runErr != nil {
			return result, runErr
		}
		return result, r.appendRecoveryCompleted(ctx, sessionID, target, req)
	case types.RecoveryRetry:
		if !req.AllowSideEffectReplay {
			return types.FlowTurnResult{}, fmt.Errorf("retry requires allow_side_effect_replay=true; replay may repeat workspace or external side effects")
		}
		if target.TeamID == "" {
			return types.FlowTurnResult{}, fmt.Errorf("interrupted %s cannot be retried because no Team was recorded", target.Kind)
		}
		result, runErr := r.runTurnWithActivations(ctx, status.Session, recoveryInput(req, target), []activation{{
			teamID:     target.TeamID,
			callerTeam: target.CallerTeam,
			force:      true,
			attempt:    target.Attempt + 1,
			recoveryOf: recoveryTargetID(target),
		}})
		if runErr != nil {
			return result, runErr
		}
		return result, r.appendRecoveryCompleted(ctx, sessionID, target, req)
	default:
		return types.FlowTurnResult{}, fmt.Errorf("unsupported recovery action %q", req.Action)
	}
}

func (r *Runtime) Cancel(ctx context.Context, sessionID string) error {
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.Status == types.SessionCompleted ||
		session.Status == types.SessionFailed ||
		session.Status == types.SessionCancelled {
		return nil
	}
	session.Status = types.SessionCancelled
	session.UpdatedAt = time.Now().UTC()
	if err := r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventFlowSessionUpdated,
			FlowSessionID: session.ID,
		},
		Payload: map[string]any{"session": session},
	}); err != nil {
		return err
	}
	// Stop the work, not just the label. The turn's own finishTurn writes the
	// terminal event with the cancelled status.
	r.cancelRunningTurn(sessionID)
	// A dispatched Team outlives the turn that started it and runs on a context
	// of its own, so cancelling the turn does not reach it. This is what stops
	// those; without it the child keeps working after the user asked it to stop.
	if stopped := r.cancelSessionWork(sessionID); stopped > 0 {
		logging.Info("cancelled background work for session", map[string]any{
			"flow_session_id": sessionID,
			"executions":      stopped,
		})
	}
	// A durable Tool task is a third kind of work: not the turn, not a
	// dispatched Team, but a queued or running task owned by the Agent layer
	// and running on its own context. Cancelling the session has to reach it by
	// id, or the child keeps calling models and writing files after the user
	// asked everything to stop.
	if stopped := r.cancelSessionTasks(ctx, sessionID); stopped > 0 {
		logging.Info("cancelled durable Tool tasks for session", map[string]any{
			"flow_session_id": sessionID,
			"tasks":           stopped,
		})
	}
	return nil
}

// cancelSessionTasks stops the durable Tool tasks a session still owns and
// reports how many were cancelled.
//
// Only queued and running tasks are touched: a finished task has nothing left
// to stop, and re-cancelling it would rewrite a result that already landed.
// The store listing is unfiltered — there is no per-session index — so this
// walks it, which is acceptable for a call made once per cancellation.
func (r *Runtime) cancelSessionTasks(ctx context.Context, sessionID string) int {
	if r.taskControl == nil || r.tasks == nil {
		return 0
	}
	// Cancelling a task writes its state, so the write must survive a caller
	// whose request context is already gone.
	writeCtx := writeEvenIfCancelled(ctx)
	tasks, err := r.tasks.List(writeCtx)
	if err != nil {
		logging.Error("list Tool tasks to cancel", map[string]any{
			"flow_session_id": sessionID,
			"error":           err.Error(),
		})
		return 0
	}
	cancelled := 0
	for _, task := range tasks {
		if task.FlowSessionID != sessionID {
			continue
		}
		if task.Status != types.ToolTaskQueued && task.Status != types.ToolTaskRunning {
			continue
		}
		if cancelErr := r.taskControl.Cancel(writeCtx, task.ID); cancelErr != nil {
			logging.Error("cancel Tool task on session cancel", map[string]any{
				"flow_session_id": sessionID,
				"task_id":         task.ID,
				"error":           cancelErr.Error(),
			})
			continue
		}
		cancelled++
	}
	return cancelled
}

func (r *Runtime) Status(ctx context.Context, sessionID string) (types.FlowSession, error) {
	status, err := r.RecoveryStatus(ctx, sessionID)
	if err != nil {
		return types.FlowSession{}, err
	}
	return status.Session, nil
}

// runTurnWithContext starts a turn under the definitions currently published.
// Every other turn entry point funnels through
// runTurnWithActivationsAndContext, which is where the snapshot is taken, so
// the tree is read exactly once per turn and every routing decision inside
// that turn comes from the same generation.
func (r *Runtime) runTurnWithContext(ctx context.Context, session types.FlowSession, input string, blocks []types.ContextBlock) (types.FlowTurnResult, error) {
	defs := r.definitions.Snapshot()
	return r.runTurnWithActivationsAndContext(ctx, defs, session, input, blocks, []activation{
		{teamID: defs.Flow.EntryTeamID},
	})
}

func (r *Runtime) runTurnWithActivations(
	ctx context.Context,
	session types.FlowSession,
	input string,
	initial []activation,
) (types.FlowTurnResult, error) {
	return r.runTurnWithActivationsAndContext(ctx, r.definitions.Snapshot(), session, input, nil, initial)
}

// runTurnWithActivationsAndContext executes one FlowTurn against defs. defs is
// the single snapshot for the whole turn: the activation queue, the routing
// decisions and the team/agent resolution all read it, and nothing in the turn
// re-reads the store. A Swap that lands mid-turn therefore takes effect on the
// next turn, not on this one.
func (r *Runtime) runTurnWithActivationsAndContext(
	ctx context.Context,
	defs *types.Definitions,
	session types.FlowSession,
	input string,
	blocks []types.ContextBlock,
	initial []activation,
) (types.FlowTurnResult, error) {
	return r.runTurnWithOrigin(ctx, defs, session, input, blocks, initial, turnOriginUser, time.Time{}, nil, nil)
}

// Turn origin values recorded on FlowTurn.Origin.
const (
	// turnOriginUser is a turn started from the caller's input, the only turn
	// whose reply is an answer to what the user asked.
	turnOriginUser = "user"
	// turnOriginMailbox is a turn woken by async dispatch mail: a Team reports
	// back, or the entry Team summarises what came back.
	turnOriginMailbox = "mailbox"
	// turnOriginResume is a turn that continues a paused Tool call or approval.
	turnOriginResume = "resume"
	// turnOriginWakeup is a turn the session started for itself, from a note
	// its own model left earlier. It is not a response to anything the user
	// said, and its input block is placed in the system section for that
	// reason.
	turnOriginWakeup = "wakeup"
)

// turnOriginText phrases one turn's origin for the model. The input text alone
// cannot carry this: every wake-up replays the sentence the user originally
// typed, so a turn that exists because a dispatched Team reported back reads
// exactly like a fresh request. Answering it as a fresh request dispatches the
// same work a second time.
func turnOriginText(origin string) string {
	switch origin {
	case turnOriginUser:
		return "user — the message below is the user's own request."
	case turnOriginMailbox:
		return "mailbox — a dispatched team reported back; the user did not send a new message."
	case turnOriginResume:
		return "resume — this turn continues a paused tool call or approval."
	case turnOriginWakeup:
		return "wakeup — the session scheduled this turn for itself; there is no new user message."
	default:
		return ""
	}
}

// runTurnWithOrigin is runTurnWithActivationsAndContext with an explicit
// origin: caller input, a mailbox wake-up, or a resumed Tool call/approval.
// Only a mailbox wake-up can be an intermediate turn; a resume is the caller
// finishing what it started, so its reply answers them.
func (r *Runtime) runTurnWithOrigin(
	ctx context.Context,
	defs *types.Definitions,
	session types.FlowSession,
	input string,
	blocks []types.ContextBlock,
	initial []activation,
	origin string,
	scopeSince time.Time,
	deliveries []types.UserDelivery,
	// pendingFailure is a condition the caller already knows is fatal for this
	// turn — a dispatched Team that came back failed, for instance. The turn
	// still runs (the model may say something useful about it), but it ends
	// failed and its text is not published as a conclusion: a turn whose work
	// did not happen must not read as an answer.
	pendingFailure error,
) (types.FlowTurnResult, error) {
	// Publish that this process owns a live turn for the session, so a message
	// arriving mid-turn is queued rather than mistaken for a crash leftover.
	ctx, clearRunning := r.markTurnRunning(ctx, session.ID)
	defer clearRunning()
	r.closeInterruptedTurns(ctx, session.ID)
	if err := r.validateDependencies(session.FlowID); err != nil {
		return types.FlowTurnResult{}, err
	}
	if r.teams == nil {
		return types.FlowTurnResult{}, errors.New("team runtime is nil")
	}

	session.Status = types.SessionRunning
	session.UpdatedAt = time.Now().UTC()
	flowAttempt := 1
	recoveryOf := ""
	for _, initialActivation := range initial {
		if initialActivation.attempt > flowAttempt {
			flowAttempt = initialActivation.attempt
		}
		if recoveryOf == "" {
			recoveryOf = initialActivation.recoveryOf
		}
	}
	turn := types.FlowTurn{
		ID:            newID("ft"),
		FlowSessionID: session.ID,
		// Origin of this turn, and whether its reply answers the user. A
		// wake-up that activates a Team without a caller (the entry Team
		// summarising its inbox) is the last hop of a chained return and is
		// the only mailbox turn worth relaying; a wake-up with an activator
		// just hands work or a result upstream.
		Origin:        origin,
		Intermediate:  origin == turnOriginMailbox && callerTeamOf(initial) != "",
		Attempt:       flowAttempt,
		RecoveryOf:    recoveryOf,
		Input:         input,
		ContextBlocks: blocks,
		Status:        types.TurnRunning,
		StartedAt:     time.Now().UTC(),
	}
	// 用户回合以本轮开始时间为界；被唤起的回合由调用方给出邮箱到达时间。
	// 早于该时间的记录回答的是上一个问题，不能当作本轮已确认的事实。
	if scopeSince.IsZero() {
		scopeSince = turn.StartedAt
	}
	if err := r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventFlowSessionUpdated,
			FlowSessionID: session.ID,
			FlowTurnID:    turn.ID,
		},
		Payload: map[string]any{"session": session},
	}); err != nil {
		return types.FlowTurnResult{}, err
	}
	if err := r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventFlowTurnStarted,
			FlowSessionID: session.ID,
			FlowTurnID:    turn.ID,
			Attempt:       turn.Attempt,
			RecoveryOf:    turn.RecoveryOf,
		},
		Payload: map[string]any{
			"turn":  turn,
			"input": input,
		},
	}); err != nil {
		return types.FlowTurnResult{}, err
	}
	// Info, not debug: these two lines are the join key between the process log
	// and the session's event log. Without them a deployment running at the
	// default level writes nothing that names the session, so "why did this turn
	// hang" can only be answered by reading jsonl and matching timestamps by eye.
	// No input text is logged — the id plus the outcome is what a reader needs.
	logging.Info("flow turn started", map[string]any{
		"flow_session_id": session.ID,
		"flow_turn_id":    turn.ID,
		"origin":          turn.Origin,
		"attempt":         turn.Attempt,
	})

	result := types.FlowTurnResult{
		Session: session,
		Turn:    turn,
	}
	attempts := make(map[string]int)
	queue := make([]activation, 0, len(initial))
	enqueue := func(next activation) {
		if next.attempt <= 0 {
			attempts[next.teamID]++
			next.attempt = attempts[next.teamID]
		} else if next.attempt > attempts[next.teamID] {
			attempts[next.teamID] = next.attempt
		}
		queue = append(queue, next)
	}
	// activators remembers, per Team, who activated it. Only an activate route
	// (or the turn's initial activation) sets it; a return or an on_proceed
	// route does not, so a Team keeps the same activator however often it is
	// reached again.
	activators := make(map[string]string)
	for _, initialActivation := range initial {
		initialActivation.activator = initialActivation.callerTeam
		activators[initialActivation.teamID] = initialActivation.activator
		enqueue(initialActivation)
	}
	routeTo := func(target, from string) activation {
		return activation{teamID: target, callerTeam: from, activator: activators[target]}
	}
	completedTeams := make(map[string]bool)
	failedTeams := make(map[string]bool)
	coordinateRetries := make(map[string]int)
	// asyncDispatched records whether this turn handed work to the background
	// mailbox dispatcher, and the IDs of the work messages it posted. It only
	// changes the end-of-turn bookkeeping (an extra batch-dispatched event);
	// the terminal status stays waiting_input, because a dispatch turn is
	// meant to end and accept further input.
	asyncDispatched := false
	asyncMessageIDs := make([]string, 0)
	// recordDispatched emits the batch-dispatched fact at most once per turn.
	// The dispatch itself happened earlier in the turn and is already on
	// record; this event is only its end-of-turn bookkeeping, so it must be
	// written at every normal exit but never twice along the fall-through path
	// (queue-empty → loop exit). Failure exits deliberately skip it along with
	// the rest of the turn bookkeeping: a turn that ends failed does not
	// complete its trailing write-out, matching finishTurn's own discipline.
	dispatchedRecorded := false
	recordDispatched := func() {
		if asyncDispatched && !dispatchedRecorded {
			r.recordBatchDispatched(ctx, session, turn, asyncMessageIDs)
			dispatchedRecorded = true
		}
	}
	allRecords, err := r.loadHistoricalFlowRecords(ctx, session.ID)
	if err != nil {
		logging.Error("load historical flow records", map[string]any{
			"flow_session_id": session.ID,
			"flow_turn_id":    turn.ID,
			"error":           err.Error(),
		})
		return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionFailed, err)
	}
	teamTurns := 0
	// finalReply is the reply of the hop that ends this turn: the Team whose
	// route leaves nobody to hand the result to. result.Reply concatenates
	// every hop (an intermediate Team's raw output included), so a caller
	// that relayed it would show the user a RepoFacts JSON blob instead of
	// the answer the entry Team wrote for them.
	//
	// fromEntry distinguishes "the entry Team answered" from "a dispatched
	// Team ran out of things to hand off": both end the turn, but only the
	// first is an answer. A mailbox turn can start with an activator and
	// still end at the entry Team, which makes it the user-facing hop.
	recordFinalReply := func(reply string, fromEntry bool, teamID string) {
		if strings.TrimSpace(reply) != "" {
			result.FinalReply = reply
			result.FinalReplyFromEntry = fromEntry
			result.FinalReplyTeam = teamID
		}
	}

	// Wait decisions accumulate across batches instead of being re-declared per
	// batch. A Team that parks waiting for a Tool task or an approval keeps
	// that state even when a later batch ends waiting for the user: the Tool
	// task is still running, and only waiting_tool makes its completion
	// callback resume the session. Per-batch locals let the last batch's
	// decision overwrite the earlier one, so the session reported
	// waiting_input while a Tool task was still outstanding.
	hasWaitInput := false
	hasWaitTool := false
	hasWaitApproval := false

	// Tracked only so a tripped Team-turn backstop can say who looped. A bare
	// count leaves the reader grepping the event stream for the cycle, and
	// the moment the backstop fires is the moment that is hardest to do.
	activationCounts := make(map[string]int)
	var recentActivations []string

	for len(queue) > 0 {
		if err := contextErr(ctx); err != nil {
			return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionFailed, err)
		}

		ready, blocked := takeReadyActivations(queue, completedTeams, failedTeams, defs.Flow)
		ready, deferred := limitActivations(ready, r.limits.WithDefaults().MaxParallelTeams)
		queue = append(deferred, blocked...)
		if len(ready) == 0 {
			// Activations whose dependencies failed are intentionally
			// discarded. Their work cannot be executed with valid inputs;
			// the Team that activated the failed one receives the original
			// TeamFailureReport instead. Do not turn this normal failure propagation into a
			// generic "activation graph cannot make progress" error.
			if len(queue) == 0 {
				break
			}
			progressErr := errors.New("flow activation graph cannot make progress")
			logging.Error("flow activation graph cannot make progress", map[string]any{
				"flow_session_id": session.ID,
				"flow_turn_id":    turn.ID,
				"error":           progressErr.Error(),
			})
			return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionFailed, progressErr)
		}
		// Per-turn count: teamTurns accumulated inside this FlowTurn only.
		// The same limit is counted over the whole session on the mailbox
		// path (scheduler.runMessageTurn), because there every round is a
		// separate turn and a per-turn counter would never trip.
		for _, act := range ready {
			pair := activationPair(act)
			activationCounts[pair]++
			recentActivations = append(recentActivations, pair)
		}
		if len(recentActivations) > recentActivationWindow {
			recentActivations = recentActivations[len(recentActivations)-recentActivationWindow:]
		}
		if teamTurns+len(ready) > r.limits.MaxTeamTurns {
			limitErr := &limitError{
				Name:     "max_team_turns",
				Value:    r.limits.MaxTeamTurns,
				Observed: teamTurns + len(ready),
				TopPairs: topActivationPairs(activationCounts),
				Recent:   recentActivations,
				Detail:   "usually a routing cycle; raise settings.json runtime.max_team_turns only if the hand-offs are legitimate",
			}
			logging.Error("flow turn exceeded max team turns", map[string]any{
				"flow_session_id": session.ID,
				"flow_turn_id":    turn.ID,
				"activated":       limitErr.Observed,
				"top_pairs":       limitErr.TopPairs,
				"recent":          limitErr.Recent,
				"error":           limitErr.Error(),
			})
			return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionFailed, limitErr)
		}
		teamTurns += len(ready)

		executions := make([]teamExecution, len(ready))
		var wg sync.WaitGroup
		var firstErr error
		var errMu sync.Mutex
		for i, current := range ready {
			wg.Add(1)
			go func(index int, current activation) {
				defer wg.Done()
				execution, executionErr := r.executeTeamTurn(
					ctx,
					defs,
					session,
					turn,
					current,
					input,
					allRecords,
					scopeSince,
					blocks,
				)
				executions[index] = execution
				if executionErr != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = executionErr
					}
					errMu.Unlock()
				}
			}(i, current)
		}
		wg.Wait()
		if firstErr != nil {
			logging.ErrorUnlessCancelled(firstErr, "team turn execution failed", map[string]any{
				"flow_session_id": session.ID,
				"flow_turn_id":    turn.ID,
			})
			return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionFailed, firstErr)
		}

		for _, execution := range executions {
			teamResult := execution.result
			result.TeamResults = append(result.TeamResults, teamResult)
			result.PendingToolTasks = append(result.PendingToolTasks, teamResult.PendingToolTasks...)
			result.PendingApprovals = append(result.PendingApprovals, teamResult.PendingApprovals...)
			result.Reply = joinReply(result.Reply, teamResult.Reply)
			allRecords = append(allRecords, teamResult.Records...)
			result.Records = append(result.Records, teamResult.Records...)
			if teamResult.Error == "" && teamResult.Turn.Status != types.TurnFailed {
				completedTeams[execution.activation.teamID] = true
			} else {
				failedTeams[execution.activation.teamID] = true
			}
			if teamFailure := teamFailureRecord(turn, execution); teamFailure != nil {
				// A failed intermediate Team is a collaboration result, not
				// an automatic replay request. Publish the failure as a
				// Flow-scope record so the Team that activated it can read it
				// together with the successful sibling Teams' records.
				allRecords = append(allRecords, *teamFailure)
				result.Records = append(result.Records, *teamFailure)
				if err := r.publishFlowRecord(ctx, session, turn, *teamFailure); err != nil {
					return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionFailed, err)
				}
			}
		}

		// Resolve all routes after the batch has finished. A parallel batch
		// must not terminate on the first branch's wait decision and
		// accidentally discard sibling results.
		var routeFailure error
		var resolvedRoute *types.Route
		// Several sibling Teams can report to the same Team in one batch. A Team
		// that has an activator (or is the entry) is an aggregation point: it is
		// routed once per batch and sees every sibling's records, including
		// TeamFailureReport, instead of waking once per sibling.
		batchRoutes := make(map[string]struct{})
		enqueueBatchRoute := func(next activation) {
			key := next.teamID + "\x00" + next.callerTeam
			if _, aggregates := activators[next.teamID]; aggregates {
				key = next.teamID
			}
			if _, exists := batchRoutes[key]; exists {
				return
			}
			batchRoutes[key] = struct{}{}
			enqueue(next)
		}
		for _, execution := range executions {
			next := execution.next
			if next == nil {
				next = &types.Route{Action: types.NextProceed}
			}
			resolvedRoute = next
			if execution.result.Turn.Status == types.TurnWaitingInput {
				// The Team paused mid-execution for user input. Do not route
				// it onward; the FlowTurn ends resumable instead.
				hasWaitInput = true
				recordFinalReply(execution.result.Reply, execution.activation.activator == "", execution.activation.teamID)
				continue
			}
			switch kind, known := classifyRoute(next.Action); {
			case !known:
				if routeFailure == nil {
					routeFailure = fmt.Errorf("unsupported next action %q", next.Action)
				}
			case kind == routeForward:
				targets := fixedTargets(execution.binding)
				if len(targets) == 0 {
					// With no fixed route the result goes back to whoever activated this Team.
					// The entry has no activator, so the FlowTurn ends and waits for the user.
					if execution.activation.activator == "" {
						hasWaitInput = true
						recordFinalReply(execution.result.Reply, execution.activation.activator == "", execution.activation.teamID)
						continue
					}
					targets = []string{execution.activation.activator}
				}
				for _, target := range targets {
					enqueueBatchRoute(routeTo(target, execution.activation.teamID))
				}
			case kind == routeRetiredActivate:
				// Collaboration is an asynchronous tool now: a Team hands work to another
				// Team by calling the Team tool, which returns a task handle and delivers the
				// answer as a tool result.
				routeFailure = fmt.Errorf(
					"team %q asked to activate %v: routing dispatch is retired, call the Team tool instead",
					execution.activation.teamID, next.Teams)
				continue
			case kind == routeReturn:
				target := next.CallerTeam
				if target == "" {
					target = execution.activation.activator
				}
				if target == "" {
					hasWaitInput = true
					recordFinalReply(execution.result.Reply, execution.activation.activator == "", execution.activation.teamID)
					continue
				}
				enqueueBatchRoute(routeTo(target, execution.activation.teamID))
			case kind == routeCoordinate:
				sourceTeam := execution.activation.teamID
				if coordinateRetries[sourceTeam] >= r.limits.MaxCoordinateRetries {
					routeFailure = fmt.Errorf(
						"team %q exceeded coordinate retry limit (%d): %s",
						sourceTeam,
						r.limits.MaxCoordinateRetries,
						next.Reason,
					)
					continue
				}
				coordinateRetries[sourceTeam]++
				// Hand the decision back to whoever activated this Team; with no activator
				// the user is the one to decide.
				if execution.activation.activator == "" {
					hasWaitInput = true
					recordFinalReply(execution.result.Reply, execution.activation.activator == "", execution.activation.teamID)
					continue
				}
				enqueueBatchRoute(routeTo(execution.activation.activator, execution.activation.teamID))
			case kind == routeStop:
				switch next.Action {
				case types.NextWaitTool:
					hasWaitTool = true
				case types.NextWaitApproval:
					hasWaitApproval = true
				default:
					// Legacy lifecycle values (complete / wait_input) describe the Agent
					// finishing, not an orchestration step. The turn ends on this hop, so its
					// reply is the answer.
					recordFinalReply(execution.result.Reply, execution.activation.activator == "", execution.activation.teamID)
				}
			case kind == routeFail:
				if routeFailure == nil {
					routeFailure = errors.New(next.Reason)
				}
			}
		}
		if routeFailure != nil {
			// A cancelled call surfaces here as a route failure whose reason is
			// text ("call \"x\": llm chat: context canceled"), so the cancel has to
			// be recognised by text or it is recorded as an incident.
			logging.ErrorUnlessCancelled(routeFailure, "flow route failure", map[string]any{
				"flow_session_id": session.ID,
				"flow_turn_id":    turn.ID,
			})
			status := types.SessionFailed
			if logging.IsCancellation(routeFailure) {
				status = types.SessionCancelled
			}
			return r.finishTurn(ctx, withDeliveries(result, deliveries), status, routeFailure)
		}
		result.Turn.Next = resolvedRoute
		if len(queue) == 0 {
			result.Turn.RecordIDs = recordIDs(result.Records)
			// A turn that dispatched work into the background records that
			// fact before finishing. The terminal status is deliberately not
			// changed: a dispatch turn ends waiting_input like any other, so
			// the session stays continuable. Recording here as well as after
			// the loop is safe: the guard makes it idempotent, so a wait-case
			// exit records it once and a plain fall-through records it once.
			recordDispatched()
			if pendingFailure != nil && !hasWaitApproval && !hasWaitTool {
				// The work this turn was woken to collect did not happen. Ending
				// "waiting_input" with the model's narration would tell the user
				// the request succeeded; the turn ends failed and the answer slot
				// stays empty, with the reason in Error.
				// Replace the model's narration with a sentence that states the
				// failure. Leaving it empty would make the failure live only in
				// the error field, which a client that renders replies never
				// shows.
				result.Reply = failureReply(pendingFailure)
				result.FinalReply = result.Reply
				logging.Error("flow turn failed on dispatched work", map[string]any{
					"flow_session_id": session.ID,
					"flow_turn_id":    turn.ID,
					"error":           pendingFailure.Error(),
				})
				return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionFailed, pendingFailure)
			}
			// A dispatch the engine refused or that failed is a failed request,
			// even when the model narrates it as done. Marking the turn failed is
			// what lets a client show the reason instead of a confident summary of
			// work that never ran. Only when nothing at all succeeded: a retry that
			// worked is a success with a failed attempt behind it.
			if !hasWaitApproval && !hasWaitTool {
				if reasons, noneSucceeded := failedTeamCalls(result.TeamResults); noneSucceeded && len(reasons) > 0 {
					failure := fmt.Errorf("dispatched work did not run: %s", strings.Join(reasons, "; "))
					result.Reply = failureReply(failure)
					result.FinalReply = result.Reply
					logging.Error("flow turn failed on dispatched work", map[string]any{
						"flow_session_id": session.ID,
						"flow_turn_id":    turn.ID,
						"error":           failure.Error(),
					})
					return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionFailed, failure)
				}
			}
			switch {
			case hasWaitApproval:
				return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionWaitingApproval, nil)
			case hasWaitTool:
				return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionWaitingTool, nil)
			case hasWaitInput:
				return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionIdle, nil)
			}
		}
	}

	result.Turn.RecordIDs = recordIDs(result.Records)
	recordDispatched()
	// A normally finished FlowTurn stays continuable: the session lifecycle
	// is a runtime decision, not a Flow configuration.
	return r.finishTurn(ctx, withDeliveries(result, deliveries), types.SessionIdle, nil)
}

// callerTeamOf returns the caller that dispatched the work a wake-up drains,
// or "" when the wake-up starts at a Team nobody activated (the entry Team).
func callerTeamOf(initial []activation) string {
	for _, a := range initial {
		if a.callerTeam != "" {
			return a.callerTeam
		}
	}
	return ""
}

type activation struct {
	teamID string
	// callerTeam is the Team whose routing triggered this activation. It
	// changes every time the Team is reached again, including when a Team it
	// activated returns to it.
	callerTeam string
	// activator is the Team that activated teamID. It is fixed when teamID is
	// activated and is where the Team's result, failure and escalation go. Empty
	// means nothing activated it: it is the Flow's entry, so there is no one to
	// return to.
	activator  string
	force      bool
	attempt    int
	recoveryOf string
	// sessionSeq is the per_task instance ordinal. 0 means the resident
	// session, so a route activation (which carries no ord) cannot silently
	// run a Team whose binding asks for one instance per task.
	sessionSeq int
	// taskKey and taskContextID are the task identity of a per_task dispatch.
	// They are empty for resident activations.
	taskKey       string
	taskContextID string
	// taskDepth is the nesting depth inside the task context (resident
	// dispatchers start at 1).
	taskDepth int
	// authorization is the frozen permission of the task (nil for resident
	// sessions and for tasks dispatched without a grant).
	authorization *types.AuthorizationGrant
	// subscriptions are the sources whose events belong to this task.
	subscriptions []string
	// dispatchCount counts this round; reopenCount counts how often a closed
	// instance was reused. Both are bookkeeping for the caller, not gates.
	dispatchCount int
	reopenCount   int
	reopened      bool
	// dispatchedBy is the instance that handed this task over. Ownership is
	// fixed to it: continue/close/reopen are refused for everybody else.
	dispatchedBy       *types.TeamSessionRef
	resumeCallID       string
	resumeCheckpointID string
	resumeInput        string
	resumeCallTurnID   string
	resumeTaskID       string
	resumeApprovalID   string
	resumeApproval     *types.HITLResponse
	resumeResults      map[string]types.CallResult
	resumeCalls        map[string]types.TeamCallResume
}

type teamExecution struct {
	activation activation
	binding    types.FlowTeamBinding
	session    types.TeamSession
	result     types.TeamTurnResult
	next       *types.Route
}

func takeReadyActivations(
	queue []activation,
	completed map[string]bool,
	failed map[string]bool,
	flow types.Flow,
) ([]activation, []activation) {
	ready := make([]activation, 0, len(queue))
	blocked := make([]activation, 0, len(queue))
	for _, current := range queue {
		binding, ok := flow.Teams[current.teamID]
		if current.force || !ok {
			// A forced activation is an explicit resume/recovery request and
			// is allowed to bypass dependency state. Ordinary queued work is
			// never allowed to run after a failed dependency.
			ready = append(ready, current)
			continue
		}
		if dependenciesFailed(current.teamID, failed, flow) {
			continue
		}
		if dependenciesCompleted(binding.DependsOn, completed) {
			ready = append(ready, current)
			continue
		}
		blocked = append(blocked, current)
	}
	return ready, blocked
}

func dependenciesFailed(teamID string, failed map[string]bool, flow types.Flow) bool {
	visiting := make(map[string]bool)
	var visit func(string) bool
	visit = func(current string) bool {
		if failed[current] {
			return true
		}
		if visiting[current] {
			return false
		}
		visiting[current] = true
		defer delete(visiting, current)

		binding, ok := flow.Teams[current]
		if !ok {
			return false
		}
		for _, dependency := range binding.DependsOn {
			if visit(dependency) {
				return true
			}
		}
		return false
	}
	return visit(teamID)
}

func limitActivations(items []activation, max int) ([]activation, []activation) {
	if max <= 0 || len(items) <= max {
		return items, nil
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].teamID == items[j].teamID {
			return items[i].attempt < items[j].attempt
		}
		return items[i].teamID < items[j].teamID
	})
	return items[:max], items[max:]
}

// executeTeamTurn resolves one activation to a Team and runs it. defs is the
// turn's snapshot: it supplies both the flow binding and the Team definition,
// so the routing that put this activation on the queue and the Team it now
// runs are guaranteed to come from the same generation. This runs inside the
// turn's parallel batch, so it must not re-read the store.
// teamSessionID derives a Team instance id. Resident sessions keep the
// historical two-part id (so existing transcripts and state files still
// resolve); per_task sessions append their ordinal, which is what makes two
// tasks of the same Team independent.
func teamSessionID(flowSessionID, teamID string, seq int) string {
	if seq > 0 {
		return fmt.Sprintf("%s:%s:%d", flowSessionID, teamID, seq)
	}
	return fmt.Sprintf("%s:%s", flowSessionID, teamID)
}

func (r *Runtime) executeTeamTurn(
	ctx context.Context,
	defs *types.Definitions,
	session types.FlowSession,
	flowTurn types.FlowTurn,
	current activation,
	input string,
	records []types.SharedRecord,
	scopeSince time.Time,
	blocks []types.ContextBlock,
) (teamExecution, error) {
	execution := teamExecution{activation: current}

	binding, ok := defs.Flow.Teams[current.teamID]
	if !ok {
		return execution, fmt.Errorf("team %q is not configured in flow", current.teamID)
	}
	execution.binding = binding

	team, ok := defs.Teams[binding.TeamID]
	if !ok {
		return execution, fmt.Errorf("team definition %q not found", binding.TeamID)
	}

	lifecycle := binding.EffectiveLifecycle()
	if lifecycle.IsPerTask() && current.sessionSeq <= 0 {
		return execution, fmt.Errorf(
			"team %q uses lifecycle per_task and must be dispatched as a task (Team tool with task_key), not activated by route",
			current.teamID)
	}
	if !lifecycle.IsPerTask() && current.sessionSeq > 0 {
		return execution, fmt.Errorf(
			"team %q is resident but the activation carried instance %d",
			current.teamID, current.sessionSeq)
	}
	teamSession := types.TeamSession{
		ID:               teamSessionID(session.ID, current.teamID, current.sessionSeq),
		FlowSessionID:    session.ID,
		TeamID:           current.teamID,
		TeamDefinitionID: binding.TeamID,
		SessionSeq:       current.sessionSeq,
		Lifecycle:        lifecycle,
		DispatchedBy:     current.dispatchedBy,
		TaskKey:          current.taskKey,
		TaskContextID:    current.taskContextID,
		TaskDepth:        current.taskDepth,
		Authorization:    current.authorization,
		Subscriptions:    current.subscriptions,
		DispatchCount:    current.dispatchCount,
		ReopenCount:      current.reopenCount,
		Status:           types.SessionRunning,
		CreatedAt:        session.CreatedAt,
		UpdatedAt:        time.Now().UTC(),
	}
	if lifecycle.IsPerTask() && teamSession.TaskContextID == "" {
		// A dispatched task roots its own task context; descendants inherit it.
		teamSession.TaskContextID = teamSession.ID
	}
	execution.session = teamSession
	teamTurn := types.TeamTurn{
		ID:            newID("tt"),
		FlowTurnID:    flowTurn.ID,
		TeamSessionID: teamSession.ID,
		TeamID:        current.teamID,
		Attempt:       current.attempt,
		RecoveryOf:    current.recoveryOf,
		CallerTeam:    current.callerTeam,
		Status:        types.TurnRunning,
		StartedAt:     time.Now().UTC(),
	}

	if err := r.appendTeamStartedEvents(ctx, session.ID, flowTurn, teamSession, teamTurn, team); err != nil {
		return execution, err
	}
	if err := r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventTeamTurnStarted,
			FlowSessionID: session.ID,
			FlowTurnID:    flowTurn.ID,
			TeamSessionID: teamSession.ID,
			TeamTurnID:    teamTurn.ID,
			TeamID:        teamTurn.TeamID,
			Attempt:       teamTurn.Attempt,
			RecoveryOf:    teamTurn.RecoveryOf,
		},
		Payload: map[string]any{
			"team_turn": teamTurn,
			"input":     teamInput(input, binding.Inputs),
		},
	}); err != nil {
		return execution, err
	}

	teamRecords := selectFlowRecordsForBinding(records, binding, scopeSince, teamSession.TaskContextID, flowTurn.ID)
	// Record the hand-off before the Team runs: the consumer is what a display
	// layer needs to draw "B → A" without repeating B's content. Writing it here
	// (not in the Team) keeps the edge a Flow fact — the Team only knows it
	// received records.
	for _, record := range teamRecords {
		if err := r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventSharedRecordConsumed,
				FlowSessionID: session.ID,
				FlowTurnID:    flowTurn.ID,
				TeamSessionID: teamSession.ID,
				TeamTurnID:    teamTurn.ID,
				TeamID:        binding.ID,
			},
			Payload: map[string]any{"record": record, "consumer_team": binding.ID},
		}); err != nil {
			return execution, err
		}
	}
	// The caller's outstanding tasks ride along as an injected block: it is
	// rebuilt every turn and sits in the part of the context compaction
	// protects, so a conversation that has been summarised away still knows
	// which tasks are outstanding and which owe a summary.
	//
	// The turn's origin travels with it. A session runs several turns per user
	// message, and every wake-up carries the same input text, so without this
	// the model has to infer "did the user speak again?" from whether someone
	// reported back. A wrong guess re-dispatches work already in flight.
	//
	// A fresh slice, not append: blocks belongs to the enclosing turn and must
	// not grow a block for every Team it reaches.
	teamBlocks := append([]types.ContextBlock(nil), blocks...)
	if origin := turnOriginText(flowTurn.Origin); origin != "" {
		teamBlocks = append(teamBlocks, types.ContextBlock{
			Kind: types.ContextBlockTurnOrigin, Text: origin, Source: "harness",
			Stability: "dynamic", Priority: 60, Compressible: false,
		})
	}
	if ledger := r.TaskLedger(ctx, session.ID, teamSession.ID); ledger != "" {
		teamBlocks = append(teamBlocks, types.ContextBlock{
			Kind: types.ContextBlockTaskLedger, Text: ledger, Source: "harness",
			Stability: "dynamic", Priority: 70, Compressible: false,
		})
	}
	teamResult, runErr := r.teams.Run(ctx, types.TeamTurnRequest{
		FlowSession:        session,
		FlowTurn:           flowTurn,
		TeamSession:        teamSession,
		TeamTurn:           teamTurn,
		Binding:            binding,
		Team:               team,
		Input:              teamInput(input, binding.Inputs),
		ContextBlocks:      teamBlocks,
		Records:            teamRecords,
		Limits:             r.limits,
		ResumeCallID:       current.resumeCallID,
		ResumeCheckpointID: current.resumeCheckpointID,
		ResumeInput:        current.resumeInput,
		ResumeCallTurnID:   current.resumeCallTurnID,
		ResumeTaskID:       current.resumeTaskID,
		ResumeApprovalID:   current.resumeApprovalID,
		ResumeApproval:     current.resumeApproval,
		ResumeResults:      current.resumeResults,
		ResumeCalls:        current.resumeCalls,
	})
	if runErr != nil {
		logging.ErrorUnlessCancelled(runErr, "team run failed", map[string]any{
			"flow_session_id": session.ID,
			"flow_turn_id":    flowTurn.ID,
			"team_id":         current.teamID,
			"team_turn_id":    teamTurn.ID,
		})
		teamResult.Error = runErr.Error()
		teamResult.Turn.Status = types.TurnFailed
	}

	// A failed Agent/Call makes its containing Team fail. Escalate that
	// failure to the Team that activated it exactly once; this is not an
	// automatic replay request. That Team can aggregate the failure with its
	// sibling Teams' results and decide what to do. A Team nobody activated
	// (the entry) has nobody to escalate to, so its failure ends the FlowTurn.
	teamFailed := runErr != nil ||
		teamResult.Error != "" ||
		teamResult.Turn.Status == types.TurnFailed ||
		(teamResult.Next != nil && teamResult.Next.Action == types.NextFail)
	if teamFailed {
		reason := strings.TrimSpace(teamResult.Error)
		if reason == "" && teamResult.Next != nil {
			reason = strings.TrimSpace(teamResult.Next.Reason)
		}
		if reason == "" {
			reason = "team execution failed"
		}
		if teamResult.Error == "" {
			teamResult.Error = reason
		}
		if current.activator == "" {
			teamResult.Next = &types.Route{Action: types.NextFail, Reason: reason}
		} else {
			teamResult.Next = &types.Route{Action: types.NextCoordinate, Reason: reason}
		}
		teamResult.Turn.Status = types.TurnFailed
	}

	next := teamResult.Next
	if next == nil {
		next = &types.Route{Action: types.NextProceed}
	}
	if next.Action == types.NextProceed &&
		binding.OnProceed != nil &&
		binding.OnProceed.Action != "" &&
		binding.OnProceed.Action != types.NextProceed {
		next = binding.OnProceed
	}
	now := time.Now().UTC()
	// Capture the Team runtime's turn status before replacing the TeamTurn
	// record below: the flow layer owns the record, but only the Team knows
	// whether it paused mid-execution (for example through the
	// AskUserQuestion Tool).
	teamTurnStatus := teamResult.Turn.Status
	teamResult.Next = next
	teamResult.Turn = teamTurn
	teamResult.Turn.Next = next
	waitingInput := teamTurnStatus == types.TurnWaitingInput
	waitingTool := teamTurnStatus == types.TurnWaitingTool ||
		next.Action == types.NextWaitTool
	waitingApproval := teamTurnStatus == types.TurnWaitingApproval ||
		next.Action == types.NextWaitApproval
	teamResult.Turn.Status = types.TurnCompleted
	if teamFailed {
		teamResult.Turn.Status = types.TurnFailed
	} else if waitingApproval {
		teamResult.Turn.Status = types.TurnWaitingApproval
	} else if waitingTool {
		teamResult.Turn.Status = types.TurnWaitingTool
	} else if waitingInput {
		teamResult.Turn.Status = types.TurnWaitingInput
	}
	if teamResult.Error != "" {
		teamResult.Turn.Status = types.TurnFailed
	}
	teamResult.Turn.RecordIDs = recordIDs(teamResult.Records)
	teamResult.Turn.FinishedAt = &now

	// A Team that finishes its turn while a call it dispatched is still running
	// has not concluded anything: the reply is a progress line ("handed it on,
	// waiting for the answer"). Publishing that as an active record is what let
	// "正在取证" travel as the current state two minutes after the work behind it
	// died (fs_xxx). The record is marked pending so no consumer
	// reads it as an answer, and resumeToolResult settles the same record when
	// the call behind it returns.
	//
	// The unit is the call, not the Team: a Team with two calls has two records,
	// and only the call that dispatched is a progress line. Downgrading every
	// record of the turn would throw away a sibling call's finished conclusion.
	pendingCalls := r.pendingCallIDs(ctx, session.ID, teamTurn.TeamID)

	// Written back through the index, not the range copy: the same slice is
	// appended to allRecords below, and the rest of this turn reads it. A copy
	// would leave the in-memory record active while the published one says
	// pending — the exact divergence that lets a progress line be consumed in
	// the turn that produced it.
	for index := range teamResult.Records {
		record := &teamResult.Records[index]
		if record.Kind == "agent_result" && record.Status == types.RecordActive &&
			callIsPending(pendingCalls, record.Producer.CallID) {
			record.Status = types.RecordPending
		}
		// A Team that ran as a sub-task produces its records for the conversation
		// that dispatched it. Its own scope says "team", so the caller's follow-up
		// turn — which reads flow scope and matches `flow_records` by name — would
		// find nothing, and no session.record.consumed edge would ever be written
		// (A→B fixture, 2026-10-28: caller declared flow_records: [WorkerReport],
		// received zero records, produced zero consumed events). Publish a
		// flow-scope copy so the hand-off is a Flow fact; the event below keeps
		// the record exactly as it was produced.
		publishScope := record.Scope
		if publishScope != types.RecordScopeFlow && teamTurn.CallerTeam != "" {
			publishScope = types.RecordScopeFlow
		}
		if publishScope == types.RecordScopeFlow && r.evidence != nil {
			published := *record
			published.Scope = publishScope
			if err := r.evidence.Publish(ctx, session.ID, published); err != nil {
				return execution, err
			}
		}
		if err := r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventSharedRecordPublished,
				FlowSessionID: session.ID,
				FlowTurnID:    flowTurn.ID,
				TeamSessionID: teamSession.ID,
				TeamTurnID:    teamTurn.ID,
			},
			Payload: map[string]any{"record": *record},
		}); err != nil {
			return execution, err
		}
	}

	// Same reason as finishTurn: a cancelled execution still has to record that
	// the TeamTurn ended. The recovery check pairs started/completed events, so a
	// TeamTurn with only a start makes the session "have unfinished execution",
	// and a session in that state refuses new input until it is recovered.
	ctx = writeEvenIfCancelled(ctx)

	eventType := types.EventTeamTurnCompleted
	if waitingApproval && teamResult.Error == "" {
		eventType = types.EventTeamTurnWaitingApproval
	} else if waitingTool && teamResult.Error == "" {
		eventType = types.EventTeamTurnWaitingTool
	} else if waitingInput && teamResult.Error == "" {
		eventType = types.EventTeamTurnWaitingInput
	}
	if err := r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          eventType,
			FlowSessionID: session.ID,
			FlowTurnID:    flowTurn.ID,
			TeamSessionID: teamSession.ID,
			TeamTurnID:    teamTurn.ID,
			TeamID:        teamTurn.TeamID,
			Attempt:       teamTurn.Attempt,
			RecoveryOf:    teamTurn.RecoveryOf,
		},
		Payload: map[string]any{"team_result": teamResult},
	}); err != nil {
		return execution, err
	}
	teamSession.Status = sessionStatusForTurn(teamResult.Turn.Status)
	teamSession.UpdatedAt = now
	if err := r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventTeamSessionUpdated,
			FlowSessionID: session.ID,
			FlowTurnID:    flowTurn.ID,
			TeamSessionID: teamSession.ID,
			TeamTurnID:    teamTurn.ID,
			Attempt:       teamTurn.Attempt,
			RecoveryOf:    teamTurn.RecoveryOf,
		},
		Payload: map[string]any{"team_session": teamSession},
	}); err != nil {
		return execution, err
	}

	execution.result = teamResult
	execution.next = next
	// A Team-level execution error is represented in TeamTurnResult so the
	// Flow can report it to the Team that activated it. It is not an automatic
	// replay request; only an explicit activate route may run work again.
	return execution, nil
}

// failureReply is what a failed turn says when the model narrated success
// instead. It names the failure so a client that only relays replies still
// tells the user something actionable.
func failureReply(err error) string {
	if err == nil {
		return ""
	}
	return "The request could not be completed: " + err.Error()
}

// failedTeamCalls reports why each Team call in this turn failed, and whether
// none of them succeeded. It returns noneSucceeded=false as soon as one call
// produced a result, so a turn that recovered from a refusal is not marked
// failed for the attempt that did not land.
func failedTeamCalls(results []types.TeamTurnResult) (reasons []string, noneSucceeded bool) {
	noneSucceeded = true
	for _, result := range results {
		failed := result.Turn.Status == types.TurnFailed || strings.TrimSpace(result.Error) != ""
		if !failed {
			noneSucceeded = false
			continue
		}
		reason := strings.TrimSpace(result.Error)
		if reason == "" && result.Next != nil {
			reason = strings.TrimSpace(result.Next.Reason)
		}
		if reason == "" {
			reason = "team execution failed"
		}
		if result.Turn.TeamID != "" {
			reason = result.Turn.TeamID + ": " + reason
		}
		reasons = append(reasons, reason)
	}
	if len(results) == 0 {
		noneSucceeded = false
	}
	return reasons, noneSucceeded
}

func (r *Runtime) publishFlowRecord(
	ctx context.Context,
	session types.FlowSession,
	flowTurn types.FlowTurn,
	record types.SharedRecord,
) error {
	if r.evidence != nil {
		if err := r.evidence.Publish(ctx, session.ID, record); err != nil {
			return err
		}
	}
	return r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventSharedRecordPublished,
			FlowSessionID: session.ID,
			FlowTurnID:    flowTurn.ID,
			TeamID:        record.Producer.TeamID,
			TeamTurnID:    record.Producer.TeamTurnID,
			CallID:        record.Producer.CallID,
			CallTurnID:    record.Producer.CallTurnID,
		},
		Payload: map[string]any{"record": record},
	})
}

func teamFailureRecord(flowTurn types.FlowTurn, execution teamExecution) *types.SharedRecord {
	teamResult := execution.result
	if teamResult.Error == "" && teamResult.Turn.Status != types.TurnFailed {
		return nil
	}
	// A Team nobody activated (the entry) has nobody to report to. Its
	// failure is terminal for this FlowTurn and is returned to the caller
	// directly rather than wrapped as another coordination record.
	if execution.activation.activator == "" {
		return nil
	}

	failedCalls := make([]string, 0)
	callErrors := make(map[string]string)
	for callID, callResult := range teamResult.CallResults {
		if callResult.Status != types.TurnFailed && callResult.Error == "" {
			continue
		}
		failedCalls = append(failedCalls, callID)
		if callResult.Error != "" {
			callErrors[callID] = callResult.Error
		}
	}
	sort.Strings(failedCalls)

	reason := strings.TrimSpace(teamResult.Error)
	if reason == "" && teamResult.Next != nil {
		reason = strings.TrimSpace(teamResult.Next.Reason)
	}
	if reason == "" {
		reason = "team execution failed"
	}

	data := map[string]any{
		"team_id":         execution.activation.teamID,
		"team_turn_id":    teamResult.Turn.ID,
		"status":          string(types.TurnFailed),
		"reason":          reason,
		"failed_calls":    failedCalls,
		"call_errors":     callErrors,
		"automatic_retry": false,
	}
	recordID := fmt.Sprintf(
		"team-failure-%s-%s",
		flowTurn.ID,
		execution.activation.teamID,
	)
	return &types.SharedRecord{
		RecordID: recordID,
		Kind:     "team_failure",
		Name:     "TeamFailureReport",
		Scope:    types.RecordScopeFlow,
		Producer: types.ProducerRef{
			FlowSessionID: flowTurn.FlowSessionID,
			FlowTurnID:    flowTurn.ID,
			TeamID:        execution.activation.teamID,
			TeamTurnID:    teamResult.Turn.ID,
			TeamSessionID: execution.session.ID,
			TaskContextID: recordTaskContextOf(execution.session),
		},
		Summary:   fmt.Sprintf("Team %q failed: %s", execution.activation.teamID, reason),
		Data:      data,
		Status:    types.RecordActive,
		Revision:  1,
		CreatedAt: time.Now().UTC(),
	}
}

// withDeliveries attaches the user-facing mailbox results to the turn result
// before the turn event is written, so callers relaying the event see them.
func withDeliveries(result types.FlowTurnResult, deliveries []types.UserDelivery) types.FlowTurnResult {
	if len(deliveries) > 0 {
		result.Deliveries = deliveries
	}
	return result
}

// deliveryPayload renders deliveries as plain maps so the event survives a JSONL
// round-trip without changing shape for the caller.
func deliveryPayload(deliveries []types.UserDelivery) []map[string]any {
	if len(deliveries) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(deliveries))
	for _, delivery := range deliveries {
		item := map[string]any{"text": delivery.Text}
		if delivery.TeamID != "" {
			item["team_id"] = delivery.TeamID
		}
		if delivery.MessageID != "" {
			item["message_id"] = delivery.MessageID
		}
		items = append(items, item)
	}
	return items
}

func (r *Runtime) finishTurn(
	ctx context.Context,
	result types.FlowTurnResult,
	status types.SessionStatus,
	runErr error,
) (types.FlowTurnResult, error) {
	// A cancelled turn reaches here through the very context Cancel cancelled.
	// The terminal event is the only record that the turn stopped, so it has to
	// be written on a context that is still alive.
	ctx = writeEvenIfCancelled(ctx)
	if logging.IsCancellation(runErr) {
		// Cancellation is not a failure of the work: it is the outcome the user
		// asked for, and reporting it as "failed" sends them looking for a bug.
		// The check is text-aware on purpose: a Team reports its reason as a
		// string, and a string cannot be unwrapped, so errors.Is alone let a
		// cancelled turn finish as failed.
		status = types.SessionCancelled
		runErr = errors.New("turn cancelled")
	}
	now := time.Now().UTC()
	result.Session.Status = sessionStatusAfterTurn(status)
	result.Session.UpdatedAt = now
	result.Turn.Status = turnStatusForSession(status)
	result.Turn.FinishedAt = &now
	if runErr != nil {
		result.Error = runErr.Error()
	}
	if status == types.SessionFailed {
		logging.ErrorUnlessCancelled(errors.New(result.Error), "flow turn failed", map[string]any{
			"flow_session_id": result.Session.ID,
			"flow_turn_id":    result.Turn.ID,
		})
	}
	eventType := types.EventFlowTurnCompleted
	switch status {
	case types.SessionIdle:
		eventType = types.EventFlowTurnWaitingInput
	case types.SessionWaitingTool:
		eventType = types.EventFlowTurnWaitingTool
	case types.SessionWaitingApproval:
		eventType = types.EventFlowTurnWaitingApproval
	}
	// A caller relaying this turn needs the answer, not every intermediate
	// hop concatenated: FinalReply is the hop that ended the turn. Fall back
	// to the concatenation for turns that never reached that hop (a failure
	// before any route resolved).
	replyForCaller := result.Reply
	if strings.TrimSpace(result.FinalReply) != "" {
		replyForCaller = result.FinalReply
	}
	// Intermediate was decided when the turn started, from the activator of
	// its first hop. A mailbox turn may start with an activator and still end
	// at the entry Team — that final hop answers the user, so the turn is not
	// an intermediate step after all.
	if result.FinalReplyFromEntry {
		result.Turn.Intermediate = false
	}
	// A turn that ends while a Team it dispatched is still running is a progress
	// report, not an answer: the result coming back is where the user-visible
	// conclusion appears. Saying so keeps a client from presenting "已转给执行侧…"
	// as if it were the outcome — the shape that made multi-hop work look like
	// three repeated non-answers.
	if status == types.SessionIdle && r.hasPendingTeamCall(ctx, result.Session.ID) {
		result.Turn.Intermediate = true
	}
	// An intermediate turn has no answer, so it must not carry one in *any* shape.
	// Clearing only the event payload left the JSON-RPC response still holding
	// the progress line — that is the copy s20's client rendered as a message.
	if result.Turn.Intermediate {
		result.Reply = ""
		result.FinalReply = ""
		// Each Team's reply is a copy of the same progress line. Keeping them
		// "for diagnostics" is what let the text escape three times; the turn and
		// team records still carry it for anyone debugging from the event stream.
		for index := range result.TeamResults {
			result.TeamResults[index].Reply = ""
		}
		// A wake-up exists to tell the user what changed, so its text is a
		// delivery even when the turn is intermediate. Without this the one
		// message the feature is for is cleared by the rule above: the turn is
		// intermediate precisely because the downstream Team it is checking on
		// is still running.
		if result.Turn.Origin == turnOriginWakeup && strings.TrimSpace(replyForCaller) != "" {
			result.Deliveries = append(result.Deliveries, types.UserDelivery{
				MessageID: result.Turn.ID,
				TeamID:    result.FinalReplyTeam,
				Text:      truncateDelivery(replyForCaller, maxDeliveryChars),
			})
		}
	}
	payload := map[string]any{
		"session": result.Session,
		"turn":    result.Turn,
		"error":   result.Error,
		// reply and origin are hoisted to the top level because a caller
		// relaying the answer of an unattended turn reads them without
		// decoding the turn record.
		"reply":  replyPayloadField(replyForCaller, result.Turn.Intermediate),
		"origin": result.Turn.Origin,
		// origin says how the turn was woken (a mailbox message), not who
		// wrote the text. Consumers attributing a message to its producer
		// must read team.
		"team": result.FinalReplyTeam,
		// Contributors is the turn's provenance: which Team said what, and
		// which Team asked it. A reply written by the coordinating Team is a
		// restatement of other Teams' results; without this a consumer can
		// only see the writer and has to guess the source.
		"contributors": contributorPayload(result.TeamResults, result.Turn.Intermediate),
		"intermediate": result.Turn.Intermediate,
		// Deliveries travel with the turn event: they are results the user
		// must see as their own messages, independent of the reply above.
		"deliveries": deliveryPayload(result.Deliveries),
	}
	// A tripped backstop carries structured facts alongside its message, so a
	// reader can mark the turn, highlight the looping edges, or count how often
	// a limit fires without parsing the error string back into fields.
	var limit *limitError
	if errors.As(runErr, &limit) {
		payload["limit"] = limit.Payload()
	}
	if err := r.appendSessionEvent(ctx, result.Session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          eventType,
			FlowSessionID: result.Session.ID,
			FlowTurnID:    result.Turn.ID,
		},
		Payload: payload,
	}); err != nil {
		return result, err
	}
	// Pairs with "flow turn started": the two lines let a reader follow any turn
	// from the process log alone, at the level a deployment actually runs.
	logging.Info("flow turn finished", map[string]any{
		"flow_session_id": result.Session.ID,
		"flow_turn_id":    result.Turn.ID,
		"status":          string(result.Turn.Status),
		"team":            result.FinalReplyTeam,
		"intermediate":    result.Turn.Intermediate,
		"error":           result.Error,
		"duration_ms":     now.Sub(result.Turn.StartedAt).Milliseconds(),
	})
	// A message that arrived while this turn was running is waiting in the
	// mailbox. Start it now, in its own goroutine: this turn still holds the
	// caller's lock, and running it inline would either recurse or deadlock.
	go r.drainQueuedInput(context.Background(), result.Session.ID)
	return result, runErr
}

// contributorPayload lists what each Team in the turn said, in completion order.
// Replies are capped: this is provenance for display, not a transcript.
//
// CallerTeam is deliberately not included. It is whatever the Team wrote into
// its route as "return the result here", filled by the model and often naming
// the Team it just called — so it reads as both upstream and downstream
// depending on the turn. Exposing it as lineage would state a relationship the
// engine never recorded. The activation edge is engine-owned and belongs in
// TeamTurn if it is ever needed.
func contributorPayload(results []types.TeamTurnResult, intermediate bool) []map[string]any {
	// An intermediate turn has no answer to attribute. Its contributors carry the
	// same progress lines as its reply — s20's payload had reply="" and still
	// shipped "已转给执行侧…" here, which is all a client needs to render it.
	if intermediate {
		return nil
	}
	items := make([]map[string]any, 0, len(results))
	for _, result := range results {
		reply := strings.TrimSpace(result.Reply)
		if reply == "" {
			continue
		}
		items = append(items, map[string]any{
			"team":  result.Turn.TeamID,
			"reply": truncateRunes(reply, 300),
		})
	}
	return items
}

// truncateRunes caps text by rune count, marking the cut.
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// runningTeams lists Teams of this session that are still executing in this
// process: started, never finished, and present in the runtime's busy set.
// A failed turn may have launched an async Team that keeps running after the
// turn itself failed. Starting a new turn then would stack a second turn on top
// of results still being produced, so continuation waits for them to drain —
// a transient condition, unlike the terminal session status this call no longer
// treats as blocking.
func (r *Runtime) runningTeams(ctx context.Context, sessionID string) ([]string, error) {
	if r.sessions == nil {
		return nil, nil
	}
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	live := r.liveIndexFor(sessionID)
	// Per instance, not per Team: one finished per_task instance must not make
	// a still-running sibling look finished (and the reverse).
	started := make(map[string]bool)
	finished := make(map[string]bool)
	teamOf := make(map[string]string)
	for _, event := range replay.Events {
		key := event.TeamSessionID
		if key == "" {
			key = "team:" + event.TeamID
		}
		switch event.Type {
		case types.EventTeamTurnStarted:
			started[key] = true
			teamOf[key] = event.TeamID
		case types.EventTeamTurnCompleted,
			types.EventTeamTurnWaitingInput,
			types.EventTeamTurnWaitingTool,
			types.EventTeamTurnWaitingApproval:
			finished[key] = true
		}
	}
	seen := make(map[string]bool)
	running := make([]string, 0, len(started))
	for key := range started {
		if finished[key] {
			continue
		}
		liveNow := live.sessions[key]
		if strings.HasPrefix(key, "team:") {
			liveNow = live.teams[teamOf[key]]
		}
		if !liveNow {
			continue
		}
		team := teamOf[key]
		if team == "" || seen[team] {
			continue
		}
		seen[team] = true
		running = append(running, team)
	}
	sort.Strings(running)
	return running, nil
}

func (r *Runtime) loadSession(ctx context.Context, sessionID string) (types.FlowSession, error) {
	if r.sessions == nil {
		return types.FlowSession{}, errors.New("session writer is nil")
	}
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return types.FlowSession{}, err
	}
	var session types.FlowSession
	for _, event := range replay.Events {
		if event.Payload == nil {
			continue
		}
		if raw, ok := event.Payload["session"]; ok {
			if err := decodeValue(raw, &session); err != nil {
				return types.FlowSession{}, err
			}
		}
	}
	if session.ID == "" {
		return types.FlowSession{}, fmt.Errorf("session %q has no session state", sessionID)
	}
	return session, nil
}

func (r *Runtime) appendSessionEvent(ctx context.Context, sessionID string, event storage.SessionEvent) error {
	if r.sessions == nil {
		return errors.New("session writer is nil")
	}
	_, err := r.sessions.Append(ctx, sessionID, storage.LayerFlow, event)
	return err
}

func (r *Runtime) loadHistoricalFlowRecords(ctx context.Context, sessionID string) ([]types.SharedRecord, error) {
	if r.evidence == nil {
		return nil, nil
	}
	records, err := r.evidence.List(ctx, sessionID, types.RecordScopeFlow)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	return records, err
}

func (r *Runtime) appendTeamStartedEvents(
	ctx context.Context,
	sessionID string,
	flowTurn types.FlowTurn,
	teamSession types.TeamSession,
	teamTurn types.TeamTurn,
	team types.Team,
) error {
	if err := r.appendSessionEvent(ctx, sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventTeamSessionCreated,
			FlowSessionID: flowTurn.FlowSessionID,
			FlowTurnID:    flowTurn.ID,
			TeamSessionID: teamSession.ID,
			TeamTurnID:    teamTurn.ID,
		},
		Payload: map[string]any{"team_session": teamSession},
	}); err != nil {
		return err
	}
	return nil
}

func sessionStatusForTurn(status types.TurnStatus) types.SessionStatus {
	switch status {
	case types.TurnCompleted:
		return types.SessionCompleted
	case types.TurnWaitingInput:
		return types.SessionIdle
	case types.TurnWaitingTool:
		return types.SessionWaitingTool
	case types.TurnCancelled:
		return types.SessionCancelled
	case types.TurnFailed:
		return types.SessionFailed
	case types.TurnWaitingApproval:
		return types.SessionWaitingApproval
	default:
		return types.SessionRunning
	}
}

// interruptedExecutions reports the executions the event stream shows as
// started but never finished. liveTeams names the Teams executing in this
// process right now: their team_turns (and the calls under them) look identical
// to a crash's leftovers — started with no completion — but they are still
// running, so they must not be reported as interrupted. Without this, a
// status query during a dispatched Team's round would mark its session
// interrupted and refuse further input.
// liveIndex answers "is this execution still live?" for both current events
// (keyed by TeamSessionID) and events written before instances existed (no
// session id, keyed by TeamID).
type liveIndex struct {
	sessions map[string]bool
	teams    map[string]bool
}

func (l liveIndex) running(event storage.SessionEvent) bool {
	if event.TeamSessionID != "" {
		return l.sessions[event.TeamSessionID]
	}
	return l.teams[event.TeamID]
}

func (r *Runtime) liveIndexFor(sessionID string) liveIndex {
	return liveIndex{
		sessions: r.liveTeamSessionsFor(sessionID),
		teams:    r.liveTeamsFor(sessionID),
	}
}

func interruptedExecutions(events []storage.SessionEvent, live liveIndex) []types.InterruptedExecution {
	type started struct {
		event storage.SessionEvent
		input string
	}
	startedCalls := make(map[string]started)
	startedTeams := make(map[string]started)
	startedFlows := make(map[string]started)
	completedCalls := make(map[string]bool)
	completedTeams := make(map[string]bool)
	completedFlows := make(map[string]bool)
	waitingCalls := make(map[string]bool)
	waitingTeams := make(map[string]bool)
	waitingFlows := make(map[string]bool)
	resolvedCalls := make(map[string]bool)
	resolvedTeams := make(map[string]bool)
	resolvedFlows := make(map[string]bool)

	for _, event := range events {
		switch event.Type {
		case types.EventFlowTurnStarted:
			startedFlows[event.FlowTurnID] = started{event: event, input: payloadString(event.Payload, "input")}
		case types.EventFlowTurnCompleted:
			completedFlows[event.FlowTurnID] = true
		case types.EventFlowTurnWaitingInput, types.EventFlowTurnWaitingTool, types.EventFlowTurnWaitingApproval:
			// A FlowTurn that ended waiting is not interrupted; it stays
			// resumable by design.
			waitingFlows[event.FlowTurnID] = true
		case types.EventTeamTurnStarted:
			startedTeams[event.TeamTurnID] = started{event: event, input: payloadString(event.Payload, "input")}
		case types.EventTeamTurnCompleted:
			completedTeams[event.TeamTurnID] = true
		case types.EventTeamTurnWaitingInput, types.EventTeamTurnWaitingTool, types.EventTeamTurnWaitingApproval:
			waitingTeams[event.TeamTurnID] = true
			waitingFlows[event.FlowTurnID] = true
		case types.EventAgentTurnStarted, types.EventCommandTurnStarted, types.EventWebhookTurnStarted:
			startedCalls[event.CallTurnID] = started{event: event, input: payloadString(event.Payload, "input")}
		case types.EventAgentTurnCompleted, types.EventCommandTurnCompleted, types.EventWebhookTurnCompleted:
			completedCalls[event.CallTurnID] = true
		case types.EventAgentTurnWaitingInput, types.EventAgentTurnWaitingTool, types.EventAgentTurnWaitingApproval:
			waitingCalls[event.CallTurnID] = true
			waitingTeams[event.TeamTurnID] = true
			waitingFlows[event.FlowTurnID] = true
		case types.EventRecoveryCompleted:
			if event.CallTurnID != "" {
				resolvedCalls[event.CallTurnID] = true
			}
			if event.TeamTurnID != "" {
				resolvedTeams[event.TeamTurnID] = true
			}
			if event.FlowTurnID != "" {
				resolvedFlows[event.FlowTurnID] = true
			}
		}
	}

	var interrupted []types.InterruptedExecution
	interruptedTeamIDs := make(map[string]bool)
	interruptedFlowIDs := make(map[string]bool)
	for turnID, item := range startedCalls {
		if completedCalls[turnID] ||
			waitingCalls[turnID] ||
			resolvedCalls[turnID] ||
			resolvedTeams[item.event.TeamTurnID] ||
			resolvedFlows[item.event.FlowTurnID] ||
			live.running(item.event) {
			continue
		}
		execution := interruptedFromEvent("call_turn", item.event, item.input)
		interrupted = append(interrupted, execution)
		interruptedTeamIDs[item.event.TeamTurnID] = true
		interruptedFlowIDs[item.event.FlowTurnID] = true
	}
	for turnID, item := range startedTeams {
		if completedTeams[turnID] || waitingTeams[turnID] || resolvedTeams[turnID] ||
			interruptedTeamIDs[turnID] || live.running(item.event) {
			continue
		}
		execution := interruptedFromEvent("team_turn", item.event, item.input)
		interrupted = append(interrupted, execution)
		interruptedFlowIDs[item.event.FlowTurnID] = true
	}
	for turnID, item := range startedFlows {
		if completedFlows[turnID] || waitingFlows[turnID] || resolvedFlows[turnID] ||
			interruptedFlowIDs[turnID] {
			continue
		}
		interrupted = append(interrupted, interruptedFromEvent("flow_turn", item.event, item.input))
	}
	sortInterrupted(interrupted)
	return interrupted
}

func interruptedFromEvent(kind string, event storage.SessionEvent, input string) types.InterruptedExecution {
	safe := kind == "flow_turn" || kind == "team_turn"
	reason := "call execution may have side effects; inspect before retry"
	if safe {
		reason = "retrying the containing Team/Flow may repeat downstream work"
	}
	return types.InterruptedExecution{
		Kind:          kind,
		FlowTurnID:    event.FlowTurnID,
		TeamID:        event.TeamID,
		TeamSessionID: event.TeamSessionID,
		TeamTurnID:    event.TeamTurnID,
		CallID:        event.CallID,
		CallTurnID:    event.CallTurnID,
		CallType:      event.CallType,
		Attempt:       event.Attempt,
		RecoveryOf:    event.RecoveryOf,
		StartedSeq:    event.Seq,
		StartedAt:     event.CreatedAt,
		Input:         input,
		CallerTeam:    payloadString(event.Payload, "caller_team"),
		StartedEvent:  event.Type,
		SafeToRetry:   safe,
		RetryReason:   reason,
	}
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

type pendingAgentResumeInfo struct {
	FlowTurnID   string
	TeamID       string
	TeamTurnID   string
	CallID       string
	CallTurnID   string
	CallerTeam   string
	Attempt      int
	CheckpointID string
	TaskID       string
	Status       types.TurnStatus
}

type pendingTeamResumeInfo struct {
	FlowTurnID       string
	TeamID           string
	TeamTurnID       string
	CallerTeam       string
	Attempt          int
	CallResults      map[string]types.CallResult
	PendingToolTasks []types.PendingToolTask
	PendingApprovals []types.AgentPendingApproval
}

// pendingTeamResumes returns every Team waiting on a Tool or an approval, most
// recent first. It used to return a single Team, which was safe while only one
// Team could be paused at a time; with several Teams running at once more than
// one can be paused, and a caller that took only the first would silently drop
// the others. Callers therefore decide what to do with the whole set.
func pendingTeamResumes(events []storage.SessionEvent) []pendingTeamResumeInfo {
	var resumes []pendingTeamResumeInfo
	seen := make(map[string]bool)
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != types.EventTeamTurnWaitingTool &&
			event.Type != types.EventTeamTurnWaitingApproval {
			continue
		}
		if event.TeamTurnID != "" && seen[event.TeamTurnID] {
			continue
		}
		info, ok := pendingTeamResumeFromEvent(event)
		if !ok {
			continue
		}
		if event.TeamTurnID != "" {
			seen[event.TeamTurnID] = true
		}
		resumes = append(resumes, info)
	}
	return resumes
}

// orphanedWaitingExecutions reports Teams paused on a Tool task that is no
// longer in the store.
//
// This is the one waiting state that cannot be resumed. pendingTasksReady loads
// the task to decide whether the Team may continue; a missing record makes it
// fail rather than wait. Since interruptedExecutions treats every waiting turn
// as resumable — correct until the thing being waited for is gone — such a
// session ends up with no path: Resume fails with "task not found", HandleInput
// refuses a session waiting on a Tool, and Recover reports nothing. Reporting
// it as interrupted is what turns a dead session into a recoverable one.
//
// Only a definite "not found" counts. Any other read error means the store
// could not answer, which is not evidence the task is gone: turning a transient
// IO failure into "your session is unrecoverable" is the worse mistake.
func (r *Runtime) orphanedWaitingExecutions(ctx context.Context, events []storage.SessionEvent) []types.InterruptedExecution {
	if r.tasks == nil {
		return nil
	}
	// pendingTeamResumeInfo carries no TeamSessionID, and recovery targets the
	// instance rather than the Team, so take it from the last event naming the
	// turn.
	lastEvent := make(map[string]storage.SessionEvent)
	for _, event := range events {
		if event.TeamTurnID != "" {
			lastEvent[event.TeamTurnID] = event
		}
	}

	var orphaned []types.InterruptedExecution
	for _, team := range pendingTeamResumes(events) {
		if len(team.PendingToolTasks) == 0 {
			continue
		}
		missing := ""
		for _, item := range team.PendingToolTasks {
			// An empty task id means the event did not record what the Team is
			// waiting for — a malformed or older event, not evidence the task is
			// gone. Only a definitive "not found" counts as orphaned; anything
			// else leaves the sitting state as it is.
			if item.TaskID == "" {
				continue
			}
			if _, err := r.tasks.Load(ctx, item.TaskID); errors.Is(err, types.ErrToolTaskNotFound) {
				missing = item.TaskID
				break
			}
		}
		if missing == "" {
			continue
		}
		event := lastEvent[team.TeamTurnID]
		orphaned = append(orphaned, types.InterruptedExecution{
			Kind:          "team_turn",
			FlowTurnID:    team.FlowTurnID,
			TeamID:        team.TeamID,
			TeamSessionID: event.TeamSessionID,
			TeamTurnID:    team.TeamTurnID,
			Attempt:       team.Attempt,
			CallerTeam:    team.CallerTeam,
			StartedEvent:  string(event.Type),
			SafeToRetry:   true,
			RetryReason: "the pending Tool task " + missing +
				" no longer exists, so this turn can never continue; retrying re-runs the Team turn from its start",
		})
	}
	return orphaned
}

// pendingTeamForApproval narrows a set of paused Teams to the one owning the
// given approval request. An approval is addressed by RequestID, so this is
// what keeps two Teams paused on approvals from being confused for each other.
func pendingTeamForApproval(teams []pendingTeamResumeInfo, requestID string) (pendingTeamResumeInfo, bool) {
	for _, team := range teams {
		for _, approval := range team.PendingApprovals {
			if approval.RequestID == requestID {
				return team, true
			}
		}
		for _, callResult := range team.CallResults {
			if callResult.Status != types.TurnWaitingApproval || callResult.PendingApproval == nil {
				continue
			}
			if callResult.PendingApproval.RequestID == requestID {
				return team, true
			}
		}
	}
	return pendingTeamResumeInfo{}, false
}

func pendingTeamResumeFromEvent(event storage.SessionEvent) (pendingTeamResumeInfo, bool) {
	info := pendingTeamResumeInfo{
		FlowTurnID:  event.FlowTurnID,
		TeamID:      event.TeamID,
		TeamTurnID:  event.TeamTurnID,
		CallerTeam:  payloadString(event.Payload, "caller_team"),
		Attempt:     event.Attempt,
		CallResults: make(map[string]types.CallResult),
	}
	if raw, ok := event.Payload["team_result"]; ok {
		var result types.TeamTurnResult
		if err := decodeValue(raw, &result); err == nil {
			info.TeamID = result.Turn.TeamID
			if info.TeamID == "" {
				info.TeamID = event.TeamID
			}
			if info.TeamTurnID == "" {
				info.TeamTurnID = result.Turn.ID
			}
			if info.FlowTurnID == "" {
				info.FlowTurnID = result.Turn.FlowTurnID
			}
			if info.Attempt == 0 {
				info.Attempt = result.Turn.Attempt
			}
			info.CallerTeam = result.Turn.CallerTeam
			info.CallResults = result.CallResults
			info.PendingToolTasks = append(info.PendingToolTasks, result.PendingToolTasks...)
			info.PendingApprovals = append(info.PendingApprovals, result.PendingApprovals...)
			for callID, callResult := range result.CallResults {
				if callResult.Status != types.TurnWaitingTool {
					continue
				}
				if hasPendingCall(info.PendingToolTasks, callID) {
					continue
				}
				info.PendingToolTasks = append(info.PendingToolTasks, types.PendingToolTask{
					CallID:       callID,
					CallTurnID:   callResult.CallTurnID,
					AgentID:      callResult.AgentID,
					TaskID:       callResult.TaskID,
					CheckpointID: callResult.CheckpointID,
					Status:       callResult.Status,
				})
			}
		}
	}
	hasApproval := false
	for _, callResult := range info.CallResults {
		if callResult.Status == types.TurnWaitingApproval &&
			callResult.PendingApproval != nil {
			hasApproval = true
			break
		}
	}
	if len(info.PendingToolTasks) == 0 && !hasApproval {
		return pendingTeamResumeInfo{}, false
	}
	return info, true
}

func hasPendingCall(items []types.PendingToolTask, callID string) bool {
	for _, item := range items {
		if item.CallID == callID {
			return true
		}
	}
	return false
}

func pendingAgentResume(events []storage.SessionEvent) (pendingAgentResumeInfo, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != types.EventAgentTurnWaitingInput &&
			event.Type != types.EventAgentTurnWaitingTool {
			continue
		}
		checkpointID := payloadString(event.Payload, "checkpoint_id")
		if raw, ok := event.Payload["call_result"]; ok {
			var result types.CallResult
			if err := decodeValue(raw, &result); err == nil {
				if checkpointID == "" {
					checkpointID = result.CheckpointID
				}
			}
		}
		if checkpointID == "" {
			continue
		}
		return pendingAgentResumeInfo{
			FlowTurnID:   event.FlowTurnID,
			TeamID:       event.TeamID,
			TeamTurnID:   event.TeamTurnID,
			CallID:       event.CallID,
			CallTurnID:   event.CallTurnID,
			CallerTeam:   payloadString(event.Payload, "caller_team"),
			Attempt:      event.Attempt,
			CheckpointID: checkpointID,
			TaskID:       payloadString(event.Payload, "task_id"),
			Status: func() types.TurnStatus {
				if event.Type == types.EventAgentTurnWaitingTool {
					return types.TurnWaitingTool
				}
				return types.TurnWaitingInput
			}(),
		}, true
	}
	return pendingAgentResumeInfo{}, false
}

func sortInterrupted(items []types.InterruptedExecution) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].CallTurnID+items[i].TeamTurnID+items[i].FlowTurnID <
			items[j].CallTurnID+items[j].TeamTurnID+items[j].FlowTurnID
	})
}

func recoveryHistory(events []storage.SessionEvent) []types.RecoveryEvent {
	history := make([]types.RecoveryEvent, 0)
	for _, event := range events {
		var status string
		switch event.Type {
		case types.EventRecoveryRequested:
			status = "requested"
		case types.EventRecoveryCompleted:
			status = "completed"
		default:
			continue
		}
		recovery, _ := event.Payload["request"].(map[string]any)
		if recovery == nil {
			recovery = map[string]any{}
		}
		action, _ := recovery["action"].(string)
		target, _ := recovery["target_turn_id"].(string)
		reason, _ := recovery["reason"].(string)
		history = append(history, types.RecoveryEvent{
			Seq:          event.Seq,
			EventID:      event.EventID,
			Status:       status,
			Action:       types.RecoveryAction(action),
			TargetTurnID: target,
			Attempt:      event.Attempt,
			Reason:       reason,
			CreatedAt:    event.CreatedAt,
		})
	}
	return history
}

func recoveryTargetID(target types.InterruptedExecution) string {
	if target.CallTurnID != "" {
		return target.CallTurnID
	}
	if target.TeamTurnID != "" {
		return target.TeamTurnID
	}
	return target.FlowTurnID
}

func selectInterrupted(items []types.InterruptedExecution, target string) (types.InterruptedExecution, error) {
	if target != "" {
		for _, item := range items {
			if item.CallTurnID == target || item.TeamTurnID == target || item.FlowTurnID == target {
				return item, nil
			}
		}
		return types.InterruptedExecution{}, fmt.Errorf("interrupted turn %q not found", target)
	}
	if len(items) == 1 {
		return items[0], nil
	}
	return types.InterruptedExecution{}, fmt.Errorf("multiple interrupted executions; target_turn_id is required")
}

func recoveryInput(req types.RecoveryRequest, target types.InterruptedExecution) string {
	if strings.TrimSpace(req.Input) != "" {
		return req.Input
	}
	if strings.TrimSpace(req.Reason) != "" {
		return req.Reason
	}
	return target.Input
}

func recoverySummary(items []types.InterruptedExecution) string {
	if len(items) == 0 {
		return ""
	}
	return fmt.Sprintf("%d interrupted execution(s) require explicit recovery action", len(items))
}

func (r *Runtime) appendRecoveryCompleted(
	ctx context.Context,
	sessionID string,
	target types.InterruptedExecution,
	request types.RecoveryRequest,
) error {
	return r.appendSessionEvent(ctx, sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventRecoveryCompleted,
			FlowSessionID: sessionID,
			FlowTurnID:    target.FlowTurnID,
			TeamID:        target.TeamID,
			TeamTurnID:    target.TeamTurnID,
			CallID:        target.CallID,
			CallTurnID:    target.CallTurnID,
			CallType:      target.CallType,
			Attempt:       target.Attempt + 1,
			RecoveryOf:    recoveryTargetID(target),
		},
		Payload: map[string]any{"request": request, "target": target},
	})
}

// validateDependencies checks the store's currently published tree. This is a
// startup/validation path rather than a turn path, so it takes its own
// snapshot instead of threading a turn's defs in: a caller asking "is the flow
// loaded" wants the answer for the definitions in force right now.
func (r *Runtime) validateDependencies(flowID string) error {
	defs := r.definitions.Snapshot()
	if defs == nil {
		return errors.New("definitions are nil")
	}
	if flowID != "" && flowID != defs.Flow.ID {
		return fmt.Errorf("flow %q is not loaded", flowID)
	}
	return defs.Flow.ValidateWithTeams(defs.Teams)
}

func dependenciesCompleted(dependencies []string, completed map[string]bool) bool {
	for _, dependency := range dependencies {
		if !completed[dependency] {
			return false
		}
	}
	return true
}

func fixedTargets(binding types.FlowTeamBinding) []string {
	if binding.OnProceed == nil {
		return nil
	}
	return append([]string(nil), binding.OnProceed.Teams...)
}

func teamInput(input string, spec types.InputSpec) string {
	if spec.UserMessage {
		return input
	}
	return ""
}

// recordTaskContextOf mirrors the rule used for call records: only per_task
// instances isolate their output; the coordination layer stays session-wide.
func recordTaskContextOf(session types.TeamSession) string {
	if session.IsPerTask() {
		return session.TaskContextID
	}
	return ""
}

// selectFlowRecords keeps the session-wide contract: only records outside any
// task context are considered.
func selectFlowRecords(records []types.SharedRecord, spec types.InputSpec, since time.Time) []types.SharedRecord {
	return selectFlowRecordsInContext(records, spec, since, "", "", "")
}

func selectFlowRecordsInContext(records []types.SharedRecord, spec types.InputSpec, since time.Time, taskContextID, consumerTeam, flowTurnID string) []types.SharedRecord {
	if spec.FlowRecords == nil && spec.Records == nil {
		return nil
	}

	allowed := make(map[string]struct{})
	for _, name := range spec.FlowRecords {
		allowed[name] = struct{}{}
	}
	for _, binding := range spec.Records {
		if binding.Record != "" {
			allowed[binding.Record] = struct{}{}
		}
	}

	// Evidence is append-only, so one record id appears once per revision.
	// Without folding to the latest revision, a record that has since been
	// settled reads as active again the moment its earlier copy is scanned.
	latest := make(map[string]int, len(records))
	for index, record := range records {
		latest[record.RecordID] = index
	}

	selected := make([]types.SharedRecord, 0, len(records))
	for index, record := range records {
		if latest[record.RecordID] != index {
			continue
		}
		if _, ok := allowed[record.Name]; !ok {
			continue
		}
		// Only an active record is input. A pending one is a progress line whose
		// work has not come back yet; superseded or invalidated text has already
		// been replaced or contradicted by what happened after it.
		if record.Status != "" && record.Status != types.RecordActive {
			continue
		}
		// A Team does not read its own words from an earlier turn as input. It
		// wrote them, and feeding a progress line back is how the entry Team
		// answered the user with its own sentence from five minutes earlier
		// (fs_xxx). Within one turn the record stays readable: that
		// is a Team continuing its own work, and examples/novel-rp relies on a
		// Team reading the record it produced (lobby declares ScenePlan as both
		// output and input).
		if consumerTeam != "" && record.Producer.TeamID == consumerTeam &&
			flowTurnID != "" && record.Producer.FlowTurnID != flowTurnID {
			continue
		}
		// 记录跨轮留存：上一轮问题产生的结论不能被当成本轮已确认的事实。
		// 只取本轮会话窗口内的记录，避免 owner 把旧结论直接当答案（s13）。
		if !since.IsZero() && !record.CreatedAt.IsZero() && record.CreatedAt.Before(since) {
			continue
		}
		// Tasks are isolated by context: a record produced inside one task
		// context is invisible outside it. Records with no context belong to the
		// session-wide layer (coordination output) and stay visible to everyone,
		// which is what keeps a resident coordinator readable to its tasks.
		if record.Producer.TaskContextID != "" && record.Producer.TaskContextID != taskContextID {
			continue
		}
		selected = append(selected, record)
	}
	return selected
}

func selectFlowRecordsForBinding(records []types.SharedRecord, binding types.FlowTeamBinding, since time.Time, taskContextID, flowTurnID string) []types.SharedRecord {
	return selectFlowRecordsInContext(records, binding.Inputs, since, taskContextID, binding.ID, flowTurnID)
}

func recordIDs(records []types.SharedRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if record.RecordID != "" {
			ids = append(ids, record.RecordID)
		}
	}
	return ids
}

func turnStatusForSession(status types.SessionStatus) types.TurnStatus {
	switch status {
	case types.SessionIdle:
		return types.TurnWaitingInput
	case types.SessionWaitingTool:
		return types.TurnWaitingTool
	case types.SessionCompleted:
		return types.TurnCompleted
	case types.SessionCancelled:
		return types.TurnCancelled
	case types.SessionFailed:
		return types.TurnFailed
	default:
		return types.TurnRunning
	}
}

// sessionStatusAfterTurn maps a turn outcome onto the session state. A failed
// turn ends that turn, not the conversation: the session goes back to
// waiting_input so the next user message starts a new turn, while the turn
// itself keeps the failed status and the error callers display. SessionFailed
// stays in the enum for old session data; nothing new writes it.
// replyPayloadField is what a turn publishes as its text.
//
// A turn that is still waiting for a Team has no answer yet: its text is a
// progress line ("已转给执行侧…"). Publishing it invites every client to render
// it as the reply, and a client that does so twice — as s19 did — leaves the
// user with two identical progress lines and no conclusion. The intermediate
// flag alone is not enough: it only works if the client reads it.
//
// So an intermediate turn publishes no reply at all. The conclusion arrives on
// the turn that actually has one.
func replyPayloadField(reply string, intermediate bool) string {
	if intermediate {
		return ""
	}
	return reply
}

// HasPendingTeamWork reports whether a Team dispatched from this session is
// still running. Transports use it while following async turns: flow.jsonl
// records a dispatched Team only when it answers, so without this a follower
// sees an empty stream during the whole hand-over and closes early.
func (r *Runtime) HasPendingTeamWork(ctx context.Context, sessionID string) bool {
	return r.hasPendingTeamCall(ctx, sessionID)
}

// hasPendingTeamCall reports whether this session still has a Team call running.
// It is the only signal that tells "this turn answered the user" apart from
// "this turn handed work over and is waiting", and only the engine has it.
func (r *Runtime) hasPendingTeamCall(ctx context.Context, sessionID string) bool {
	if r.tasks == nil || sessionID == "" {
		return false
	}
	tasks, err := r.tasks.List(ctx)
	if err != nil {
		return false
	}
	for _, task := range tasks {
		if task.FlowSessionID != sessionID || task.ToolName != types.ToolNameTeamRun {
			continue
		}
		if task.Status == types.ToolTaskQueued || task.Status == types.ToolTaskRunning {
			return true
		}
	}
	return false
}

// markTurnRunning records that this process is executing a turn for a session.
// closeInterruptedTurns gives every execution left running a terminal event
// before this process starts a new turn on the session.
//
// A process that exits mid-turn leaves started events with no terminal: a
// replay of 128 real sessions found 13 such FlowTurns and 22 TeamTurns, all on
// the tail of a session. Without a terminal the state reads "still running"
// forever, so every consumer asking "is it done" has to infer it — and an
// inferred state is exactly what produces duplicated or missing output.
//
// Only the FlowTurn is closed here. Team and call turns belong to it: a client
// reading the flow level sees the session become resumable again, and the
// deeper records stay as they were for anyone debugging from the event stream.
func (r *Runtime) closeInterruptedTurns(ctx context.Context, sessionID string) {
	if r == nil || r.sessions == nil || sessionID == "" {
		return
	}
	r.turnMu.Lock()
	if r.closedInterrupts == nil {
		r.closedInterrupts = make(map[string]bool)
	}
	alreadyClosed := r.closedInterrupts[sessionID]
	r.closedInterrupts[sessionID] = true
	r.turnMu.Unlock()
	if alreadyClosed {
		return
	}
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, item := range interruptedExecutions(replay.Events, r.liveIndexFor(sessionID)) {
		if item.Kind != "flow_turn" || item.FlowTurnID == "" {
			continue
		}
		turn := types.FlowTurn{
			ID:            item.FlowTurnID,
			FlowSessionID: sessionID,
			Attempt:       item.Attempt,
			RecoveryOf:    item.RecoveryOf,
			Input:         item.Input,
			Status:        types.TurnCancelled,
			StartedAt:     item.StartedAt,
			FinishedAt:    &now,
		}
		if err := r.appendSessionEvent(ctx, sessionID, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventFlowTurnCompleted,
				FlowSessionID: sessionID,
				FlowTurnID:    item.FlowTurnID,
				Attempt:       item.Attempt,
				RecoveryOf:    item.RecoveryOf,
			},
			Payload: map[string]any{
				"turn":  turn,
				"error": "interrupted: the process stopped while this turn was running",
				// intermediate keeps a truncated progress turn from being
				// relayed as an answer now that it has a terminal.
				"intermediate": true,
			},
		}); err != nil {
			logging.Warn("close interrupted turn failed", map[string]any{
				"flow_session_id": sessionID,
				"flow_turn_id":    item.FlowTurnID,
				"error":           err.Error(),
			})
		}
	}
}

// markTurnRunning publishes the turn as live and returns a context Cancel can
// cancel, plus the cleanup that unpublishes it.
func (r *Runtime) markTurnRunning(ctx context.Context, sessionID string) (context.Context, func()) {
	if sessionID == "" {
		return ctx, func() {}
	}
	turnCtx, cancel := context.WithCancel(ctx)
	r.turnMu.Lock()
	if r.runningTurns == nil {
		r.runningTurns = make(map[string]bool)
	}
	if r.turnCancels == nil {
		r.turnCancels = make(map[string]context.CancelFunc)
	}
	r.runningTurns[sessionID] = true
	r.turnCancels[sessionID] = cancel
	r.turnMu.Unlock()
	return turnCtx, func() {
		cancel()
		r.turnMu.Lock()
		delete(r.runningTurns, sessionID)
		delete(r.turnCancels, sessionID)
		r.turnMu.Unlock()
	}
}

// cancelRunningTurn stops the turn this process is running for the session, if
// any. Reporting "cancelled" without this leaves the in-flight model call and
// its answer to arrive afterwards.
func (r *Runtime) cancelRunningTurn(sessionID string) bool {
	r.turnMu.Lock()
	cancel := r.turnCancels[sessionID]
	r.turnMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// isTurnRunning reports whether this process has a live turn for the session.
func (r *Runtime) isTurnRunning(sessionID string) bool {
	r.turnMu.Lock()
	defer r.turnMu.Unlock()
	return r.runningTurns[sessionID]
}

// registerSessionWork returns a cancellable context for work that must outlive
// the turn that started it, plus a release func to call when that work ends.
//
// The context is deliberately not derived from the caller's: a dispatched Team
// keeps running after the FlowTurn returns, so inheriting that context would
// kill it at once. Detaching it from the caller does not mean detaching it from
// the session, which is what this map restores.
func (r *Runtime) registerSessionWork(sessionID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancelMu.Lock()
	if r.sessionWork == nil {
		r.sessionWork = make(map[string]map[int]context.CancelFunc)
	}
	if r.sessionWork[sessionID] == nil {
		r.sessionWork[sessionID] = make(map[int]context.CancelFunc)
	}
	id := r.nextWorkID
	r.nextWorkID++
	r.sessionWork[sessionID][id] = cancel
	r.cancelMu.Unlock()

	return ctx, func() {
		cancel()
		r.cancelMu.Lock()
		if work := r.sessionWork[sessionID]; work != nil {
			delete(work, id)
			if len(work) == 0 {
				delete(r.sessionWork, sessionID)
			}
		}
		r.cancelMu.Unlock()
	}
}

// cancelSessionWork stops every background execution registered for a session
// and reports how many were running. The cancel funcs are removed before being
// called so a concurrent release cannot double-delete.
func (r *Runtime) cancelSessionWork(sessionID string) int {
	r.cancelMu.Lock()
	work := r.sessionWork[sessionID]
	delete(r.sessionWork, sessionID)
	r.cancelMu.Unlock()

	for _, cancel := range work {
		cancel()
	}
	return len(work)
}

func sessionStatusAfterTurn(status types.SessionStatus) types.SessionStatus {
	if status == types.SessionFailed {
		return types.SessionIdle
	}
	return status
}

func joinReply(current, next string) string {
	if strings.TrimSpace(current) == "" {
		return next
	}
	if strings.TrimSpace(next) == "" {
		return current
	}
	return current + "\n\n" + next
}

func decodeValue(value any, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
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

func newID(prefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// recentActivationWindow is how many activations a tripped Team-turn backstop
// reports. Six is enough to show a two-Team cycle repeating without turning
// the error into a log dump.
const recentActivationWindow = 6

// activationPair names one edge of the activation graph, e.g. "owner→worktree".
// The entry Team has no activator, so it is labelled distinctly rather than
// showing up as "→entry".
func activationPair(act activation) string {
	if act.activator == "" {
		return "entry→" + act.teamID
	}
	return act.activator + "→" + act.teamID
}

// topActivationPairs returns the most frequent edges, most-used first.
func topActivationPairs(counts map[string]int) []string {
	type pair struct {
		name string
		n    int
	}
	pairs := make([]pair, 0, len(counts))
	for name, n := range counts {
		pairs = append(pairs, pair{name, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].name < pairs[j].name
	})
	if len(pairs) > 3 {
		pairs = pairs[:3]
	}
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, fmt.Sprintf("%s ×%d", p.name, p.n))
	}
	return out
}

// writeEvenIfCancelled returns a context still usable for writing when the
// caller's context has already been cancelled.
//
// Terminal events are the ledger: they are how the recovery check learns that a
// turn, team turn or call ended. Skipping them on cancellation makes an ordinary
// cancel indistinguishable from a crash, and the session then refuses new input
// until someone runs recovery.
func writeEvenIfCancelled(ctx context.Context) context.Context {
	if ctx == nil || ctx.Err() == nil {
		return ctx
	}
	return context.Background()
}
