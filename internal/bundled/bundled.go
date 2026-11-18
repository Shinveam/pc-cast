// Package bundled 管理随程序一起分发的内置 mpv 播放器。
//
// 二进制以 gzip 压缩后 go:embed 编进可执行文件，首次运行时解压到用户配置
// 目录下的稳定缓存位置，后续启动直接复用。解压目录名带内容版本，升级时
// 旧版本目录自然失效；解压采用「临时文件 + 原子改名」避免半写入被复用。
package bundled

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	appName   = "pc-cast"
	dirName   = "bundled-mpv"
	exeName   = "mpv.exe"
	tmpPrefix = ".mpv-extract-"
)

var (
	once sync.Once
	path string
	err  error
)

// source 由各平台文件提供：name 是压缩数据里的可执行文件名，data 是 gzip 字节。
// 未内置播放器的平台（当前为 darwin/linux）返回 ok=false。
var source struct {
	name string
	data []byte
	ok   bool
}

// Path 返回内置 mpv 可执行文件路径；该平台未内置或解压失败时返回错误。
// 结果在进程内缓存，解压只发生一次，可安全并发调用。
func Path() (string, error) {
	once.Do(func() { path, err = ensure() })
	return path, err
}

// Available 报告当前平台是否随程序内置了 mpv（不做解压）。
func Available() bool { return source.ok }

func cacheRoot() (string, error) {
	base, e := os.UserConfigDir()
	if e != nil {
		return "", fmt.Errorf("定位配置目录失败: %w", e)
	}
	return filepath.Join(base, appName, dirName), nil
}

// version 用压缩数据的 SHA256 前 16 位，内容变则目录变，天然支持升级与去重。
func version() string {
	sum := sha256.Sum256(source.data)
	return hex.EncodeToString(sum[:8])
}

func ensure() (string, error) {
	if !source.ok || len(source.data) == 0 {
		return "", errors.New("当前平台未内置 mpv 播放器")
	}
	root, e := cacheRoot()
	if e != nil {
		return "", e
	}
	return extractInto(source.data, filepath.Join(root, version()))
}

// extractInto 把 gzip 数据解压到 dir 下的 exeName，已存在则复用，
// 解压走「临时文件 + 原子改名」。dir 可注入，便于单测覆盖解压与复用两条路径。
func extractInto(data []byte, dir string) (string, error) {
	target := filepath.Join(dir, exeName)
	if st, stErr := os.Stat(target); stErr == nil && !st.IsDir() && st.Size() > 0 {
		return target, nil
	}
	if e := os.MkdirAll(dir, 0o755); e != nil {
		return "", fmt.Errorf("创建播放器缓存目录失败: %w", e)
	}

	tmp, e := os.CreateTemp(dir, tmpPrefix)
	if e != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", e)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	gr, e := gzip.NewReader(bytes.NewReader(data))
	if e != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("读取内置播放器数据失败: %w", e)
	}
	_, e = io.Copy(tmp, gr)
	closeErr := gr.Close()
	_ = tmp.Close()
	if e != nil || closeErr != nil {
		return "", fmt.Errorf("解压内置播放器失败: %v %v", e, closeErr)
	}
	if e := os.Rename(tmpPath, target); e != nil {
		return "", fmt.Errorf("安装内置播放器失败: %w", e)
	}
	return target, nil
}
