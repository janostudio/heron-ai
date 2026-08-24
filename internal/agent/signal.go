package agent

import (
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// RouteParser converts the small, model-facing XML markers used by the
// built-in prompt into the core Route protocol. The runtime does not expose a
// separate Signal domain object.
type RouteParser struct{}

func NewRouteParser() *RouteParser {
	return &RouteParser{}
}

func (p *RouteParser) Parse(text string) types.NextAction {
	text = strings.TrimSpace(text)
	switch {
	case strings.HasSuffix(text, "</continue>") || strings.Contains(text, "<continue/>"):
		return types.NextProceed
	case strings.HasSuffix(text, "</wait_input>") || strings.Contains(text, "<wait_input/>"):
		return types.NextWaitInput
	case strings.HasSuffix(text, "</goal_achieved>") || strings.Contains(text, "<goal_achieved/>"):
		return types.NextComplete
	case strings.HasSuffix(text, "</goal_failed>") || strings.Contains(text, "<goal_failed/>"):
		return types.NextFail
	case strings.HasSuffix(text, "</goal_impossible>") || strings.Contains(text, "<goal_impossible/>"):
		return types.NextFail
	default:
		return ""
	}
}

// ParseWithMode returns the route action and clean model text. An omitted
// action means "wait for the user" in a bounded multi-round Subagent loop,
// and "proceed" in a single-round execution.
func (p *RouteParser) ParseWithMode(text string, loopMode bool) (types.NextAction, string) {
	action := p.Parse(text)
	if action != "" {
		clean := strings.TrimSpace(text)
		for _, tag := range []string{
			"<continue/>", "</continue>",
			"<wait_input/>", "</wait_input>",
			"<goal_achieved/>", "</goal_achieved>",
			"<goal_failed/>", "</goal_failed>",
			"<goal_impossible/>", "</goal_impossible>",
		} {
			clean = strings.TrimSuffix(clean, tag)
			clean = strings.ReplaceAll(clean, tag, "")
		}
		return action, strings.TrimSpace(clean)
	}
	if loopMode {
		return types.NextWaitInput, text
	}
	return types.NextProceed, text
}
