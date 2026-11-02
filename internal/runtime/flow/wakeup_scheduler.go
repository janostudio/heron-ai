package flow

import (
	"context"
	"errors"
	"time"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// wakeupSkipRetry is how long a deferred wake-up waits before trying again.
// A wake-up is deferred when the session is busy or the user just spoke; both
// clear on the order of minutes, so this is a poll interval, not a backoff.
var wakeupSkipRetry = 5 * time.Minute

// SetSessionLister wires the source of session ids used by startup recovery.
// The runtime deliberately does not know the sessions directory: that is the
// app layer's layout, and hard-coding it here would make the flow package
// depend on where the CLI happens to put its files.
func (r *Runtime) SetSessionLister(fn func(context.Context) ([]string, error)) {
	r.wakeupMu.Lock()
	r.wakeupSessionLister = fn
	r.wakeupMu.Unlock()
}

// RecoverWakeups loads every session's wake-up records so a process restart
// does not lose a timer. It is best-effort: a session that cannot be read is
// skipped, because one unreadable conversation must not stop the engine from
// starting.
//
// A record whose FireAt has already passed is left scheduled; the scheduler
// loop fires it as soon as it starts, which is the "a wake-up due during a
// restart fires when the service comes back" case.
func (r *Runtime) RecoverWakeups(ctx context.Context) int {
	r.wakeupMu.Lock()
	lister := r.wakeupSessionLister
	r.wakeupMu.Unlock()
	if lister == nil {
		logging.Debug("wake-up recovery skipped: no session lister configured", nil)
		return 0
	}
	sessionIDs, err := lister(ctx)
	if err != nil {
		logging.Warn("wake-up recovery: cannot list sessions", map[string]any{"error": err.Error()})
		return 0
	}

	var records, pending int
	for _, sessionID := range sessionIDs {
		index, err := r.wakeupIndexFor(ctx, sessionID)
		if err != nil {
			logging.Warn("wake-up recovery: cannot read session", map[string]any{
				"flow_session_id": sessionID,
				"error":           err.Error(),
			})
			continue
		}
		r.wakeupMu.Lock()
		records += len(index.records)
		for _, record := range index.records {
			if record.Status == types.WakeupScheduled {
				pending++
			}
		}
		r.wakeupMu.Unlock()
	}
	logging.Info("wake-up recovery complete", map[string]any{
		"sessions": len(sessionIDs),
		"records":  records,
		"pending":  pending,
	})
	return pending
}

// StartWakeupSchedulerAsync starts the loop in the background, at most once.
//
// It is called when there is something to wait for: a registration, or
// recovered records with future deadlines. A process with no timers holds no
// goroutine.
func (r *Runtime) StartWakeupSchedulerAsync() {
	r.wakeupOnce.Do(func() {
		go r.RunWakeupScheduler(context.Background())
	})
}

// RunWakeupScheduler is the loop that fires due wake-ups. It blocks until ctx
// is done; the app layer starts it in its own goroutine next to the JSON-RPC
// server.
//
// The loop owns no per-wake-up state: the index is the queue, and the heap is
// just the earliest deadline recomputed after every wake-up, registration and
// cancellation. That keeps a restart correct without persisting a heap.
func (r *Runtime) RunWakeupScheduler(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	_ = r.RecoverWakeups(ctx)
	logging.Info("wake-up scheduler started", nil)

	for {
		next, ok := r.nextWakeupDeadline()
		if !ok {
			select {
			case <-ctx.Done():
				logging.Info("wake-up scheduler stopped", nil)
				return
			case <-r.wakeupSignal:
				continue
			}
		}

		wait := time.Until(next)
		if wait <= 0 {
			r.fireDueWakeups(ctx)
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			logging.Info("wake-up scheduler stopped", nil)
			return
		case <-timer.C:
		case <-r.wakeupSignal:
			timer.Stop()
		}
	}
}

// nextWakeupDeadline is the earliest scheduled FireAt not already being fired.
func (r *Runtime) nextWakeupDeadline() (time.Time, bool) {
	r.wakeupMu.Lock()
	defer r.wakeupMu.Unlock()
	var (
		earliest time.Time
		found    bool
	)
	for _, index := range r.wakeupIndex {
		for _, record := range index.records {
			if record.Status != types.WakeupScheduled {
				continue
			}
			if r.wakeupInflight[record.ID] {
				continue
			}
			if !found || record.FireAt.Before(earliest) {
				earliest = record.FireAt
				found = true
			}
		}
	}
	return earliest, found
}

// fireDueWakeups starts every wake-up whose time has come. Each runs in its own
// goroutine: a wake-up turn can take minutes, and one slow conversation must not
// hold the others.
func (r *Runtime) fireDueWakeups(ctx context.Context) {
	now := time.Now().UTC()
	for _, record := range r.pendingWakeups() {
		if record.FireAt.After(now) {
			continue
		}
		if !r.claimWakeupFire(record.ID) {
			continue
		}
		go r.fireWakeup(context.Background(), record)
	}
}

// claimWakeupFire marks one record as being fired so the loop cannot start it
// twice before its state event lands.
func (r *Runtime) claimWakeupFire(wakeupID string) bool {
	r.wakeupMu.Lock()
	defer r.wakeupMu.Unlock()
	if r.wakeupInflight == nil {
		r.wakeupInflight = make(map[string]bool)
	}
	if r.wakeupInflight[wakeupID] {
		return false
	}
	r.wakeupInflight[wakeupID] = true
	return true
}

func (r *Runtime) releaseWakeupFire(wakeupID string) {
	r.wakeupMu.Lock()
	delete(r.wakeupInflight, wakeupID)
	r.wakeupMu.Unlock()
}

// fireWakeup runs one due record and records the outcome.
//
// Deferral is not a failure: the record goes back to scheduled a few minutes
// later, and an event says why so the model can see "I tried and the session
// was busy" on its next wake-up. A terminal session drops the record instead —
// resuming is Recover's job, not a timer's.
func (r *Runtime) fireWakeup(ctx context.Context, record types.WakeupRecord) {
	defer r.releaseWakeupFire(record.ID)

	_, err := r.ResumeForWakeup(ctx, record.FlowSessionID, record)
	switch {
	case err == nil:
		return
	case errors.Is(err, errWakeupDeferred):
		retry := record
		retry.FireAt = time.Now().UTC().Add(wakeupSkipRetry)
		if markErr := r.markWakeup(ctx, retry, types.EventWakeupSkipped, map[string]any{
			"reason": wakeupDeferralReason(err),
		}); markErr != nil {
			logging.Error("record deferred wake-up", map[string]any{
				"flow_session_id": record.FlowSessionID,
				"wakeup_id":       record.ID,
				"error":           markErr.Error(),
			})
		}
		logging.Info("wake-up deferred", map[string]any{
			"flow_session_id": record.FlowSessionID,
			"wakeup_id":       record.ID,
			"reason":          wakeupDeferralReason(err),
			"retry_at":        retry.FireAt.Format(time.RFC3339),
		})
		r.signalWakeupScheduler()
	case errors.Is(err, errWakeupTerminal):
		dropped := record
		dropped.Status = types.WakeupExpired
		if markErr := r.markWakeup(ctx, dropped, types.EventWakeupExpired, map[string]any{
			"reason": "session reached a terminal state before the wake-up fired",
		}); markErr != nil {
			logging.Error("expire wake-up of terminal session", map[string]any{
				"flow_session_id": record.FlowSessionID,
				"wakeup_id":       record.ID,
				"error":           markErr.Error(),
			})
		}
	default:
		logging.Error("wake-up failed", map[string]any{
			"flow_session_id": record.FlowSessionID,
			"wakeup_id":       record.ID,
			"error":           err.Error(),
		})
	}
}
