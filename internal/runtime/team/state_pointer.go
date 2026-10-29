package team

import "github.com/heron-ai/heron-engine/internal/state"

// agentStatePointer renders the pointer block that replaces snapshot injection.
//
// It is a wrapper rather than a direct call because Run's local runState
// variable is named "state", which shadows the package name inside runtime.go.
func agentStatePointer(agentID string) string {
	return state.PointerText(agentID)
}
