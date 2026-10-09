//go:build windows

package agent

import "os/exec"

// setHookProcessGroup is a no-op on Windows: the platform has no POSIX process
// groups, so the child is signalled directly. See hooks_process_unix.go for
// why the group exists at all.
//
// Known limitation, the same one internal/mcp documents: a grandchild that
// outlives the shell can hold the captured stdout. cmd.WaitDelay in
// RunHookCommand still bounds the wait, so a timeout returns on time, but such
// a grandchild may survive on Windows.
func setHookProcessGroup(*exec.Cmd) {}

// terminateHookProcess kills the child outright. Windows has no SIGTERM, so
// there is no graceful window and killHookProcess is the same operation.
func terminateHookProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// killHookProcess is terminateHookProcess on Windows.
func killHookProcess(cmd *exec.Cmd) error {
	return terminateHookProcess(cmd)
}
