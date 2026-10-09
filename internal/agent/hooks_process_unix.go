//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
)

// setHookProcessGroup puts the hook's shell into a process group of its own so
// terminateHookProcess can signal the shell and everything it forked.
//
// This is what makes a hook timeout mean anything. A hook command is a shell
// command, and the shell may fork: `sleep 30 & wait` always does, and on Linux
// a plain command does too. A forked child inherits the captured stdout, so
// killing only the shell leaves that child holding the pipe — cmd.Wait then
// blocks until the child exits on its own, and the timeout is a no-op. That is
// exactly what passed on macOS and failed on Linux CI.
//
// The group is the child's own (Setpgid makes the pgid equal to the child's
// pid), so signalling it can never reach the engine's own group.
func setHookProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateHookProcess asks the group to exit. The caller must still wait;
// that is what reaps the shell, and it returns once the group is gone.
func terminateHookProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// A negative pid addresses the process group, not the process.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// killHookProcess is the SIGKILL escalation for a group that ignored SIGTERM.
func killHookProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
