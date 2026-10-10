package workspace

import (
	"strings"
	"testing"
)

// 保证注入的全局 env 落到子进程：起一个 echo 子进程验证 $AUTO 可见。
func TestLocalWorkspaceInjectsEnvIntoCommand(t *testing.T) {
	root := t.TempDir()
	ws, err := NewLocal(root, map[string]string{"AUTO": "/injected/workspace"})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	res, err := ws.Run(t.Context(), CommandRequest{Command: "printf '%s' \"$AUTO\"", Shell: "/bin/sh"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "/injected/workspace" {
		t.Fatalf("stdout = %q, want /injected/workspace", res.Stdout)
	}
}

// 无注入时子进程仍能读到进程环境（向后兼容：req.Env 为空走 os.Environ()）。
func TestLocalWorkspaceWithoutInjectionInheritsProcessEnv(t *testing.T) {
	const key = "HERON_WORKSPACE_INHERIT_TEST"
	t.Setenv(key, "inherited")

	ws, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	res, err := ws.Run(t.Context(), CommandRequest{Command: "printf '%s' \"$" + key + "\"", Shell: "/bin/sh"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "inherited" {
		t.Fatalf("stdout = %q, want inherited", res.Stdout)
	}
}
