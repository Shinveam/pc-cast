//go:build windows

package bundled

import _ "embed"

// mpvWindowsExeGZ 是 shinchiro 社区构建 mpv（Windows amd64）的 gzip 压缩体。
// 版本：20261111 / mpv v0.42.0-38-gbcde6b93c，上游发布页：
// https://github.com/shinchiro/mpv-winbuild-cmake/releases/tag/20261111
// 更新二进制时替换本文件并同步 THIRD_PARTY_NOTICES.md 的版本号。
//
//go:embed data/mpv-windows-amd64.exe.gz
var mpvWindowsExeGZ []byte

func init() {
	source.name = exeName
	source.data = mpvWindowsExeGZ
	source.ok = len(mpvWindowsExeGZ) > 0
}
