//go:build windows

package mcp

import "os/exec"

// setProcessGroup is a no-op on Windows: the platform has no POSIX process
// groups, so the child is signalled directly. See process_unix.go for why the
// group exists at all.
func setProcessGroup(*exec.Cmd) {}

// terminateProcess kills the child outright. Windows has no SIGTERM, so a
// graceful window is not available and killProcess is the same operation.
func terminateProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// killProcess is terminateProcess on Windows.
func killProcess(cmd *exec.Cmd) error {
	return terminateProcess(cmd)
}
