package flow

import (
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// newTestRuntime wraps a definitions fixture in a store so every existing test
// keeps passing a bare tree. The store is the production constructor's only
// input; the wrapper lives here, in test code, precisely so no production
// compatibility shim has to exist for the old signature.
//
// root/flow are left empty: these fixtures use embedded flows with no config
// root on disk, and a store without them reports "reload is not wired" rather
// than pretending to reload files that were never loaded.
func newTestRuntime(
	defs *types.Definitions,
	teamRuntime types.TeamRuntime,
	sessionWriter storage.SessionWriter,
	evidenceStore storage.EvidenceStore,
) *Runtime {
	return NewRuntime(
		types.NewDefinitionStore(defs, "", ""),
		teamRuntime,
		sessionWriter,
		evidenceStore,
	)
}

// newTestRuntimeWithStore is the store-taking form, for tests that need to
// publish a new tree mid-test.
func newTestRuntimeWithStore(
	store *types.DefinitionStore,
	teamRuntime types.TeamRuntime,
	sessionWriter storage.SessionWriter,
	evidenceStore storage.EvidenceStore,
) *Runtime {
	return NewRuntime(store, teamRuntime, sessionWriter, evidenceStore)
}
