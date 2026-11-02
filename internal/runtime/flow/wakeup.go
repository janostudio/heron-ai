package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

const (
	// maxWakeupNoteChars bounds one note. A note is instruction for the
	// model's future self, not a document: past a couple of thousand
	// characters it is re-reading its own planning instead of working. It is
	// rejected rather than truncated, because a silently shortened note
	// changes what the model asked to be reminded of.
	maxWakeupNoteChars = 2000
	// maxWakeupFactsChars bounds the facts block built at wake-up time.
	maxWakeupFactsChars = 2000
	// wakeupFactsActions is how many recent tool calls the facts block shows.
	wakeupFactsActions = 10
)

// wakeupIndex is one session's wake-up records as this process knows them. It
// is a cache over the event stream, not a second source of truth: a session
// missing from the map is either new or from a process that has not read it
// yet, and wakeupIndexFor replays in both cases.
type wakeupIndex struct {
	records []types.WakeupRecord
	loaded  bool
}

// ScheduleWakeup implements types.WakeupScheduler: it records what the model
// wants to be reminded of and returns immediately. Nothing waits for FireAt.
func (r *Runtime) ScheduleWakeup(ctx context.Context, req types.WakeupRequest) (types.WakeupRecord, error) {
	sessionID := strings.TrimSpace(req.FlowSessionID)
	if sessionID == "" {
		return types.WakeupRecord{}, fmt.Errorf("wake-up requires a flow session id")
	}
	teamID := strings.TrimSpace(req.TeamID)
	if teamID == "" {
		return types.WakeupRecord{}, fmt.Errorf("wake-up requires the Team scheduling it")
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		return types.WakeupRecord{}, fmt.Errorf("wake-up requires a note: what to check, and what to do about what it finds")
	}
	if runes := len([]rune(note)); runes > maxWakeupNoteChars {
		return types.WakeupRecord{}, fmt.Errorf(
			"wake-up note is %d characters, over the %d limit; shorten it instead of relying on truncation",
			runes, maxWakeupNoteChars)
	}
	now := time.Now().UTC()
	limits := r.wakeupLimits()
	if !req.FireAt.After(now.Add(limits.MinAfter)) {
		return types.WakeupRecord{}, fmt.Errorf("wake-up must be at least %s from now", limits.MinAfter)
	}
	if req.FireAt.After(now.Add(limits.MaxAge)) {
		return types.WakeupRecord{}, fmt.Errorf("wake-up must be within %s", limits.MaxAge)
	}

	index, err := r.wakeupIndexFor(ctx, sessionID)
	if err != nil {
		return types.WakeupRecord{}, err
	}

	chain := r.firesSinceLastUserTurn(ctx, sessionID)

	r.wakeupMu.Lock()
	pending := 0
	firesToday := 0
	for _, rec := range index.records {
		if rec.Status != types.WakeupScheduled {
			if rec.FiredAt != nil && sameUTCDay(*rec.FiredAt, now) {
				firesToday++
			}
			continue
		}
		pending++
	}
	if pending >= limits.MaxPending {
		r.wakeupMu.Unlock()
		return types.WakeupRecord{}, fmt.Errorf(
			"this session already has %d pending wake-ups (limit %d); cancel one with CancelWakeup first",
			pending, limits.MaxPending)
	}
	if firesToday >= limits.MaxFiresPerDay {
		r.wakeupMu.Unlock()
		return types.WakeupRecord{}, fmt.Errorf(
			"this session already woke up %d times today (limit %d)", firesToday, limits.MaxFiresPerDay)
	}
	if chain >= limits.MaxChain {
		r.wakeupMu.Unlock()
		return types.WakeupRecord{}, fmt.Errorf(
			"this session reached %d consecutive wake-ups (limit %d); ask the user how to proceed instead",
			chain, limits.MaxChain)
	}
	r.wakeupMu.Unlock()

	record := types.WakeupRecord{
		ID:            newID("wk"),
		FlowSessionID: sessionID,
		TeamID:        teamID,
		CallID:        strings.TrimSpace(req.CallID),
		FireAt:        req.FireAt.UTC(),
		Note:          note,
		LastSeq:       r.latestSessionSeq(ctx, sessionID),
		Status:        types.WakeupScheduled,
		Chain:         chain + 1,
		CreatedAt:     now,
	}
	if err := r.appendSessionEvent(ctx, sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventWakeupCreated,
			FlowSessionID: sessionID,
			TeamID:        teamID,
			CallID:        record.CallID,
		},
		Payload: map[string]any{"wakeup": record},
	}); err != nil {
		return types.WakeupRecord{}, err
	}

	r.wakeupMu.Lock()
	index.records = append(index.records, record)
	r.wakeupMu.Unlock()

	// A registration is the moment a process with nothing to do becomes a
	// process with something to wait for.
	r.StartWakeupSchedulerAsync()
	r.signalWakeupScheduler()
	logging.Info("wake-up scheduled", map[string]any{
		"flow_session_id": sessionID,
		"team_id":         teamID,
		"wakeup_id":       record.ID,
		"fire_at":         record.FireAt.Format(time.RFC3339),
		"chain":           record.Chain,
	})
	return record, nil
}

// CancelWakeup implements types.WakeupScheduler. Cancelling an already
// cancelled or fired wake-up is a no-op success: the model's intent is "this
// no longer matters", and failing on a race would only make it retry.
func (r *Runtime) CancelWakeup(ctx context.Context, sessionID, wakeupID string) error {
	sessionID = strings.TrimSpace(sessionID)
	wakeupID = strings.TrimSpace(wakeupID)
	if sessionID == "" || wakeupID == "" {
		return fmt.Errorf("cancelling a wake-up requires a session id and a wake-up id")
	}
	index, err := r.wakeupIndexFor(ctx, sessionID)
	if err != nil {
		return err
	}

	r.wakeupMu.Lock()
	var target *types.WakeupRecord
	for i := range index.records {
		if index.records[i].ID == wakeupID {
			target = &index.records[i]
			break
		}
	}
	if target == nil {
		r.wakeupMu.Unlock()
		return fmt.Errorf("no wake-up %q in this session", wakeupID)
	}
	if target.Status != types.WakeupScheduled {
		r.wakeupMu.Unlock()
		return nil
	}
	record := *target
	record.Status = types.WakeupCancelled
	r.wakeupMu.Unlock()

	if err := r.appendSessionEvent(ctx, sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventWakeupCancelled,
			FlowSessionID: sessionID,
			TeamID:        record.TeamID,
			CallID:        record.CallID,
		},
		Payload: map[string]any{"wakeup": record},
	}); err != nil {
		return err
	}

	r.wakeupMu.Lock()
	r.replaceWakeupLocked(index, record)
	r.wakeupMu.Unlock()
	r.signalWakeupScheduler()
	return nil
}

// wakeupIndexFor returns the session's index, replaying the event stream the
// first time it is asked for. The returned pointer is stable for the life of
// the session; callers must hold wakeupMu while reading or mutating it.
func (r *Runtime) wakeupIndexFor(ctx context.Context, sessionID string) (*wakeupIndex, error) {
	r.wakeupMu.Lock()
	if index, ok := r.wakeupIndex[sessionID]; ok && index.loaded {
		r.wakeupMu.Unlock()
		return index, nil
	}
	r.wakeupMu.Unlock()

	records, err := r.replayWakeups(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	r.wakeupMu.Lock()
	defer r.wakeupMu.Unlock()
	if r.wakeupIndex == nil {
		r.wakeupIndex = make(map[string]*wakeupIndex)
	}
	index := &wakeupIndex{records: records, loaded: true}
	r.wakeupIndex[sessionID] = index
	return index, nil
}

// replayWakeups folds every wake-up event of one session into the current
// record set. Later events for the same id overwrite earlier ones, so the last
// state written wins — the same rule the live index follows.
func (r *Runtime) replayWakeups(ctx context.Context, sessionID string) ([]types.WakeupRecord, error) {
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	byID := make(map[string]types.WakeupRecord)
	order := make([]string, 0, 4)
	for _, event := range replay.Events {
		switch event.Type {
		case types.EventWakeupCreated,
			types.EventWakeupFired,
			types.EventWakeupSkipped,
			types.EventWakeupCancelled,
			types.EventWakeupExpired:
		default:
			continue
		}
		record, ok := wakeupFromPayload(event.Payload)
		if !ok {
			continue
		}
		if _, seen := byID[record.ID]; !seen {
			order = append(order, record.ID)
		}
		byID[record.ID] = record
	}
	records := make([]types.WakeupRecord, 0, len(order))
	for _, id := range order {
		records = append(records, byID[id])
	}
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	return records, nil
}

// wakeupFromPayload accepts both shapes a payload takes: the typed record
// while the event is still in memory, and a decoded map after a JSONL round
// trip (or a restart).
func wakeupFromPayload(payload map[string]any) (types.WakeupRecord, bool) {
	if payload == nil {
		return types.WakeupRecord{}, false
	}
	switch value := payload["wakeup"].(type) {
	case types.WakeupRecord:
		return value, true
	case map[string]any:
		data, err := json.Marshal(value)
		if err != nil {
			return types.WakeupRecord{}, false
		}
		var record types.WakeupRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return types.WakeupRecord{}, false
		}
		return record, true
	default:
		return types.WakeupRecord{}, false
	}
}

// replaceWakeupLocked swaps one record in the index. Caller holds wakeupMu.
func (r *Runtime) replaceWakeupLocked(index *wakeupIndex, record types.WakeupRecord) {
	for i := range index.records {
		if index.records[i].ID == record.ID {
			index.records[i] = record
			return
		}
	}
	index.records = append(index.records, record)
}

// pendingWakeups returns every scheduled wake-up of every loaded session,
// soonest first. The scheduler uses it to decide what to wait for; sessions
// that were never touched by this process are not in the map yet and are
// loaded by resumeAllWakeups at startup.
func (r *Runtime) pendingWakeups() []types.WakeupRecord {
	r.wakeupMu.Lock()
	defer r.wakeupMu.Unlock()
	var pending []types.WakeupRecord
	for _, index := range r.wakeupIndex {
		for _, record := range index.records {
			if record.Status == types.WakeupScheduled {
				pending = append(pending, record)
			}
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].FireAt.Before(pending[j].FireAt)
	})
	return pending
}

// latestSessionSeq is the sequence number the facts block will start after.
// A failure to read it is not fatal: zero means "show everything", which is
// correct if verbose.
func (r *Runtime) latestSessionSeq(ctx context.Context, sessionID string) int64 {
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return 0
	}
	var latest int64
	for _, event := range replay.Events {
		if event.Seq > latest {
			latest = event.Seq
		}
	}
	return latest
}

// sameUTCDay reports whether two instants fall on the same UTC calendar day.
// The daily quota is a cost bound, so a fixed timezone is enough and avoids a
// session's quota moving with the caller's locale.
func sameUTCDay(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}

// SetWakeupLimits replaces the default quotas. Zero fields fall back to the
// package defaults, so a caller can override one knob without restating the
// rest.
func (r *Runtime) SetWakeupLimits(limits types.WakeupLimits) {
	r.wakeupMu.Lock()
	r.wakeupCfg = limits.WithDefaults()
	r.wakeupMu.Unlock()
}

// wakeupLimits returns the effective quotas. The stored value is already
// defaulted by SetWakeupLimits, so a test that zeroes one field sees it zeroed.
func (r *Runtime) wakeupLimits() types.WakeupLimits {
	r.wakeupMu.Lock()
	defer r.wakeupMu.Unlock()
	return r.wakeupCfg
}

// signalWakeupScheduler asks the scheduler loop to recompute its next deadline.
// It never blocks: a nudge that arrives while one is already pending carries
// the same information as the pending one.
func (r *Runtime) signalWakeupScheduler() {
	if r.wakeupSignal == nil {
		return
	}
	select {
	case r.wakeupSignal <- struct{}{}:
	default:
	}
}

// markWakeup persists one state transition and updates the in-memory index.
// The event is the source of truth: a failure here leaves the record
// untouched, so a retry sees the state the caller actually reached.
func (r *Runtime) markWakeup(ctx context.Context, record types.WakeupRecord, eventType string, extra map[string]any) error {
	payload := map[string]any{"wakeup": record}
	for key, value := range extra {
		payload[key] = value
	}
	if err := r.appendSessionEvent(ctx, record.FlowSessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          eventType,
			FlowSessionID: record.FlowSessionID,
			TeamID:        record.TeamID,
			CallID:        record.CallID,
		},
		Payload: payload,
	}); err != nil {
		return err
	}
	index, err := r.wakeupIndexFor(ctx, record.FlowSessionID)
	if err != nil {
		return err
	}
	r.wakeupMu.Lock()
	r.replaceWakeupLocked(index, record)
	r.wakeupMu.Unlock()
	return nil
}

// firesSinceLastUserTurn counts wake-ups fired since the user last spoke. It is
// the chain length: a user turn resets it, which is what makes "wait a bit
// longer" sustainable while still stopping a model that only ever talks to
// itself.
func (r *Runtime) firesSinceLastUserTurn(ctx context.Context, sessionID string) int {
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return 0
	}
	lastUserSeq := int64(0)
	for _, event := range replay.Events {
		if event.Type != types.EventFlowTurnStarted {
			continue
		}
		if origin, _ := turnOriginAndStart(event.Payload); origin == turnOriginUser {
			lastUserSeq = event.Seq
		}
	}
	fires := 0
	for _, event := range replay.Events {
		if event.Type == types.EventWakeupFired && event.Seq > lastUserSeq {
			fires++
		}
	}
	return fires
}

// WakeupStats reports how many wake-ups are waiting and how many sessions this
// process has loaded. It is what /api/health shows, so an operator can answer
// "did the engine remember my timers across the restart" without reading event
// logs.
func (r *Runtime) WakeupStats(_ context.Context) (pending int, sessions int) {
	r.wakeupMu.Lock()
	defer r.wakeupMu.Unlock()
	sessions = len(r.wakeupIndex)
	for _, index := range r.wakeupIndex {
		for _, record := range index.records {
			if record.Status == types.WakeupScheduled {
				pending++
			}
		}
	}
	return pending, sessions
}
