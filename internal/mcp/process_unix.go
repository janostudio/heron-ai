//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child into a process group of its own so
// terminateProcess can signal the whole tree.
//
// This is the guard against orphaned servers. An MCP stdio server is usually
// launched through a wrapper — `npx -y @modelcontextprotocol/server-github`
// runs npx, which then execs or forks the real server. Killing only the direct
// child reaps npx and leaves the server holding the pipe (and whatever port or
// lock it took) alive for the rest of the machine's uptime. Signalling the
// group reaches both.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcess asks the group to exit. The caller must still call
// cmd.Wait(); that is what reaps the child, and it returns once the group is
// gone.
func terminateProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// A negative pid addresses the process group, not the process.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// killProcess is the SIGKILL escalation used when the group ignored SIGTERM.
func killProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
