package flow

import "github.com/heron-ai/heron-engine/pkg/types"

// routeKind is what a Team's next action means, independent of who is asking.
//
// The routing decision is consumed twice: inside a Flow turn, where a route
// forwards the result to another Team in the same turn, and after a dispatched
// round, where it hands the result back through the mailbox. Those two sides
// execute differently on purpose — the in-turn side counts coordinate retries,
// the dispatched side parks the session instead of finishing it — but they have
// to agree on *which actions exist*. They had drifted: an older Agent answering
// `complete` / `wait_input` was handled in-turn and rejected as
// "unsupported next action" after a dispatch, so the same response failed
// depending on how it was reached.
//
// Classification lives here; the execution stays on each side.
type routeKind int

const (
	// routeStop ends this hop without forwarding: the round is waiting for a
	// person, a Tool task or an approval, or the Agent described its own
	// completion with one of the legacy lifecycle actions.
	routeStop routeKind = iota
	// routeForward sends the result along the binding's fixed route.
	routeForward
	// routeReturn sends the result back to whoever activated the Team.
	routeReturn
	// routeCoordinate asks the activator to decide what to do next.
	routeCoordinate
	// routeFail reports a Team-level failure to the activator.
	routeFail
	// routeRetiredActivate is the removed path where a Team asked the Flow to
	// start another Team. It is recognised so the error can say what replaced
	// it (the Team tool), instead of "unsupported next action".
	routeRetiredActivate
)

// classifyRoute maps a next action to its kind. ok is false for an action this
// engine does not know: that is a protocol error, not a stop.
func classifyRoute(action types.NextAction) (kind routeKind, ok bool) {
	switch action {
	case types.NextProceed:
		return routeForward, true
	case types.NextReturn:
		return routeReturn, true
	case types.NextCoordinate:
		return routeCoordinate, true
	case types.NextFail:
		return routeFail, true
	case types.NextWaitTool, types.NextWaitApproval:
		return routeStop, true
	case types.NextActivate:
		return routeRetiredActivate, true
	case types.NextAction("complete"), types.NextAction("wait_input"):
		// Lifecycle values from older Agent responses. They describe the Agent
		// finishing, not an orchestration step: nothing to forward, nothing to
		// fail.
		return routeStop, true
	}
	return routeStop, false
}
