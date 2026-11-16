//go:build windows

package renderer

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW：主程序是 GUI 子系统，启动 console 类子进程（mpv、ffplay、
// 版本探测等）时若不设置该标志，Windows 会额外弹出一个黑色控制台窗口。
const createNoWindow = 0x08000000

func prepareCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
