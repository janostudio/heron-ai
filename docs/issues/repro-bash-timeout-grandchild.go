package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// 复现：exec.CommandContext 超时后是否真的返回
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// bash -c 里再派生一个子进程（模拟 grep），并让子进程持有 stdout
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", "sleep 30 & sleep 30")
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	fmt.Printf("耗时=%v  err=%v  ctx.Err=%v\n", elapsed.Round(time.Millisecond), err, ctx.Err())
	fmt.Printf("输出=%q\n", string(out))
}
