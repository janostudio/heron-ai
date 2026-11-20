package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// 默认配置必须通过：0.45 < 0.90 < 1.0。
func TestValidateContextConfigDefaultsPass(t *testing.T) {
	require.NoError(t, validateContextConfig("default-agent", types.ContextConfig{}))
}

// 旧配置里的 hard_limit_ratio 会被 loader 拒绝，并给出可照做的迁移值。
// 静默忽略更糟：用户以为配了 0.80，实际生效的是派生值。
func TestValidateContextConfigRejectsDeprecatedHardLimit(t *testing.T) {
	err := validateContextConfig("legacy", types.ContextConfig{HardLimitRatio: 0.80})
	require.Error(t, err)
	require.Contains(t, err.Error(), "hard_limit_ratio")
	require.Contains(t, err.Error(), "output_reserve_ratio: 0.2")
}

// 预留 15% 后硬限是 0.85，默认阈值 0.90 越界：以前会被静默钳到 0.85，
// 现在直接拒绝，并说明哪个数真正生效。
func TestValidateContextConfigRejectsThresholdAtHardLimit(t *testing.T) {
	err := validateContextConfig("overlap", types.ContextConfig{OutputReserveRatio: 0.15})
	require.Error(t, err)
	require.Contains(t, err.Error(), "hard limit")
	require.Contains(t, err.Error(), "0.85")
}

// 压缩目标必须在触发点之下，否则压缩会朝更大的体积去压。
func TestValidateContextConfigRejectsTargetAboveThreshold(t *testing.T) {
	err := validateContextConfig("inverted", types.ContextConfig{TargetRatio: 0.60, CompactionThreshold: 0.50})
	require.Error(t, err)
	require.Contains(t, err.Error(), "target_ratio")
}

// 显式把阈值调到硬限以下、目标位低于阈值就合法。
func TestValidateContextConfigAcceptsOrderedRatios(t *testing.T) {
	require.NoError(t, validateContextConfig("ordered", types.ContextConfig{
		TargetRatio:         0.40,
		CompactionThreshold: 0.75,
		OutputReserveRatio:  0.15,
	}))
}
