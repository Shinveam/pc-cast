package app

import (
	"strings"
	"testing"

	"pc-cast/renderer"
)

func TestPlayerNotifyLine(t *testing.T) {
	t.Run("full backend omits capability tag", func(t *testing.T) {
		got := playerNotifyLine(renderer.Descriptor{Name: "内置 mpv", Capability: renderer.CapFull})
		if got != "播放器：内置 mpv" {
			t.Fatalf("完整控制后端不应显示能力标签，得到 %q", got)
		}
	})
	t.Run("weak backend keeps capability tag", func(t *testing.T) {
		got := playerNotifyLine(renderer.Descriptor{Name: "Windows Media Player", Capability: renderer.CapLaunch})
		if !strings.HasSuffix(got, "（基础播放）") {
			t.Fatalf("弱后端应保留降级标签，得到 %q", got)
		}
	})
}
