package flow

import "github.com/heron-ai/heron-engine/internal/metrics"

// Metric names this package publishes.
//
// They are constants rather than literals at the call sites so a dashboard and
// the code cannot drift apart, and so the set is greppable in one place.
const (
	metricTurnsStarted   = "turns_started_total"
	metricTurnsFinished  = "turns_finished_total"
	metricTurnsInflight  = "turns_inflight_current"
	metricWakeupsFired   = "wakeups_fired_total"
	metricWakeupResched  = "wakeup_reschedules_total"
	metricWakeupsPending = "wakeups_pending_current"
)

// trackTurn counts a turn from entry to return.
//
// It returns the function to defer, rather than a defer inside itself, because
// the caller owns the return paths: a turn refused by validation still counts
// as attempted, and the in-flight gauge has to come back down on every one of
// them.
func trackTurn() func() {
	metrics.Inc(metricTurnsStarted)
	metrics.Add(metricTurnsInflight, 1)
	return func() {
		metrics.Add(metricTurnsInflight, -1)
		metrics.Inc(metricTurnsFinished)
	}
}
