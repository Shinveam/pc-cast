// Package renderer 定义 DLNA 层使用的播放器抽象，并按运行环境自动选择可用后端。
package renderer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"pc-cast/internal/bundled"
)

// Capability 描述一个播放后端能被 DLNA 层控制到什么程度。
type Capability string

const (
	// CapFull 有双向 IPC（mpv JSON IPC / VLC HTTP Lua），状态、进度、暂停、跳转、音量都是真实值。
	CapFull Capability = "full"
	// CapLaunch 只能拉起进程播放，暂停/跳转靠「记位置 + 重启进程」模拟，进度为墙钟估算。
	CapLaunch Capability = "launch"
	// CapSystem 把 URL 交给系统默认程序，无法停止、无法跟踪退出。
	CapSystem Capability = "system"
	// CapNone 没有任何可用后端，DLNA 服务仍可被发现，但 SetAVTransportURI 会明确报错。
	CapNone Capability = "none"
)

func (c Capability) Label() string {
	switch c {
	case CapFull:
		return "完整控制"
	case CapLaunch:
		return "基础播放"
	case CapSystem:
		return "系统默认"
	default:
		return "不可用"
	}
}

func capabilityNote(c Capability) string {
	switch c {
	case CapFull:
		return "双向 IPC（mpv JSON IPC / VLC HTTP Lua）：状态、进度、暂停/跳转/音量均为真实值"
	case CapLaunch:
		return "基础模式：可播放；暂停与跳转通过重启播放器实现，进度为估算值"
	case CapSystem:
		return "交给系统默认程序打开：无法暂停、停止或上报真实进度"
	default:
		return "未找到可用播放器，请在设置中指定路径"
	}
}

const (
	// ModeAuto 按能力优先级自动选择。
	ModeAuto = "auto"
	// ModeCustom 使用 PlayerPath 指定的可执行文件。
	ModeCustom = "custom"
)

// Descriptor 是当前实际生效的播放后端，供托盘、设置页与软件信息展示。
type Descriptor struct {
	ID         string
	Name       string
	Path       string
	Version    string
	Capability Capability
	Note       string
}

// described 是可选接口：不让 Player 接口因展示需求而被迫扩张，dlna 层零改动。
type described interface {
	Descriptor() Descriptor
}

// Describe 从任意 Player 提取展示信息；未实现的后端按完整能力兜底。
func Describe(p Player) Descriptor {
	if p == nil {
		return Descriptor{ID: "none", Name: "未就绪", Capability: CapNone, Note: capabilityNote(CapNone)}
	}
	if d, ok := p.(described); ok {
		return d.Descriptor()
	}
	return Descriptor{
		ID: "builtin", Name: "内置播放器", Path: p.Path(), Version: p.Version(),
		Capability: CapFull, Note: capabilityNote(CapFull),
	}
}

// candidate 是一个播放后端的静态定义。
type candidate struct {
	id            string
	name          string
	names         []string // 交给 exec.LookPath 的可执行文件名
	paths         []string // 常见安装绝对路径
	capability    Capability
	args          []string // 打开 URL 之前附加的固定参数
	fullscreenArg string   // 全屏开关（放在 URL 之前）
	// fullscreenPostArgs 是全屏时必须放在 URL 之后的开关。
	// WMP (`wmplayer "file" /fullscreen`) 与 MPC-HC (`mpc-hc.exe "pathname" [switches]`)
	// 的官方语法都要求开关位于媒体路径之后，写成前置参数会被忽略。
	fullscreenPostArgs []string
	geometryArgs       func(int, int) []string      // 窗口尺寸参数；nil 表示该后端无法指定
	seekArgs           func(time.Duration) []string // 恢复播放位置的参数；nil 表示不支持
	versionArgs        []string
}

// hasFullscreen 报告该后端是否能通过命令行进入全屏。
func (c candidate) hasFullscreen() bool {
	return c.fullscreenArg != "" || len(c.fullscreenPostArgs) > 0
}

// 各后端的窗口尺寸参数。mpv / ffplay 能精确控制窗口，VLC 的 --width/--height
// 在部分版本只影响视频输出尺寸，因此只作为尽力而为的设置。
func mpvGeometry(w, h int) []string {
	return []string{fmt.Sprintf("--geometry=%dx%d", w, h)}
}

func vlcGeometry(w, h int) []string {
	return []string{fmt.Sprintf("--width=%d", w), fmt.Sprintf("--height=%d", h)}
}

func ffplayGeometry(w, h int) []string {
	return []string{"-x", fmt.Sprintf("%d", w), "-y", fmt.Sprintf("%d", h)}
}

// ModeBuiltin 是「内置播放器」的伪 ID：它不对应 catalog 里某个静态条目，
// 由 choose 在 auto 分支优先注入，用户也可显式指定强制使用内置。
const ModeBuiltin = "builtin"

// catalogFn 可替换，便于单元测试注入其它平台的候选清单。
var catalogFn = defaultCatalog

func defaultCatalog() []candidate {
	switch runtime.GOOS {
	case "windows":
		return windowsCatalog()
	case "darwin":
		return darwinCatalog()
	default:
		return linuxCatalog()
	}
}

func windowsCatalog() []candidate {
	dirs := envDirs("ProgramFiles", "ProgramW6432", "ProgramFiles(x86)", "LOCALAPPDATA")
	join := func(rel string) []string {
		out := make([]string, 0, len(dirs)+2)
		for _, d := range dirs {
			out = append(out, filepath.Join(d, filepath.FromSlash(rel)))
		}
		out = append(out,
			`C:\mpv\mpv.exe`,
			`C:\Program Files\mpv\mpv.exe`,
		)
		return out
	}
	vlc := join("VideoLAN/VLC/vlc.exe")
	pot := join("DAUM/PotPlayer/PotPlayerMini64.exe")
	pot = append(pot, join("DAUM/PotPlayer/PotPlayerMini.exe")...)
	mpc := join("MPC-BE/mpc-be64.exe")
	mpc = append(mpc, join("MPC-HC/mpc-hc64.exe")...)

	seekJoined := func(flag string) func(time.Duration) []string {
		return func(d time.Duration) []string {
			return []string{fmt.Sprintf("%s%.3f", flag, d.Seconds())}
		}
	}
	return []candidate{
		{id: "mpv", name: "mpv", names: []string{"mpv", "mpv.exe"}, paths: join("mpv/mpv.exe"),
			capability: CapFull, fullscreenArg: "--fs", geometryArgs: mpvGeometry,
			seekArgs: seekJoined("--start="), versionArgs: []string{"--version"}},
		{id: "vlc", name: "VLC", names: []string{"vlc", "vlc.exe"}, paths: vlc,
			capability: CapFull, args: []string{"--no-video-title-show"},
			fullscreenArg: "--fullscreen", geometryArgs: vlcGeometry,
			seekArgs: seekJoined("--start-time="), versionArgs: []string{"--version"}},
		{id: "potplayer", name: "PotPlayer", names: []string{"PotPlayerMini64.exe", "PotPlayerMini.exe"}, paths: pot,
			capability: CapLaunch, fullscreenArg: "/fullscreen", versionArgs: nil},
		{id: "mpc", name: "MPC-BE / MPC-HC", paths: mpc, capability: CapLaunch,
			fullscreenPostArgs: []string{"/fullscreen"}},
		{id: "ffplay", name: "ffplay", names: []string{"ffplay", "ffplay.exe"},
			capability: CapLaunch, fullscreenArg: "-fs", geometryArgs: ffplayGeometry,
			seekArgs: func(d time.Duration) []string {
				return []string{"-ss", fmt.Sprintf("%.3f", d.Seconds())}
			}, versionArgs: []string{"-version"}},
		// Windows 自带的桌面播放器，能直接打开 http 视频 URL；
		// 作为较弱的 CapLaunch 候选排在完整控制后端之后，且不支持 HLS 直播。
		{id: "wmplayer", name: "Windows Media Player",
			paths: []string{
				filepath.Join(envDirs("ProgramFiles")[0], "Windows Media Player", "wmplayer.exe"),
				`C:\Program Files\Windows Media Player\wmplayer.exe`,
				`C:\Program Files (x86)\Windows Media Player\wmplayer.exe`,
			}, capability: CapLaunch, fullscreenPostArgs: []string{"/fullscreen"}},
	}
}

func darwinCatalog() []candidate {
	return []candidate{
		{id: "mpv", name: "mpv", names: []string{"mpv"},
			paths:      []string{"/Applications/mpv.app/Contents/MacOS/mpv", "/opt/homebrew/bin/mpv", "/usr/local/bin/mpv"},
			capability: CapFull, fullscreenArg: "--fs", geometryArgs: mpvGeometry, versionArgs: []string{"--version"}},
		{id: "iina", name: "IINA", paths: []string{"/Applications/IINA.app/Contents/MacOS/IINA"}, capability: CapLaunch},
		{id: "vlc", name: "VLC", names: []string{"vlc"},
			paths: []string{"/Applications/VLC.app/Contents/MacOS/VLC"}, capability: CapFull,
			fullscreenArg: "--fullscreen", geometryArgs: vlcGeometry,
			seekArgs: func(d time.Duration) []string {
				return []string{fmt.Sprintf("--start-time=%.3f", d.Seconds())}
			}, versionArgs: []string{"--version"}},
		{id: "ffplay", name: "ffplay", names: []string{"ffplay"}, capability: CapLaunch,
			fullscreenArg: "-fs", geometryArgs: ffplayGeometry, versionArgs: []string{"-version"}},
	}
}

func linuxCatalog() []candidate {
	return []candidate{
		{id: "mpv", name: "mpv", names: []string{"mpv"},
			paths:      []string{"/usr/bin/mpv", "/usr/local/bin/mpv", "/snap/bin/mpv"},
			capability: CapFull, fullscreenArg: "--fs", geometryArgs: mpvGeometry, versionArgs: []string{"--version"}},
		{id: "vlc", name: "VLC", names: []string{"vlc"}, capability: CapFull,
			fullscreenArg: "--fullscreen", geometryArgs: vlcGeometry,
			seekArgs: func(d time.Duration) []string {
				return []string{fmt.Sprintf("--start-time=%.3f", d.Seconds())}
			}, versionArgs: []string{"--version"}},
		{id: "ffplay", name: "ffplay", names: []string{"ffplay"}, capability: CapLaunch,
			fullscreenArg: "-fs", geometryArgs: ffplayGeometry, versionArgs: []string{"-version"}},
		{id: "celluloid", name: "Celluloid", names: []string{"celluloid"}, capability: CapLaunch},
		{id: "totem", name: "Totem", names: []string{"totem"}, capability: CapLaunch},
	}
}

func envDirs(keys ...string) []string {
	var out []string
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		out = append(out, `C:\Windows`)
	}
	return out
}

// builtinCandidate 是内置播放器的动态候选。它不在静态 catalog 里，
// 由 withBuiltin 在 auto / 显式内置模式下插到候选链最前。
func builtinCandidate() candidate {
	return candidate{
		id: ModeBuiltin, name: "内置 mpv",
		capability: CapFull,
	}
}

// withBuiltin 在候选链最前插入内置播放器（仅当该平台确实内置了二进制）。
// macOS / Linux 无内置数据，返回原链，行为与此前完全一致。
func withBuiltin(cands []candidate) []candidate {
	if !bundled.Available() {
		return cands
	}
	out := make([]candidate, 0, len(cands)+1)
	out = append(out, builtinCandidate())
	return append(out, cands...)
}

// probe 找到候选的可执行文件；不执行任何外部命令，因此可以在 UI 线程安全调用。
// 内置候选是唯一例外：它只需检查数据是否存在，解压推迟到真正构建播放器时。
func probe(c candidate) (string, bool) {
	if c.id == ModeBuiltin {
		if bundled.Available() {
			return "builtin:mpv", true // 占位路径；真实路径由 buildPlayer 取
		}
		return "", false
	}
	for _, n := range c.names {
		if p, err := exec.LookPath(n); err == nil && p != "" {
			return p, true
		}
	}
	for _, p := range c.paths {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// Available 返回本机探测到的可用后端，顺序即自动选择优先级。
func Available() []Descriptor {
	out := make([]Descriptor, 0, len(catalogFn()))
	for _, c := range catalogFn() {
		if p, ok := probe(c); ok {
			out = append(out, Descriptor{
				ID: c.id, Name: c.name, Path: p, Capability: c.capability,
				Note: capabilityNote(c.capability),
			})
		}
	}
	return out
}

// selection 是 choose 的结果。Fallback 非空表示用户指定的后端不可用、已自动降级。
type selection struct {
	cand     candidate
	path     string
	fallback string
}

// choose 决定使用哪个后端。probeFn 可注入以便单元测试覆盖整条降级链。
func choose(mode, customPath string, cands []candidate, probeFn func(candidate) (string, bool)) selection {
	fallback := ""
	if mode != "" && mode != ModeAuto && mode != ModeCustom {
		for _, c := range cands {
			if c.id != mode {
				continue
			}
			if p, ok := probeFn(c); ok {
				return selection{cand: c, path: p}
			}
			fallback = fmt.Sprintf("指定的播放器 %s 不可用，已自动选择其它后端", c.name)
			break
		}
	}
	if mode == ModeCustom {
		p := strings.TrimSpace(customPath)
		if p != "" {
			if abs, err := exec.LookPath(p); err == nil {
				p = abs
			}
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				name := filepath.Base(p)
				lower := strings.ToLower(name)
				cap := CapLaunch
				ver := []string{"--version"}
				if strings.Contains(lower, "mpv") || strings.Contains(lower, "vlc") {
					cap = CapFull
				}
				c := candidate{id: "custom", name: name + "（自定义）", capability: cap,
					paths: []string{p}, versionArgs: ver, fullscreenArg: launchFullscreenArg(name),
					fullscreenPostArgs: customFullscreenPostArgs(name)}
				switch {
				case strings.Contains(lower, "mpv"):
					c.geometryArgs = mpvGeometry
				case strings.Contains(lower, "vlc"):
					c.geometryArgs = vlcGeometry
				}
				if cap == CapFull && strings.Contains(lower, "mpv") {
					c.seekArgs = func(d time.Duration) []string {
						return []string{fmt.Sprintf("--start=%.3f", d.Seconds())}
					}
				} else if strings.Contains(lower, "vlc") {
					c.seekArgs = func(d time.Duration) []string {
						return []string{fmt.Sprintf("--start-time=%.3f", d.Seconds())}
					}
				}
				return selection{cand: c, path: p}
			}
			fallback = "自定义播放器路径不存在，已自动选择其它后端"
		}
	}
	for _, c := range cands {
		if p, ok := probeFn(c); ok {
			return selection{cand: c, path: p, fallback: fallback}
		}
	}
	if fallback == "" {
		fallback = "未找到任何可用播放器"
	}
	return selection{cand: candidate{id: "none", name: "无可用播放器", capability: CapNone},
		fallback: fallback}
}

// customFullscreenPostArgs 让自定义路径下的 WMP / MPC 也遵守「开关在路径之后」。
func customFullscreenPostArgs(name string) []string {
	n := strings.ToLower(name)
	if strings.Contains(n, "wmplayer") || strings.Contains(n, "mpc") {
		return []string{"/fullscreen"}
	}
	return nil
}

func launchFullscreenArg(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "vlc"):
		return "--fullscreen"
	case strings.Contains(n, "mpv"):
		return "--fs"
	case strings.Contains(n, "ffplay"):
		return "-fs"
	default:
		return ""
	}
}

// buildArgv 生成启动参数。多数后端要求 URL 是最后一个参数；
// WMP / MPC-HC 例外，它们的全屏开关必须跟在媒体路径之后。
// 保留五参数版本供既有调用方与测试使用；带窗口尺寸的版本供 External 使用。
func buildArgv(c candidate, path, uri string, pos time.Duration, fullscreen bool) []string {
	return buildArgvWithSize(c, path, uri, pos, fullscreen, 0, 0)
}

func buildArgvWithSize(c candidate, path, uri string, pos time.Duration, fullscreen bool, w, h int) []string {
	argv := []string{path}
	argv = append(argv, c.args...)
	if fullscreen {
		if c.fullscreenArg != "" {
			argv = append(argv, c.fullscreenArg)
		}
	} else if w > 0 && h > 0 && c.geometryArgs != nil {
		// 全屏优先：开启全屏时窗口尺寸参数不再传入，避免两个设置互相打架。
		argv = append(argv, c.geometryArgs(w, h)...)
	}
	if pos > 0 && c.seekArgs != nil {
		argv = append(argv, c.seekArgs(pos)...)
	}
	argv = append(argv, uri)
	if fullscreen {
		argv = append(argv, c.fullscreenPostArgs...)
	}
	return argv
}

// backendOf 决定用哪个 Player 实现。自定义路径没有稳定的 id，按可执行文件名推断。
func backendOf(c candidate, path string) string {
	id := strings.ToLower(c.id)
	base := strings.ToLower(filepath.Base(path))
	switch {
	case id == "vlc" || strings.Contains(base, "vlc"):
		return "vlc"
	case id == "mpv" || strings.Contains(base, "mpv"):
		return "mpv"
	default:
		return "external"
	}
}
