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
func waitForResultMessage(t *testing.T, rt *Runtime, sessionID, teamID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		box, err := rt.loadMailbox(context.Background(), sessionID)
		if err != nil {
			return false
		}
		for _, msg := range box {
			if msg.Kind == mailboxKindResult && msg.To.TeamID == teamID {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "result message for team %q never arrived", teamID)
}

// waitForDispatchSettled blocks until the whole async chain for a session has
// finished writing: nothing is left pending or claimed, no Team is executing
// any more, and the session is back to an idle status.
//
// It is the settle condition for chains that keep going on their own — a
// dispatched Team whose result wakes another Team, which dispatches in turn.
// Waiting for the first result message is not enough there: the wake starts
// more rounds, and each one writes more session events.
func waitForDispatchSettled(t *testing.T, rt *Runtime, sessionID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		box, err := rt.loadMailbox(context.Background(), sessionID)
		if err != nil {
			return false
		}
		for _, msg := range box {
			if msg.Status == types.MailboxPending || msg.Status == types.MailboxClaimed {
				return false
			}
		}
		if len(rt.liveTeams()) > 0 {
			return false
		}
		session, err := rt.loadSession(context.Background(), sessionID)
		if err != nil {
			return false
		}
		switch session.Status {
		case types.SessionWaitingInput, types.SessionWaitingTool,
			types.SessionWaitingApproval, types.SessionCreated:
			return true
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "async dispatch chain never settled")
}

// waitForDispatchersDone blocks until no dispatched Team is executing any
// more and no message is still being worked on.
//
// Unlike waitForDispatchSettled it tolerates mail that is still pending: a
// wake that arrives while a turn is running is dropped on purpose
// (dispatch.wakeIfIdle leaves the message for the next turn), and a test
// without a waker leaves its results pending by design. Neither case writes
// anything else to the session, so it is safe to return once the dispatch
// goroutines themselves are done.
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
