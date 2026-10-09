//go:build windows

package workspace

import "os/exec"

// setProcessGroup is a no-op on Windows: the platform has no POSIX process
// groups, so the child is signalled directly. See exec_process_unix.go for why
// the group exists at all.
//
// Known limitation, the same one internal/agent and internal/mcp document: a
// grandchild that outlives the shell can still hold the captured pipe.
// cmd.WaitDelay in Run bounds that wait so a timeout returns on time, but such
// a grandchild may survive on Windows.
func setProcessGroup(*exec.Cmd) {}

// killProcessGroup kills the child outright. Windows has no process-group
// equivalent, so grandchildren may survive.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
