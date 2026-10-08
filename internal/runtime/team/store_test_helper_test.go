package team

import (
	"github.com/heron-ai/heron-engine/internal/runtime/call"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// newTestRuntime builds a Team runtime over a bare agent map. The store is the
// production constructor's only input; wrapping the map here, in test code,
// keeps the old signature out of production while leaving every existing test
// call site readable.
//
// root/flow are empty: these fixtures describe no config root on disk, and a
// store without them reports "reload is not wired" instead of pretending to
// reload files that were never loaded.
func newTestRuntime(executors *call.Registry, agents map[string]types.AgentConfig) *Runtime {
	return NewRuntime(executors, newTestDefinitionStore(agents))
}

// newTestDefinitionStore is the map-wrapping store itself, for the tests that
// construct a Spawn tool directly.
func newTestDefinitionStore(agents map[string]types.AgentConfig) *types.DefinitionStore {
	return types.NewDefinitionStore(&types.Definitions{Agents: agents}, "", "")
}
