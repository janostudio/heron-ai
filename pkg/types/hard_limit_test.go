package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 硬限由 output_reserve_ratio 派生：可发送上限与"给回答留多少"必须是同一个事实，
// 否则两个旋钮会各说一套，而分歧只有在回合失败时才暴露。
func TestHardLimitDerivedFromReserve(t *testing.T) {
	require.InDelta(t, 1.0, ContextConfig{}.HardLimit(), 1e-9, "缺省不预留")
	require.InDelta(t, 0.85, ContextConfig{OutputReserveRatio: 0.15}.HardLimit(), 1e-9)
	require.InDelta(t, 0.5, ContextConfig{OutputReserveRatio: 0.5}.HardLimit(), 1e-9)
}

// 非法预留由 WithDefaults 收回到 0.15，硬限跟着变。
func TestHardLimitAfterDefaults(t *testing.T) {
	require.InDelta(t, 0.85, ContextConfig{OutputReserveRatio: -1}.HardLimit(), 1e-9)
	require.InDelta(t, 0.85, ContextConfig{OutputReserveRatio: 1.5}.HardLimit(), 1e-9)
}

// 默认阈值必须落在硬限以下，否则压缩没有可用落点。
func TestDefaultThresholdBelowHardLimit(t *testing.T) {
	resolved := ContextConfig{}.WithDefaults()
	require.Less(t, resolved.CompactionThreshold, resolved.HardLimit())
}

// 显式预留 15% 时，默认的 0.90 阈值会越过硬限 0.85：这正是 loader 要拒绝的
// 组合，测试把它钉住，避免有人再把它"修"回静默钳制。
func TestThresholdCanExceedHardLimitWhenReserveConfigured(t *testing.T) {
	resolved := ContextConfig{OutputReserveRatio: 0.15}.WithDefaults()
	require.GreaterOrEqual(t, resolved.CompactionThreshold, resolved.HardLimit())
}

// WithDefaults 不再改动 hard_limit_ratio：该字段已废弃，填默认只会让旧配置
// 看起来"配过了"，而真正生效的是派生值。
func TestWithDefaultsLeavesDeprecatedHardLimitAlone(t *testing.T) {
	require.Zero(t, ContextConfig{}.WithDefaults().HardLimitRatio)
	require.InDelta(t, 0.80, ContextConfig{HardLimitRatio: 0.80}.WithDefaults().HardLimitRatio, 1e-9)
}
