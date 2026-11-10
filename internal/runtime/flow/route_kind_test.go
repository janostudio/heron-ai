package flow

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestClassifyRoute pins the shared classification both routing sides use.
//
// The two sides execute differently on purpose (in-turn counts coordinate
// retries and finishes the turn; after a dispatch the session is parked), but
// they must agree on which actions exist. They had drifted: a legacy `complete`
// from an older Agent was handled in-turn and rejected as "unsupported next
// action" after a dispatch.
func TestClassifyRoute(t *testing.T) {
	tests := []struct {
		action types.NextAction
		want   routeKind
		known  bool
	}{
		{types.NextProceed, routeForward, true},
		{types.NextReturn, routeReturn, true},
		{types.NextCoordinate, routeCoordinate, true},
		{types.NextFail, routeFail, true},
		{types.NextWaitTool, routeStop, true},
		{types.NextWaitApproval, routeStop, true},
		{types.NextActivate, routeRetiredActivate, true},
		{types.NextAction("complete"), routeStop, true},
		{types.NextAction("wait_input"), routeStop, true},
		{types.NextAction("no_such_action"), routeStop, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.action), func(t *testing.T) {
			kind, known := classifyRoute(tc.action)
			require.Equal(t, tc.known, known, "classifyRoute(%q) known = %v", tc.action, known)
			require.Equal(t, tc.want, kind, "classifyRoute(%q) kind = %v, want %v", tc.action, kind, tc.want)
		})
	}
}
