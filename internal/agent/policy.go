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

// defaultRestrictedBashPhrases 是内置的安全底线，不随业务配置变化。
var defaultRestrictedBashPhrases = []string{
	"rm -rf", "sudo ", "shutdown", "mkfs", "git reset --hard",
}

type DefaultToolPolicy struct {
	restricted []string
}

// NewDefaultToolPolicy keeps the built-in list only.
func NewDefaultToolPolicy() *DefaultToolPolicy { return NewToolPolicy(nil) }

// NewToolPolicy merges the built-in list with externally configured phrases.
// Extra entries only add restrictions; the defaults always apply so a missing
// or empty config cannot silently disable the safety floor.
func NewToolPolicy(extra []string) *DefaultToolPolicy {
	merged := make([]string, 0, len(defaultRestrictedBashPhrases)+len(extra))
	seen := make(map[string]struct{}, len(defaultRestrictedBashPhrases)+len(extra))
	for _, phrase := range append(append([]string{}, defaultRestrictedBashPhrases...), extra...) {
		normalized := strings.ToLower(strings.TrimSpace(phrase))
		if normalized == "" {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		merged = append(merged, normalized)
	}
	return &DefaultToolPolicy{restricted: merged}
}

func (p *DefaultToolPolicy) Check(_ context.Context, req ToolPolicyRequest) (ToolDecision, string, error) {
	name := strings.TrimSpace(req.Call.Name)
	if name == "" {
		return ToolDeny, "tool name is empty", nil
	}
	if name == "Bash" {
		command, _ := req.Call.Arguments["command"].(string)
		lower := strings.ToLower(command)
		restricted := p.restricted
		if len(restricted) == 0 {
			restricted = defaultRestrictedBashPhrases
		}
		for _, phrase := range restricted {
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
