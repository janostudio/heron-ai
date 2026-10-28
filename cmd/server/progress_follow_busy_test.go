package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFollowKeepsSubscriptionWhileTeamTaskRuns is the s24 regression.
//
// 派活轮返回空答复后，被派出去的 Team 还在跑，flow.jsonl 里一段时间没有任何
// 事件。旧的 idle 判定只读事件流，于是 3 秒后就把订阅关了；等 Team 写出结论时
// 已经没人收听，调用方永远等不到答复（实测：订阅 17:44 关闭，结论 17:49 落盘）。
// 现在 idle 判定还要问一次“还有没有 Team 任务在跑”。
func TestFollowKeepsSubscriptionWhileTeamTaskRuns(t *testing.T) {
	restore := shortenFollowCadence(t)
	defer restore()

	store := newMemorySessionStore()
	pump := startEventPump(context.Background(), store, "fs-1", "", 0, func(map[string]any) error { return nil })
	defer pump.Stop()

	pump.busy = func(context.Context, string) bool { return true }

	start := time.Now()
	pump.waitForIdle(context.Background(), "fs-1", 120*time.Millisecond)
	elapsed := time.Since(start)

	require.GreaterOrEqual(t, elapsed, 100*time.Millisecond,
		"还有 Team 任务在跑时不能按 idle 关闭订阅，应一直跟到 maxWait")
}

// TestFollowClosesWhenNothingIsPending 是上一条的对照：没有 Team 任务在跑时，
// 静默超过 followAsyncHold 就该关闭订阅，别把订阅挂到 15 分钟。
func TestFollowClosesWhenNothingIsPending(t *testing.T) {
	restore := shortenFollowCadence(t)
	defer restore()

	store := newMemorySessionStore()
	pump := startEventPump(context.Background(), store, "fs-1", "", 0, func(map[string]any) error { return nil })
	defer pump.Stop()

	pump.busy = func(context.Context, string) bool { return false }

	start := time.Now()
	pump.waitForIdle(context.Background(), "fs-1", 5*time.Second)
	elapsed := time.Since(start)

	require.Less(t, elapsed, time.Second, "无活可等时应尽快关闭订阅")
}

// TestFollowClosesAfterTeamTaskFinishes 覆盖 busy 由 true 变 false 的过渡：
// Team 跑完那一刻不能立刻断订阅（结论帧可能还在路上），但也不能一直挂着——
// 静默达到 followAsyncHold 之后必须关闭，且延迟不超过一次 busy 轮询。
func TestFollowClosesAfterTeamTaskFinishes(t *testing.T) {
	restore := shortenFollowCadence(t)
	defer restore()

	store := newMemorySessionStore()
	pump := startEventPump(context.Background(), store, "fs-1", "", 0, func(map[string]any) error { return nil })
	defer pump.Stop()

	checks := 0
	pump.busy = func(context.Context, string) bool {
		checks++
		return checks <= 3
	}

	start := time.Now()
	pump.waitForIdle(context.Background(), "fs-1", 5*time.Second)
	elapsed := time.Since(start)

	require.GreaterOrEqual(t, elapsed, followAsyncHold,
		"Team 刚跑完时要留出 quiet window，别把最后一帧关在门外")
	require.Less(t, elapsed, time.Second,
		"busy 变 false 之后必须收口，不能挂到 maxWait")
}

// shortenFollowCadence 把跟随节奏压到毫秒级，返回还原函数。
func shortenFollowCadence(t *testing.T) func() {
	t.Helper()
	oldHold, oldPoll, oldBusyPoll := followAsyncHold, followAsyncPoll, followAsyncBusyPoll
	followAsyncHold = 10 * time.Millisecond
	followAsyncPoll = 5 * time.Millisecond
	followAsyncBusyPoll = 5 * time.Millisecond
	return func() {
		followAsyncHold, followAsyncPoll, followAsyncBusyPoll = oldHold, oldPoll, oldBusyPoll
	}
}
