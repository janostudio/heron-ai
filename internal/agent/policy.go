package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type ToolDecision string

const (
	ToolAllow           ToolDecision = "allow"
	ToolDeny            ToolDecision = "deny"
	ToolRequireApproval ToolDecision = "require_approval"
)

type ToolPolicyRequest struct {
	Agent  types.AgentConfig
	Call   types.ToolCall
	FlowID string
	TeamID string
	CallID string
}

type ToolPolicy interface {
	Check(ctx context.Context, req ToolPolicyRequest) (ToolDecision, string, error)
}

type DefaultToolPolicy struct{}

func NewDefaultToolPolicy() *DefaultToolPolicy { return &DefaultToolPolicy{} }

func (p *DefaultToolPolicy) Check(_ context.Context, req ToolPolicyRequest) (ToolDecision, string, error) {
	name := strings.TrimSpace(req.Call.Name)
	if name == "" {
		return ToolDeny, "tool name is empty", nil
	}
	if name == "Bash" {
		command, _ := req.Call.Arguments["command"].(string)
		lower := strings.ToLower(command)
		for _, phrase := range []string{"rm -rf", "sudo ", "shutdown", "mkfs", "git reset --hard"} {
			if strings.Contains(lower, phrase) {
				return ToolRequireApproval, fmt.Sprintf("Bash command contains restricted operation %q", phrase), nil
			}
		}
	}
	if name == "Write" {
		file, _ := req.Call.Arguments["file"].(string)
		if strings.HasPrefix(strings.TrimSpace(file), ".env") || strings.Contains(file, "/.env") {
			return ToolRequireApproval, "writing environment files requires approval", nil
		}
	}
	return ToolAllow, "", nil
}

type ContextPolicy interface {
	Build(req types.AgentRequest) []types.ContextBlock
	Normalize(blocks []types.ContextBlock) []types.ContextBlock
}

type DefaultContextPolicy struct{}

func NewDefaultContextPolicy() *DefaultContextPolicy { return &DefaultContextPolicy{} }

func (p *DefaultContextPolicy) Build(req types.AgentRequest) []types.ContextBlock {
	return p.Normalize(req.ContextBlocks)
}

func contextBlockParts(blocks []types.ContextBlock, kind string) []types.ContentPart {
	var parts []types.ContentPart
	for _, block := range blocks {
		if kind != "" && block.Kind != kind {
			continue
		}
		for _, part := range block.Parts {
			clone := part
			if part.Media != nil {
				media := *part.Media
				clone.Media = &media
			}
			parts = append(parts, clone)
		}
	}
	return parts
}

func (p *DefaultContextPolicy) Normalize(blocks []types.ContextBlock) []types.ContextBlock {
	result := make([]types.ContextBlock, 0, len(blocks))
	seen := make(map[string]struct{}, len(blocks))
	for _, block := range blocks {
		block.Kind = strings.TrimSpace(block.Kind)
		block.Text = strings.TrimSpace(block.Text)
		if block.Kind == "" || (block.Text == "" && len(block.Parts) == 0) {
			continue
		}
		key := block.Kind + "\x00" + block.Source
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if block.Stability == "" {
			block.Stability = "dynamic"
		}
		if block.Placement == "" {
			block.Placement = "user"
		}
		result = append(result, block)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Priority == result[j].Priority {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Priority > result[j].Priority
	})
	return result
}
