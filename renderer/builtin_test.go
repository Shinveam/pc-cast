package renderer

import (
	"log/slog"
	"testing"

	"pc-cast/internal/bundled"
)

// 这些测试验证「内置优先 + 用户可覆盖 + 无 system 兜底」的选择链契约。
// probe 用桩注入，避免依赖本机实际安装了哪些播放器。

func catalogNoBuiltin() []candidate {
	return []candidate{
		{id: "mpv", name: "mpv", capability: CapFull},
		{id: "vlc", name: "VLC", capability: CapFull},
		{id: "potplayer", name: "PotPlayer", capability: CapLaunch},
	}
}

func TestWithBuiltinPrependsWhenAvailable(t *testing.T) {
	base := catalogNoBuiltin()
	got := withBuiltin(base)
	if !bundled.Available() {
		if len(got) != len(base) {
			t.Fatalf("无内置数据时不应注入: %d", len(got))
		}
		return
	}
	if len(got) != len(base)+1 || got[0].id != ModeBuiltin {
		t.Fatalf("内置应置顶: %#v", got)
	}
}

func TestChooseAutoSelectsBuiltinFirst(t *testing.T) {
	if !bundled.Available() {
		t.Skip("当前平台无内置二进制")
	}
	cands := withBuiltin(catalogNoBuiltin())
	sel := choose(ModeAuto, "", cands, probe) // 用真实 probe，内置候选走占位路径分支
	if sel.cand.id != ModeBuiltin {
		t.Fatalf("auto 应优先内置，实际 %s", sel.cand.id)
	}
	if sel.path != "builtin:mpv" {
		t.Fatalf("内置候选应使用占位路径，实际 %q", sel.path)
	}
}

func TestChooseExplicitOverridesBuiltin(t *testing.T) {
	cands := withBuiltin(catalogNoBuiltin())
	// 用户显式指定 vlc：即便内置在第一位，也必须按 ID 精确命中 vlc。
	sel := choose("vlc", "", cands, probeExcept())
	if sel.cand.id != "vlc" {
		t.Fatalf("显式 vlc 应覆盖内置，实际 %s", sel.cand.id)
	}
	// 显式 builtin 也应命中。
	if !bundled.Available() {
		return
	}
	sel = choose(ModeBuiltin, "", cands, probeExcept())
	if sel.cand.id != ModeBuiltin {
		t.Fatalf("显式 builtin 应命中，实际 %s", sel.cand.id)
	}
}

func TestNoSystemFallbackInCatalogs(t *testing.T) {
	for _, cands := range [][]candidate{windowsCatalog(), darwinCatalog(), linuxCatalog()} {
		for _, c := range cands {
			if c.id == "system" || c.capability == CapSystem {
				t.Fatalf("catalog 不应再包含 system 兜底: %s/%s", c.id, c.capability)
			}
		}
	}
}

func TestNewPlayerAutoUsesBuiltinDescriptor(t *testing.T) {
	if !bundled.Available() {
		t.Skip("当前平台无内置二进制")
	}
	p, d, _ := NewPlayer(Options{Mode: ModeAuto}, slog.Default())
	defer p.Close()
	if d.ID != ModeBuiltin || d.Name != "内置 mpv" {
		t.Fatalf("auto 应报告内置播放器，实际 %s/%s", d.ID, d.Name)
	}
	if p.Path() == "" {
		t.Fatal("内置播放器路径不应为空")
	}
}
