package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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

// waitForResultMessage blocks until the background dispatcher has recorded the
// result message addressed to teamID.
//
// The dispatcher owns its own goroutine lifetime (dispatch.go) and keeps
// writing after the dispatched Team's round returns: the work message's
// delivered event, then the result message it posts back. A test that returns
// as soon as its own assertion is satisfied races those writes against
// t.TempDir()'s RemoveAll, which fails with "directory not empty" on Linux.
// The result message is the last write of a round (scheduler.go's
// runMessageTurn), so waiting for it removes the race.

func waitForDispatchersDone(t *testing.T, rt *Runtime, sessionID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		if len(rt.liveTeams()) > 0 {
			return false
		}
		box, err := rt.loadMailbox(context.Background(), sessionID)
		if err != nil {
			return false
		}
		for _, msg := range box {
			// Claimed means some dispatcher is still working on it.
			if msg.Status == types.MailboxClaimed {
				return false
			}
		}
		return true
	}, 10*time.Second, 10*time.Millisecond, "dispatched teams are still running")
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
