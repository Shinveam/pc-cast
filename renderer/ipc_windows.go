//go:build windows

package renderer

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
)

// Windows 上 mpv 的 JSON IPC 走命名管道。
func ipcAddress() string {
	return fmt.Sprintf(`\\.\pipe\pc-cast-mpv-%d-%d`, os.Getpid(), nextIPCToken())
}

func dialIPC(addr string) (io.ReadWriteCloser, error) {
	timeout := 2 * time.Second
	return winio.DialPipe(addr, &timeout)
}

func cleanupIPC(_ string) {}
