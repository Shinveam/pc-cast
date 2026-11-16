//go:build !windows

package renderer

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Unix-like 平台上 mpv 的 JSON IPC 走 Unix domain socket。
func ipcAddress() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("pc-cast-mpv-%d-%d.sock", os.Getpid(), nextIPCToken()))
}

func dialIPC(addr string) (io.ReadWriteCloser, error) {
	return net.DialTimeout("unix", addr, 2*time.Second)
}

func cleanupIPC(addr string) { _ = os.Remove(addr) }
