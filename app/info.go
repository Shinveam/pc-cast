package app

import (
	"fmt"
	"runtime"
	"strings"
)

// 由构建脚本通过 -ldflags -X 注入；Commit 缺省为专属构建标识。
var (
	Version   = "0.1.0"
	Commit    = "Shinveam"
	BuildTime = "unknown"
)

// Info 是"软件信息"窗口展示的数据。
type Info struct {
	Name      string
	Version   string
	Commit    string
	BuildTime string
	GoVersion string
	OS        string
	Arch      string
	Protocol  string
	Renderer  string
	UIToolkit string
}

func BuildInfo() Info {
	return Info{
		Name:      "PC-Cast",
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Protocol:  "DLNA / UPnP MediaRenderer:1 (AVTransport, RenderingControl, ConnectionManager)",
		Renderer:  "按运行环境自动选择（mpv / VLC / 其它本机播放器 / 系统默认）",
		UIToolkit: "mygo (native UI, pure Go)",
	}
}

// Lines 返回用于界面展示的键值行。
func (i Info) Lines() [][2]string {
	return [][2]string{
		{"名称", i.Name},
		{"版本", i.Version},
		{"构建", fmt.Sprintf("%s @ %s", i.Commit, i.BuildTime)},
		{"Go", i.GoVersion},
		{"平台", i.OS + "/" + i.Arch},
		{"协议", i.Protocol},
		{"播放策略", i.Renderer},
		{"界面", i.UIToolkit},
	}
}

func (i Info) Summary() string {
	var b strings.Builder
	for n, kv := range i.Lines() {
		if n > 0 {
			b.WriteString("\n")
		}
		b.WriteString(kv[0] + ": " + kv[1])
	}
	return b.String()
}
