package flow

import (
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// turnEndpoint resolves the two dimensions of a turn terminal state. The
// session status answers "who is it waiting for"; the run error answers "what
// stopped it". Keeping them separate is the point: the old single enum forced
// "finished and waiting for the user" and "finished and waiting for approval"
// to be different states instead of the same state with a different blocker.
func turnEndpoint(status types.SessionStatus, runErr error) (types.TurnStopReason, types.TurnAwaiting) {
	switch status {
	case types.SessionIdle:
		return types.StopEndTurn, types.AwaitingUser
	case types.SessionWaitingTool:
		return types.StopEndTurn, types.AwaitingTool
	case types.SessionWaitingApproval:
		return types.StopEndTurn, types.AwaitingApproval
	case types.SessionCancelled:
		return types.StopCancelled, ""
	case types.SessionFailed:
		return stopReasonFromError(runErr), ""
	default:
		return types.StopEndTurn, ""
	}
}

// stopReasonFromError classifies why a failed turn stopped. The distinctions
// matter to whoever consumes the event: a budget stop is not worth retrying
// unchanged, a refusal is a model decision, and anything else is a fault.
func stopReasonFromError(err error) types.TurnStopReason {
	if err == nil {
		return types.StopFailed
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "max_model_rounds"), strings.Contains(message, "round limit"):
		return types.StopMaxRounds
	case strings.Contains(message, "max_tool_calls"):
		return types.StopMaxToolCalls
	case strings.Contains(message, "max_input_tokens"),
		strings.Contains(message, "max_output_tokens"),
		strings.Contains(message, "context limit"):
		return types.StopMaxTokens
	case strings.Contains(message, "refusal"):
		return types.StopRefusal
	default:
		return types.StopFailed
	}
}
