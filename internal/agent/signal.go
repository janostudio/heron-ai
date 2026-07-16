package agent

import (
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type SignalParser struct{}

func NewSignalParser() *SignalParser {
	return &SignalParser{}
}

// Parse extracts signal from text by looking for XML-style tags
func (p *SignalParser) Parse(text string) types.Signal {
	text = strings.TrimSpace(text)

	if strings.HasSuffix(text, "</continue>") || strings.Contains(text, "<continue/>") {
		return types.SignalContinue
	}
	if strings.HasSuffix(text, "</wait_input>") || strings.Contains(text, "<wait_input/>") {
		return types.SignalWaitInput
	}
	if strings.HasSuffix(text, "</goal_achieved>") || strings.Contains(text, "<goal_achieved/>") {
		return types.SignalGoalAchieved
	}
	if strings.HasSuffix(text, "</goal_failed>") || strings.Contains(text, "<goal_failed/>") {
		return types.SignalGoalFailed
	}
	if strings.HasSuffix(text, "</goal_impossible>") || strings.Contains(text, "<goal_impossible/>") {
		return types.SignalGoalImpossible
	}

	return ""
}

// ParseWithMode parses signal, using loop mode as default fallback
func (p *SignalParser) ParseWithMode(text string, loopMode bool) (types.Signal, string) {
	signal := p.Parse(text)
	if signal != "" {
		// Strip signal tags from text
		clean := strings.TrimSpace(text)
		for _, tag := range []string{"<continue/>", "</continue>", "<wait_input/>", "</wait_input>",
			"<goal_achieved/>", "</goal_achieved>", "<goal_failed/>", "</goal_failed>",
			"<goal_impossible/>", "</goal_impossible>"} {
			clean = strings.TrimSuffix(clean, tag)
			clean = strings.ReplaceAll(clean, tag, "")
		}
		return signal, strings.TrimSpace(clean)
	}

	// Default: loop mode returns wait_input, non-loop returns continue
	if loopMode {
		return types.SignalWaitInput, text
	}
	return types.SignalContinue, text
}
