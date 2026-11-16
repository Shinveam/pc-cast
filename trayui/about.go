package trayui

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"pc-cast/logging"
)

type aboutView struct {
	ui    *UI
	win   *mygo.Window
	lines [][2]string

	icon       ui.ImageSource
	badge      ui.ImageSource
	badgeLabel string
}

func (u *UI) openAbout() { u.about.open() }

func (v *aboutView) open() {
	if v.ui.d.InfoLines != nil {
		v.lines = v.ui.d.InfoLines()
	}
	// 位图在打开窗口时解码一次，避免每次重绘都重新解析 PNG。
	if v.icon == nil {
		if src, err := ui.DecodeBitmap(windowIcon()); err == nil {
			v.icon = src
		} else {
			logging.Warn(v.ui.d.Log, "tray", "关于窗口图标解码失败", map[string]any{"error": err.Error()})
		}
	}
	badge, label := v.ui.statusBadge()
	if src, err := ui.DecodeBitmap(badge); err == nil {
		v.badge = src
	} else {
		v.badge = nil
	}
	v.badgeLabel = label

	v.ui.mu.Lock()
	if v.win == nil || v.win.IsDestroyed() {
		w := mygo.NewWindow(mygo.WindowOptions{
			Title:     "关于 PC-Cast",
			Width:     560,
			Height:    560,
			MinWidth:  440,
			MinHeight: 400,
			Hidden:    true,
			Content:   ui.View(v.view),
		})
		if err := w.SetIcon(windowIcon()); err != nil {
			logging.Warn(v.ui.d.Log, "tray", "设置窗口图标失败", map[string]any{"error": err.Error()})
		}
		w.OnClose(func(e *mygo.CloseEvent) {
			if v.ui.IsQuitting() {
				return
			}
			e.PreventDefault()
			w.Hide()
		})
		v.win = w
	}
	win := v.win
	v.ui.mu.Unlock()
	win.Invalidate()
	win.Show()
	win.Focus()
}

func (v *aboutView) view(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Padding(20).Gap(10).Children(func() {
		ui.Row(c).Gap(14).Children(func() {
			if v.icon != nil {
				ui.Image(c, v.icon).Size(56, 56)
			}
			ui.Column(c).Gap(4).Grow(1).Children(func() {
				ui.Text(c, "PC-Cast").FontSize(24).Bold()
				ui.Text(c, "跨平台 DLNA / UPnP 投屏接收端，自动选择本机可用播放器").FontSize(12).TextColor(t.TextMuted)
			})
		})
		ui.Row(c).Gap(8).Children(func() {
			if v.badge != nil {
				ui.Image(c, v.badge).Size(20, 20)
			}
			ui.Text(c, v.badgeLabel).FontSize(12).TextColor(t.TextMuted)
			ui.Spacer(c)
		})
		ui.Divider(c)
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Gap(8).Children(func() {
				for _, kv := range v.lines {
					ui.Row(c).Gap(10).Children(func() {
						ui.Text(c, kv[0]).Width(84).FontSize(12).TextColor(t.TextMuted)
						ui.Text(c, kv[1]).Grow(1).FontSize(12)
					})
				}
			})
		})
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c, "复制信息").OnClick(func() {
				if v.ui.d.InfoText != nil {
					mygo.Clipboard.WriteText(v.ui.d.InfoText())
				}
			})
			ui.Spacer(c)
			ui.Button(c, "关闭").OnClick(func() { v.win.Hide() })
		})
	})
}
