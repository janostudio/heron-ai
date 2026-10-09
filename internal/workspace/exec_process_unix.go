//go:build !windows

package workspace

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command's shell into a process group of its own so
// that killing the group also kills whatever the shell forked.
//
// This is what makes a Bash timeout mean anything. The command runs through a
// shell (`sh -c` / `bash -c`), and the shell forks: `sleep 30 & wait` always
// forks, and on Linux a plain command through dash forks too. A forked
// grandchild inherits the captured stdout, so killing only the shell leaves
// that grandchild holding the pipe — cmd.Wait then blocks until the grandchild
// exits on its own, and the declared timeout is a no-op. That is exactly the
// hang in docs/issues/ISSUE-bash-timeout-grandchild-hang.
//
// The group is the child's own (Setpgid makes the pgid equal to the child's
// pid), so signalling it can never reach the engine's own group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup SIGKILLs the whole group the command started. A negative
// pid addresses the group, not the process.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// The group may already be gone; fall back to signalling just the
		// shell so a stale group id never turns into a leaked process.
		return cmd.Process.Kill()
	}
	return nil
}
