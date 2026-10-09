package workspace

import "os/exec"

// SetProcessGroup puts cmd in its own process group so KillProcessGroup can
// signal the command and everything it forked. Exported for callers that spawn
// processes outside the workspace backend (e.g. the CodeNav helper) but need
// the same "kill the whole tree, not just the shell" guarantee that
// ISSUE-bash-timeout-grandchild-hang is about.
func SetProcessGroup(cmd *exec.Cmd) { setProcessGroup(cmd) }

// KillProcessGroup SIGKILLs the process group of a command started with
// SetProcessGroup.
func KillProcessGroup(cmd *exec.Cmd) error { return killProcessGroup(cmd) }
