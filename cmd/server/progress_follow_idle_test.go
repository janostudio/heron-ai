package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// shortenFollowLimits 压缩两个"无进展"阈值，返回还原函数。
// 与 shortenFollowCadence 分开，避免影响已有用例的语义。
func shortenFollowLimits(t *testing.T, silence, pending time.Duration) func() {
	t.Helper()
	oldSilence, oldPending := followAsyncSilenceLimit, followAsyncPendingGrace
	followAsyncSilenceLimit, followAsyncPendingGrace = silence, pending
	return func() {
		followAsyncSilenceLimit, followAsyncPendingGrace = oldSilence, oldPending
	}
}

// TestFollowHasNoTotalRuntimeCap 是本次修改的主回归。
//
// 旧实现用一个固定的总时长上限（15 分钟）兜底：只要有 Team 在跑，订阅在满
// 15 分钟时被无条件关掉，之后写出的结论没人收听，调用方永远停在"执行中"
// （实测 fs_cac1168638104818：15 分 37 秒的任务，结论落在窗口外）。
// 现在改为"多久没有进展才放弃"：只要还有 Team 在跑，跟多久都行。
func TestFollowHasNoTotalRuntimeCap(t *testing.T) {
	restore := shortenFollowCadence(t)
	defer restore()
	restoreLimits := shortenFollowLimits(t, time.Hour, time.Hour)
	defer restoreLimits()

	store := newMemorySessionStore()
	pump := startEventPump(context.Background(), store, "fs-1", "", 0, func(map[string]any) error { return nil })
	defer pump.Stop()

	pump.busy = func(context.Context, string) bool { return true }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		// maxWait = 0：不设总时长上限
		pump.waitForIdle(ctx, "fs-1", 0)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("还有 Team 在跑时不能因为总时长而关闭订阅")
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	<-done
}

// TestFollowGivesUpOnStalePendingWork 覆盖"事件账本说还有活、实际没有任务在跑"
// 的场景（旧实现会一直挂到总时长上限才释放）。
func TestFollowGivesUpOnStalePendingWork(t *testing.T) {
	restore := shortenFollowCadence(t)
	defer restore()
	restoreLimits := shortenFollowLimits(t, time.Hour, 40*time.Millisecond)
	defer restoreLimits()

	store := newMemorySessionStore()
	pump := startEventPump(context.Background(), store, "fs-1", "", 0, func(map[string]any) error { return nil })
	defer pump.Stop()

	pump.busy = func(context.Context, string) bool { return false }
	// 一条已入队但永远不会投递的 mailbox 消息：pending 为真，但没有任务在跑。
	pump.workMu.Lock()
	pump.work.openMail = map[string]struct{}{"m-stale": {}}
	pump.workMu.Unlock()

	start := time.Now()
	pump.waitForIdle(context.Background(), "fs-1", 0)
	elapsed := time.Since(start)

	require.GreaterOrEqual(t, elapsed, 40*time.Millisecond,
		"要留出 pending grace，别把即将到达的唤醒关在门外")
	require.Less(t, elapsed, time.Second,
		"pending 长期不落地时必须收口，不能挂到无限")
}

// TestFollowGivesUpWhenTaskRunsSilently 覆盖"任务卡在 running、但一条事件都不产生"
// 的兜底：没有进展就放弃，并留下 warn。
func TestFollowGivesUpWhenTaskRunsSilently(t *testing.T) {
	restore := shortenFollowCadence(t)
	defer restore()
	restoreLimits := shortenFollowLimits(t, 40*time.Millisecond, time.Hour)
	defer restoreLimits()

	store := newMemorySessionStore()
	pump := startEventPump(context.Background(), store, "fs-1", "", 0, func(map[string]any) error { return nil })
	defer pump.Stop()

	pump.busy = func(context.Context, string) bool { return true }

	start := time.Now()
	pump.waitForIdle(context.Background(), "fs-1", 0)
	elapsed := time.Since(start)

	require.GreaterOrEqual(t, elapsed, 40*time.Millisecond, "静默阈值未到之前要保持订阅")
	require.Less(t, elapsed, time.Second, "任务长期零事件时必须判定卡死并释放订阅")
}
