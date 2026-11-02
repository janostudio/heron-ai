package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type stubWakeupScheduler struct {
	requests  []types.WakeupRequest
	cancelled []string
	err       error
}

func (s *stubWakeupScheduler) ScheduleWakeup(_ context.Context, req types.WakeupRequest) (types.WakeupRecord, error) {
	if s.err != nil {
		return types.WakeupRecord{}, s.err
	}
	s.requests = append(s.requests, req)
	return types.WakeupRecord{
		ID:            "wk_test",
		FlowSessionID: req.FlowSessionID,
		TeamID:        req.TeamID,
		FireAt:        req.FireAt,
		Note:          req.Note,
		Status:        types.WakeupScheduled,
	}, nil
}

func (s *stubWakeupScheduler) CancelWakeup(_ context.Context, _ string, wakeupID string) error {
	if s.err != nil {
		return s.err
	}
	s.cancelled = append(s.cancelled, wakeupID)
	return nil
}

func wakeupIdentityContext() context.Context {
	return withSpawnIdentity(context.Background(), types.AgentConfig{Name: "agent-1"}, types.AgentRequest{
		FlowSessionID: "fs-1",
		TeamID:        "default",
		CallID:        "call-1",
	})
}

// The tool must hand the scheduler the caller's coordinates, not guess them:
// the wake-up starts at that Team when it fires.
func TestScheduleWakeupToolPassesCallerIdentity(t *testing.T) {
	scheduler := &stubWakeupScheduler{}
	tool := NewScheduleWakeupTool(scheduler)

	result, err := tool.Execute(wakeupIdentityContext(), map[string]any{
		"when": "2h",
		"note": "check whether the build finished and report the failure log",
	})

	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Len(t, scheduler.requests, 1)
	require.Equal(t, "fs-1", scheduler.requests[0].FlowSessionID)
	require.Equal(t, "default", scheduler.requests[0].TeamID)
	require.Equal(t, "call-1", scheduler.requests[0].CallID)
	require.Contains(t, result.Content, "wk_test")
}

func TestScheduleWakeupToolRejectsMissingNote(t *testing.T) {
	scheduler := &stubWakeupScheduler{}
	tool := NewScheduleWakeupTool(scheduler)

	result, err := tool.Execute(wakeupIdentityContext(), map[string]any{"when": "2h"})

	require.NoError(t, err)
	require.False(t, result.Success, "an empty note leaves the future self nothing to check")
}

func TestScheduleWakeupToolReportsSchedulerError(t *testing.T) {
	tool := NewScheduleWakeupTool(&stubWakeupScheduler{err: context.DeadlineExceeded})

	result, err := tool.Execute(wakeupIdentityContext(), map[string]any{"when": "2h", "note": "x"})

	require.NoError(t, err)
	require.False(t, result.Success)
}

func TestCancelWakeupToolPassesWakeupID(t *testing.T) {
	scheduler := &stubWakeupScheduler{}
	tool := NewCancelWakeupTool(scheduler)

	result, err := tool.Execute(wakeupIdentityContext(), map[string]any{"wakeup_id": "wk_42"})

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, []string{"wk_42"}, scheduler.cancelled)
}

// "6" is rejected rather than guessed: a wake-up at the wrong scale is worse
// than one that fails loudly.
func TestParseWakeupWhenFormats(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		raw      string
		expected time.Time
	}{
		{"30m", now.Add(30 * time.Minute)},
		{"6h", now.Add(6 * time.Hour)},
		{"2d", now.Add(48 * time.Hour)},
		{"2026-10-30T09:00:00+08:00", time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		got, err := parseWakeupWhen(tc.raw, now)
		require.NoError(t, err, tc.raw)
		require.True(t, got.Equal(tc.expected), "%s: got %s want %s", tc.raw, got, tc.expected)
	}

	for _, bad := range []string{"", "6", "soon", "-5m", "0m"} {
		_, err := parseWakeupWhen(bad, now)
		require.Error(t, err, "expected %q to be rejected", bad)
	}
}
