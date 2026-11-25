package view

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pageScript is the embedded controller the browser runs. Reading it from the
// embed FS (rather than from disk) keeps the test on the bytes that ship.
func pageScript(t *testing.T) string {
	t.Helper()
	raw, err := webAssets.ReadFile("webassets/app.js")
	require.NoError(t, err)
	return string(raw)
}

// TestStopButtonIsWired pins the one property a reader cannot see from the
// rendered page: a button that exists and is styled is not a button that works.
// The stop control was created, hidden and shown by CSS for weeks while nothing
// listened to its clicks, so every click was silent — no request, no error, and
// nothing in the session log to explain it.
func TestStopButtonIsWired(t *testing.T) {
	script := pageScript(t)
	require.Contains(t, script, `stopButton.addEventListener("click", stop)`,
		"the stop control must have a click handler; without one it is a decoration")
}

// TestStopCallsCancelEndpoint pins the other half: the handler has to reach the
// API that actually stops the turn, not just repaint the page.
func TestStopCallsCancelEndpoint(t *testing.T) {
	script := pageScript(t)
	require.Contains(t, script, `"/api/cancel?session_id="`,
		"stopping has to ask the runtime to cancel, not only drop the client stream")
	require.Contains(t, script, `method: "POST"`,
		"the cancel route only accepts POST")
}

// TestStopDoesNotDependOnTurnClass keeps the handler off the styling hook. The
// button's visibility is driven by body.turn-active, so reading that class inside
// the handler makes the click depend on a CSS rule: any repaint that clears the
// class silently disables stopping while the turn keeps running.
func TestStopDoesNotDependOnTurnClass(t *testing.T) {
	script := pageScript(t)
	start := strings.Index(script, "function stop() {")
	require.GreaterOrEqual(t, start, 0, "stop() must exist")
	end := strings.Index(script[start:], "\n    }")
	require.Greater(t, end, 0)
	body := script[start : start+end]
	require.NotContains(t, body, "turn-active",
		"stop() must not gate on the class that only decides whether the button is painted")
}

// TestSubmitReportsWhereTheMessageWent pins the feedback the composer owes after a
// send. A message that leaves the box with no visible outcome is indistinguishable
// from one that was dropped, which is what made a steered turn look broken: the
// server had taken the text and the page said nothing.
func TestSubmitReportsWhereTheMessageWent(t *testing.T) {
	script := pageScript(t)
	require.Contains(t, script, "accepted.steered",
		"the composer has to read steered, or a message handed to the running turn looks ignored")
	require.Contains(t, script, "accepted.queued",
		"and it has to say when the message is waiting for the next turn instead")
}

// TestInjectedInputIsVisibleInTheTranscript: the correction is part of the turn's
// story. Rendering only the answer makes the turn look like it changed direction on
// its own.
func TestInjectedInputIsVisibleInTheTranscript(t *testing.T) {
	script := pageScript(t)
	require.Equal(t, 2, strings.Count(script, `"session.user_input.injected"`),
		"both renderers (live turn and folded history) must show the injected message")
}
