package trayui

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"pc-cast/assets"
	"pc-cast/config"
	"pc-cast/renderer"
)

func iconSize(t *testing.T, b []byte) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("图标解码失败: %v", err)
	}
	return cfg.Width, cfg.Height
}

// hasColorPlate 判断是否为「不透明彩色底板」图标：既能排除透明底单色
// 模板，也允许「投屏已关闭」的灰阶变体（仍带灰蓝底板）。
func hasColorPlate(t *testing.T, b []byte) bool {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("PNG 解码失败: %v", err)
	}
	opaque, blue := 0, 0
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a < 0xc000 {
				continue
			}
			opaque++
			if bl > r+4000 && bl > g+2000 {
				blue++
			}
		}
	}
	return opaque >= 800 && blue >= 100
}

// TestTrayIconIsColoredStatusVariant 确认托盘占位图是彩色待机变体，
// 而不是上一版的透明底单色模板。
func TestTrayIconIsColoredStatusVariant(t *testing.T) {
	b := trayIcon()
	if w, h := iconSize(t, b); w != 32 || h != 32 {
		t.Fatalf("托盘图标规格 %dx%d，期望 32x32", w, h)
	}
	if !bytes.Equal(b, assets.TrayStatusIcon(assets.StatusIdle)) {
		t.Fatal("托盘占位图未取自内嵌彩色待机变体")
	}
	if !hasColorPlate(t, b) {
		t.Fatal("托盘图标必须是彩色，不能退回单色模板")
	}
}

func TestWindowIcon(t *testing.T) {
	if w, h := iconSize(t, windowIcon()); w != 256 || h != 256 {
		t.Fatalf("窗口图标规格 %dx%d，期望 256x256", w, h)
	}
}

func TestNotificationImageByStyle(t *testing.T) {
	cases := []struct {
		style NotifyStyle
		want  []byte
	}{
		{NotifyDefault, assets.NotifyImage()},
		{NotifyActive, assets.StatusImage(assets.StatusActive)},
		{NotifyLimited, assets.StatusImage(assets.StatusLimited)},
		{NotifyDisabled, assets.StatusImage(assets.StatusDisabled)},
		{NotifyStyle(200), assets.NotifyImage()},
	}
	for _, c := range cases {
		if !bytes.Equal(notificationImage(c.style), c.want) {
			t.Fatalf("NotifyStyle %d 取到错误的图标", c.style)
		}
	}
}

// TestImagesDecodeAsUI 覆盖关于窗口与通知实际使用的解码路径，
// 防止某张图格式不兼容时界面静默丢图。
func TestImagesDecodeAsUI(t *testing.T) {
	images := [][]byte{windowIcon(), trayIcon()}
	for _, s := range []assets.Status{
		assets.StatusIdle, assets.StatusActive, assets.StatusLimited, assets.StatusDisabled,
	} {
		images = append(images, trayIconFor(s))
	}
	for _, s := range []NotifyStyle{NotifyDefault, NotifyActive, NotifyLimited, NotifyDisabled} {
		images = append(images, notificationImage(s))
	}
	for i, b := range images {
		if _, err := ui.DecodeBitmap(b); err != nil {
			t.Fatalf("第 %d 张图 ui.DecodeBitmap 失败: %v", i, err)
		}
	}
}

// stubPlayer 只实现 State；嵌入接口补齐其余方法，测试不会调用它们。
type stubPlayer struct {
	renderer.Player
	state renderer.State
}

func (p stubPlayer) State() renderer.State { return p.state }

func TestStatusBadge(t *testing.T) {
	t.Run("cast disabled", func(t *testing.T) {
		u := castTestUI(t)
		if err := u.d.Config.Update(func(c *config.Config) { c.CastEnabled = false }); err != nil {
			t.Fatal(err)
		}
		img, label := u.statusBadge()
		if !strings.Contains(label, "关闭") || !bytes.Equal(img, assets.StatusImage(assets.StatusDisabled)) {
			t.Fatalf("关闭态徽标错误: %q", label)
		}
	})

	t.Run("idle", func(t *testing.T) {
		u := castTestUI(t) // Player/PlayerDesc getter 均为 nil，需安全降级
		img, label := u.statusBadge()
		if !strings.Contains(label, "待机") || !bytes.Equal(img, assets.StatusImage(assets.StatusIdle)) {
			t.Fatalf("待机态徽标错误: %q", label)
		}
	})

	t.Run("capability limited", func(t *testing.T) {
		u := castTestUI(t)
		u.d.PlayerDesc = func() renderer.Descriptor {
			return renderer.Descriptor{Name: "wmplayer", Capability: renderer.CapSystem}
		}
		img, label := u.statusBadge()
		if !strings.Contains(label, "能力受限") || !bytes.Equal(img, assets.StatusImage(assets.StatusLimited)) {
			t.Fatalf("能力受限徽标错误: %q", label)
		}
	})

	t.Run("playing", func(t *testing.T) {
		u := castTestUI(t)
		u.d.Player = func() renderer.Player { return stubPlayer{state: renderer.StatePlaying} }
		img, label := u.statusBadge()
		if !strings.Contains(label, "投屏中") || !bytes.Equal(img, assets.StatusImage(assets.StatusActive)) {
			t.Fatalf("投屏中徽标错误: %q", label)
		}
	})
}

// TestTrayIconForStatus 覆盖托盘取图：四态必须互不相同，且都取自内嵌的
// 32px 彩色状态变体（语义色角标由 assets 包的单测负责校验）。
func TestTrayIconForStatus(t *testing.T) {
	states := []assets.Status{
		assets.StatusIdle, assets.StatusActive, assets.StatusLimited, assets.StatusDisabled,
	}
	seen := map[string]assets.Status{}
	for _, s := range states {
		b := trayIconFor(s)
		if w, h := iconSize(t, b); w != 32 || h != 32 {
			t.Fatalf("status=%d -> %dx%d，期望 32x32", s, w, h)
		}
		if !bytes.Equal(b, assets.TrayStatusIcon(s)) {
			t.Fatalf("status=%d 未取自内嵌彩色状态变体", s)
		}
		if !hasColorPlate(t, b) {
			t.Fatalf("status=%d 托盘图不是彩色", s)
		}
		if prev, ok := seen[string(b)]; ok {
			t.Fatalf("状态 %d 与 %d 使用了同一张托盘图", s, prev)
		}
		seen[string(b)] = s
	}
	if !bytes.Equal(trayIconFor(assets.Status(200)), assets.TrayStatusIcon(assets.StatusIdle)) {
		t.Fatal("未知状态应回退到待机图")
	}
}

// TestCurrentStatus 与 statusBadge 共用同一状态判定，四分支逐一核对。
func TestCurrentStatus(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		u := castTestUI(t)
		if err := u.d.Config.Update(func(c *config.Config) { c.CastEnabled = false }); err != nil {
			t.Fatal(err)
		}
		if s, label := u.currentStatus(); s != assets.StatusDisabled || !strings.Contains(label, "关闭") {
			t.Fatalf("关闭态判定错误: %d %q", s, label)
		}
	})
	t.Run("idle", func(t *testing.T) {
		if s, _ := castTestUI(t).currentStatus(); s != assets.StatusIdle {
			t.Fatalf("无播放器时应为待机，得到 %d", s)
		}
	})
	t.Run("limited", func(t *testing.T) {
		u := castTestUI(t)
		u.d.PlayerDesc = func() renderer.Descriptor {
			return renderer.Descriptor{Name: "wmplayer", Capability: renderer.CapSystem}
		}
		if s, _ := u.currentStatus(); s != assets.StatusLimited {
			t.Fatalf("能力受限判定错误: %d", s)
		}
	})
	t.Run("playing beats limited", func(t *testing.T) {
		// 本机默认后端 wmplayer 是 CapLaunch，能力受限会长期成立；
		// 播放态必须优先，否则托盘图标永远停在「能力受限」。
		u := castTestUI(t)
		u.d.PlayerDesc = func() renderer.Descriptor {
			return renderer.Descriptor{Name: "Windows Media Player", Capability: renderer.CapLaunch}
		}
		u.d.Player = func() renderer.Player { return stubPlayer{state: renderer.StatePlaying} }
		if s, _ := u.currentStatus(); s != assets.StatusActive {
			t.Fatalf("播放态必须优先于常驻的能力受限，得到 %d", s)
		}
	})
}
