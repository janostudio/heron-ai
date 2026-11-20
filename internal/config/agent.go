package config

import (
	"fmt"
	"strings"

	"github.com/adrg/frontmatter"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func (l *ConfigLoader) loadAgent(path string) (*types.AgentConfig, error) {
	data, err := l.fileStore.Read(path)
	if err != nil {
		return nil, err
	}

	var agent types.AgentConfig
	body, err := frontmatter.Parse(strings.NewReader(string(data)), &agent)
	if err != nil {
		return nil, err
	}
	agent.Body = string(body)

	if err := validateAgentPolicy(agent); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return &agent, nil
}

// validateAgentPolicy rejects configuration that no longer has an effect.
//
// Time limits on an agent turn were removed: an agent's run time is decided by
// how much work the task needs, and a wall clock cannot tell "still working"
// from "wedged". Leaving the fields unread would let a config look like it sets
// a ceiling that nothing enforces, so they are refused with the reason.
func validateAgentPolicy(agent types.AgentConfig) error {
	if err := validateContextConfig(agent.Name, agent.Context); err != nil {
		return err
	}
	if timeout := strings.TrimSpace(agent.Loop.Timeout); timeout != "" {
		return fmt.Errorf(
			"loop.timeout=%q is no longer supported (agent %q): an agent turn is bounded by rounds and tool calls, not by wall clock. Remove timeout — hangs are caught by the model transport timeouts, the per-tool timeout, and the no-progress guards",
			timeout, agent.Name)
	}
	if wall := strings.TrimSpace(agent.Budget.MaxWallTime); wall != "" {
		return fmt.Errorf(
			"budget.max_wall_time=%q is no longer supported (agent %q): remove it and bound the work with budget.max_model_rounds / max_tool_calls / max_input_tokens if a ceiling is needed",
			wall, agent.Name)
	}
	return nil
}

// validateContextConfig enforces the ordering the compaction path depends on.
//
// These mistakes used to be silently clamped instead of rejected: a threshold
// above the hard limit was lowered to it, and a hard limit that fought
// output_reserve_ratio left two limits sitting on the same value, so "compact,
// then fail" collapsed into "fail". Both look like ordinary configuration and
// only surface later as odd turn failures, so the loader refuses them and says
// which number actually applies.
func validateContextConfig(agentName string, config types.ContextConfig) error {
	if config.HardLimitRatio > 0 {
		return fmt.Errorf(
			"context.hard_limit_ratio=%g is no longer a separate knob (agent %q): the hard limit is derived as 1 - output_reserve_ratio, so the send ceiling and the response reserve cannot disagree. Remove hard_limit_ratio; to keep the same ceiling set output_reserve_ratio: %.2f",
			config.HardLimitRatio, agentName, 1-config.HardLimitRatio)
	}
	resolved := config.WithDefaults()
	if resolved.TargetRatio >= resolved.CompactionThreshold {
		return fmt.Errorf(
			"context.target_ratio=%g must stay below context.compaction_threshold=%g (agent %q): compaction cannot land above the point that triggers it",
			resolved.TargetRatio, resolved.CompactionThreshold, agentName)
	}
	if hard := resolved.HardLimit(); resolved.CompactionThreshold >= hard {
		return fmt.Errorf(
			"context.compaction_threshold=%g must stay below the hard limit %.2f (= 1 - output_reserve_ratio %.2f, agent %q): at or above it there is no window left to compact into before the turn fails",
			resolved.CompactionThreshold, hard, resolved.OutputReserveRatio, agentName)
	}
	return nil
}
