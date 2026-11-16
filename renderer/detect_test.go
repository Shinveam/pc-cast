package renderer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCandidates() []candidate {
	return []candidate{
		{id: "mpv", name: "mpv", capability: CapFull},
		{id: "vlc", name: "VLC", capability: CapFull},
		{id: "ffplay", name: "ffplay", capability: CapLaunch},
		{id: "system", name: "系统默认程序", capability: CapSystem},
	}
}

func probeExcept(unavailable ...string) func(candidate) (string, bool) {
	bad := make(map[string]bool, len(unavailable))
	for _, id := range unavailable {
		bad[id] = true
	}
	return func(c candidate) (string, bool) {
		if bad[c.id] {
			return "", false
		}
		return "/opt/" + c.id, true
	}
}

func TestChooseAutoPrefersCapability(t *testing.T) {
	sel := choose(ModeAuto, "", testCandidates(), probeExcept())
	if sel.cand.id != "mpv" || sel.cand.capability != CapFull {
		t.Fatalf("全部可用时应选 mpv，实际 %#v", sel.cand)
	}
}

func TestChooseFallbackChain(t *testing.T) {
	cases := []struct {
		name        string
		unavailable []string
		want        string
		wantCap     Capability
	}{
		{"mpv 缺失降级到 VLC（仍是完整控制）", []string{"mpv"}, "vlc", CapFull},
		{"只剩系统默认程序", []string{"mpv", "vlc", "ffplay"}, "system", CapSystem},
		{"全部缺失", []string{"mpv", "vlc", "ffplay", "system"}, "none", CapNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sel := choose(ModeAuto, "", testCandidates(), probeExcept(tc.unavailable...))
			if sel.cand.id != tc.want || sel.cand.capability != tc.wantCap {
				t.Fatalf("得到 %s/%s，期望 %s/%s", sel.cand.id, sel.cand.capability, tc.want, tc.wantCap)
			}
			if tc.want == "none" && sel.fallback == "" {
				t.Fatal("全部缺失时应给出降级说明")
			}
		})
	}
}

func TestChooseExplicitMode(t *testing.T) {
	sel := choose("vlc", "", testCandidates(), probeExcept())
	if sel.cand.id != "vlc" || sel.fallback != "" {
		t.Fatalf("显式指定 VLC 失败: %#v", sel)
	}
	sel = choose("mpv", "", testCandidates(), probeExcept("mpv"))
	if sel.fallback == "" {
		t.Fatal("指定后端不可用时应给出降级说明")
	}
	// factory 会把不可用后端排除后重新 choose，这里验证第二次确实落到 VLC。
	sel = choose(ModeAuto, "", testCandidates(), probeExcept("mpv"))
	if sel.cand.id != "vlc" {
		t.Fatalf("降级后应选中 VLC，实际 %s", sel.cand.id)
	}
}

func TestChooseCustomPath(t *testing.T) {
	dir := t.TempDir()
	mpvLike := filepath.Join(dir, "portable-mpv.exe")
	vlcLike := filepath.Join(dir, "vlc-portable.exe")
	for _, p := range []string{mpvLike, vlcLike} {
		if err := os.WriteFile(p, []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mpv := choose(ModeCustom, mpvLike, testCandidates(), probeExcept())
	if mpv.cand.capability != CapFull || mpv.path != mpvLike {
		t.Fatalf("自定义 mpv 应按完整能力处理: %#v", mpv)
	}
	vlc := choose(ModeCustom, vlcLike, testCandidates(), probeExcept())
	// VLC 已接入 HTTP Lua IPC，自定义 VLC 与内置 VLC 一样属于完整控制后端。
	if vlc.cand.capability != CapFull || vlc.cand.seekArgs == nil {
		t.Fatalf("自定义 VLC 应具备完整控制能力: %#v", vlc.cand)
	}
	if got := backendOf(vlc.cand, vlc.path); got != "vlc" {
		t.Fatalf("自定义 VLC 应分派到 VLC 后端，实际 %s", got)
	}
	missing := choose(ModeCustom, filepath.Join(dir, "nope.exe"), testCandidates(), probeExcept())
	if missing.cand.id != "mpv" || missing.fallback == "" {
		t.Fatalf("自定义路径不存在时应自动降级: %#v", missing)
	}
}

func TestBuildArgv(t *testing.T) {
	c := candidate{id: "vlc", args: []string{"--no-video-title-show"}, fullscreenArg: "--fullscreen",
		seekArgs: func(d time.Duration) []string { return []string{"--start-time=42.000"} }}
	argv := buildArgv(c, "C:/VLC/vlc.exe", "http://192.168.1.9/movie.mp4", 42*time.Second, true)
	if argv[0] != "C:/VLC/vlc.exe" || argv[len(argv)-1] != "http://192.168.1.9/movie.mp4" {
		t.Fatalf("URL 必须是最后一个参数: %#v", argv)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--no-video-title-show", "--fullscreen", "--start-time=42.000"} {
		if !strings.Contains(joined, want) {
			t.Errorf("参数缺少 %q: %#v", want, argv)
		}
	}
	short := buildArgv(c, "vlc", "http://x/v.mp4", 0, false)
	if len(short) != 3 {
		t.Fatalf("无 seek/全屏时参数应精简: %#v", short)
	}
}

func TestModeOptionsStructure(t *testing.T) {
	opts := ModeOptions()
	if len(opts) < 2 || opts[0].Value != ModeAuto || opts[len(opts)-1].Value != ModeCustom {
		t.Fatalf("下拉项结构异常: %#v", opts)
	}
	for _, o := range opts {
		if o.Value == "" || o.Label == "" {
			t.Fatalf("下拉项存在空值: %#v", o)
		}
	}
}

func TestAvailableNeverPanics(t *testing.T) {
	// 探测只做 LookPath / Stat，不应执行外部命令，也不应因缺失环境变量崩溃。
	for _, d := range Available() {
		if d.ID == "" || d.Name == "" || d.Path == "" {
			t.Fatalf("探测结果不完整: %#v", d)
		}
	}
}

// WMP / MPC-HC 的官方语法要求全屏开关位于媒体路径之后；
// 若误写成前置参数，播放器会静默忽略，用户勾选「全屏播放」却毫无效果。
func TestFullscreenSwitchAfterURI(t *testing.T) {
	const uri = "http://192.168.1.9/movie.mp4"
	for _, id := range []string{"wmplayer", "mpc"} {
		var c candidate
		for _, item := range windowsCatalog() {
			if item.id == id {
				c = item
			}
		}
		if c.id == "" {
			t.Fatalf("windowsCatalog 缺少 %s", id)
		}
		full := buildArgvWithSize(c, "player.exe", uri, 0, true, 1920, 1080)
		if full[len(full)-1] != "/fullscreen" || full[len(full)-2] != uri {
			t.Errorf("%s 全屏开关必须紧跟在 URL 之后: %#v", id, full)
		}
		normal := buildArgvWithSize(c, "player.exe", uri, 0, false, 1920, 1080)
		if strings.Contains(strings.Join(normal, " "), "/fullscreen") {
			t.Errorf("%s 未开启全屏时不应带全屏开关: %#v", id, normal)
		}
		if normal[len(normal)-1] != uri {
			t.Errorf("%s 非全屏时 URL 应是最后一个参数: %#v", id, normal)
		}
	}
}

// 除「系统默认程序」这一无法控制的兜底外，每个 Windows 后端都必须能响应全屏设置，
// 防止新增后端时再次出现「勾选了但没生效」的静默失败。
func TestWindowsBackendsSupportFullscreen(t *testing.T) {
	for _, c := range windowsCatalog() {
		if c.id == "system" {
			continue
		}
		if !c.hasFullscreen() {
			t.Errorf("后端 %s 缺少全屏参数", c.id)
		}
	}
}

func TestFullscreenOverridesWindowSize(t *testing.T) {
	var ffplay candidate
	for _, c := range windowsCatalog() {
		if c.id == "ffplay" {
			ffplay = c
		}
	}
	argv := buildArgvWithSize(ffplay, "ffplay", "http://x/v.mp4", 0, true, 1920, 1080)
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-fs") {
		t.Errorf("全屏时应带 -fs: %#v", argv)
	}
	if strings.Contains(joined, "-x 1920") {
		t.Errorf("全屏时不应再传窗口尺寸: %#v", argv)
	}
}
