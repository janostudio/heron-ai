package agent

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/internal/tool/script"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type ToolDecision string

const (
	ToolAllow           ToolDecision = "allow"
	ToolDeny            ToolDecision = "deny"
	ToolRequireApproval ToolDecision = "require_approval"
)

// decisionOf maps a permission decision onto the three outcomes the loop knows.
// The posture speaks allow/ask/deny; the loop routes on these three names.
func decisionOf(d types.PermissionDecision) ToolDecision {
	switch d {
	case types.PermissionDeny:
		return ToolDeny
	case types.PermissionAsk:
		return ToolRequireApproval
	default:
		return ToolAllow
	}
}

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

// builtinBashPhrases is the floor that stays whatever the posture says. The posture
// answers "may this job run commands"; these phrases answer "may it run *this*
// command unattended", which no posture should grant silently:
//
//	"permissions": {"shell": "allow"}
//
// is a statement about the machine, not a statement that wiping a directory needs
// no second look. Configuration can add phrases, never remove these.
var builtinBashPhrases = []string{
	"rm -rf", "sudo ", "shutdown", "mkfs", "git reset --hard",
}

// DefaultToolPolicy answers one question per call: does this runtime's posture
// allow it, ask about it, or refuse it?
//
// The posture is global — settings.json's permissions section — not per Agent and
// not per REPL program. One block decides for everything that runs here, which is
// what keeps the configuration surface small: no per-tool table to keep in sync,
// and no second, program-specific permission model to explain.
type DefaultToolPolicy struct {
	posture types.PermissionsConfig
	phrases []string
}

// NewDefaultToolPolicy is the posture of a Flow that declares nothing: allow
// everywhere, plus the built-in floor. It matches what every existing installation
// did before this feature existed.
func NewDefaultToolPolicy() *DefaultToolPolicy {
	return NewPermissionPolicy(types.PermissionsConfig{}, nil)
}

// NewPermissionPolicy builds the policy from the runtime's posture plus the extra
// restricted Bash phrases from settings.json tools.restricted_bash_phrases.
//
// Two sources rather than one because they answer different questions: the posture
// says whether this machine may use a shell at all, the phrase list says which
// individual commands need a human even then.
func NewPermissionPolicy(posture types.PermissionsConfig, extraPhrases []string) *DefaultToolPolicy {
	phrases := make([]string, 0, len(builtinBashPhrases)+len(extraPhrases))
	seen := make(map[string]struct{}, len(builtinBashPhrases)+len(extraPhrases))
	for _, phrase := range append(append([]string{}, builtinBashPhrases...), extraPhrases...) {
		normalized := strings.ToLower(strings.TrimSpace(phrase))
		if normalized == "" {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		phrases = append(phrases, normalized)
	}
	return &DefaultToolPolicy{posture: posture.Normalize(), phrases: phrases}
}

// Check evaluates one call.
//
// Order follows one question: "did the Flow allow this, and is this particular
// invocation still within what a human would expect?"
//  1. posture deny → refuse.
//  2. built-in floor (a destructive shell phrase, a write to .env) → ask, whatever
//     the posture says.
//  3. posture ask → ask.
//  4. otherwise allow.
func (p *DefaultToolPolicy) Check(_ context.Context, req ToolPolicyRequest) (ToolDecision, string, error) {
	name := strings.TrimSpace(req.Call.Name)
	if name == "" {
		return ToolDeny, "tool name is empty", nil
	}
	if types.Governed(name) {
		if p.posture.DecisionFor(name) == types.PermissionDeny {
			return ToolDeny, fmt.Sprintf("permissions: %s in this runtime", p.posture.DescribeTool(name)), nil
		}
	}
	if reason, hit := p.floorHit(name, req.Call.Arguments); hit {
		return ToolRequireApproval, reason, nil
	}
	if types.Governed(name) && p.posture.DecisionFor(name) == types.PermissionAsk {
		// The reason names the tool and its posture: "ask" without a reason is the
		// prompt-with-no-information this design exists to replace.
		return ToolRequireApproval, fmt.Sprintf("permissions: %s in this runtime", p.posture.DescribeTool(name)), nil
	}
	return ToolAllow, "", nil
}

// floorHit reports whether a call is one the floor always asks about.
func (p *DefaultToolPolicy) floorHit(name string, args map[string]any) (string, bool) {
	if name == "Bash" {
		command := strings.ToLower(stringArg(args, "command"))
		if command == "" {
			return "", false
		}
		for _, phrase := range p.phrases {
			if strings.Contains(command, phrase) {
				return fmt.Sprintf("shell command contains %q, which this engine always asks about", phrase), true
			}
		}
	}
	if name == "Write" {
		file := stringArg(args, "file")
		if envPath(file) {
			return fmt.Sprintf("writing %q touches an environment file, which this engine always asks about", file), true
		}
	}
	return "", false
}

// DenyTools reports which governed tools the posture refuses.
func (p *DefaultToolPolicy) DenyTools() []string {
	return p.toolsWith(types.PermissionDeny)
}

func (p *DefaultToolPolicy) toolsWith(want types.PermissionDecision) []string {
	var out []string
	for _, tool := range types.GovernedTools() {
		if p.posture.DecisionFor(tool) == want {
			out = append(out, tool)
		}
	}
	sort.Strings(out)
	return out
}

// Posture exposes the resolved posture for diagnostics.
func (p *DefaultToolPolicy) Posture() types.PermissionsConfig { return p.posture }

// envPath reports whether a file argument names an environment file. It is a code
// rule rather than configuration: a Flow that allows writes is saying "edit the
// code", not "rewrite the secrets".
func envPath(file string) bool {
	file = strings.TrimSpace(file)
	if file == "" {
		return false
	}
	base := path.Base(file)
	// Prefix, not just the exact name: ".env.local" and ".envrc" both hold secrets,
	// and a rule that protects one spelling of the file is a rule people work around.
	return strings.HasPrefix(base, ".env")
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, _ := args[key].(string)
	return value
}

// inScript reports whether the call is a statement inside a REPL program. The
// marker is set by the sandbox right before it calls back into the executor, so
// "inside a program" has exactly one definition.
func inScript(ctx context.Context) bool {
	_, _, ok := script.StepFromContext(ctx)
	return ok
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
	result := make([]types.ContentPart, 0, len(blocks))
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
			result = append(result, clone)
		}
	}
	return result
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
