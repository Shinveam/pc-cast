//go:build !windows

package renderer

import "os/exec"

// Unix-like 平台无需为子进程隐藏控制台窗口。
func prepareCommand(_ *exec.Cmd) {}
