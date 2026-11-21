package script

import (
	"errors"
	"fmt"
)

// ApprovalRequest describes one inner call that must be approved before it runs.
//
// It is the unit of the pause: the program stops at this step, the host asks a
// human, and the resumed run skips everything before it (those steps are in the
// replay log) and executes this one under the approval.
type ApprovalRequest struct {
	// ScriptID identifies the program, so the approval can be matched back to the
	// replay log it belongs to.
	ScriptID string
	// Step is the zero-based position of the call inside the program.
	Step int
	// Tool and Args are what the call would have done.
	Tool string
	Args map[string]any
	// Reason is the policy's explanation, shown to the human.
	Reason string
}

// ErrApprovalRequired is returned by a ToolCaller to stop the program at a step
// that needs a human. The sandbox turns it into Execution.Paused; it is never
// surfaced to the script as a tool failure, because the script did not fail — it
// is waiting.
type ErrApprovalRequired struct {
	Request ApprovalRequest
}

func (e *ErrApprovalRequired) Error() string {
	return fmt.Sprintf("tool %q at step %d requires human approval", e.Request.Tool, e.Request.Step)
}

// AsApprovalRequired reports whether err is (or wraps) an approval request.
func AsApprovalRequired(err error) (ApprovalRequest, bool) {
	var target *ErrApprovalRequired
	if errors.As(err, &target) && target != nil {
		return target.Request, true
	}
	return ApprovalRequest{}, false
}
