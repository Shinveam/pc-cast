package trayui

import (
	"pc-cast/assets"
	"pc-cast/renderer"
)

// 图标资源统一由 assets 包 go:embed 提供，本文件只负责「在什么场景用哪一张」。
// 视觉规范见「PC-Cast 图标与通知设计规范」展板：彩色状态变体以绿 / 琥珀 /
// 灰阶角标表达运行状态，深浅主题共用同一张图；托盘、通知、关于窗口状态徽标
// 取自同一套变体，仅按场景选择 32 / 48 / 64px 尺寸。

// trayIcon 返回创建托盘瞬间的占位图（待机态），此时服务尚未启动、
// 状态必为待机；随后 Ready 会按实际状态校正一次。
func trayIcon() []byte { return assets.TrayStatusIcon(assets.StatusIdle) }

// trayIconFor 返回当前运行状态对应的 32px 彩色状态变体，用于系统托盘。
func trayIconFor(s assets.Status) []byte { return assets.TrayStatusIcon(s) }

// windowIcon 返回窗口标题栏与任务栏使用的彩色主图标。
func windowIcon() []byte { return assets.AppIcon() }

// NotifyStyle 决定桌面通知附带哪张图标。
type NotifyStyle uint8

const (
	NotifyDefault  NotifyStyle = iota // 48px 彩色主图标
	NotifyActive                      // 投屏中（绿色角标）
	NotifyLimited                     // 能力受限（琥珀色角标）
	NotifyDisabled                    // 投屏已关闭（灰阶）
)

func notificationImage(style NotifyStyle) []byte {
	switch style {
	case NotifyActive:
		return assets.StatusImage(assets.StatusActive)
	case NotifyLimited:
		return assets.StatusImage(assets.StatusLimited)
	case NotifyDisabled:
		return assets.StatusImage(assets.StatusDisabled)
	default:
		return assets.NotifyImage()
	}
}

// currentStatus 判定当前运行状态，是托盘图标、通知与关于窗口共用的唯一来源。
// 优先级：总开关关闭 > 正在播放 > 能力受限 > 待机。
// 「能力受限」是环境属性（未装 mpv / VLC 时会长期落在 CapLaunch / CapSystem
// 后端），若排在播放态之前会永久遮蔽「投屏中」，使状态图标失去意义。
func (u *UI) currentStatus() (assets.Status, string) {
	if u.d.Config != nil && !u.d.Config.Get().CastEnabled {
		return assets.StatusDisabled, "投屏已关闭"
	}
	if u.d.Player != nil {
		if p := u.d.Player(); p != nil && p.State() == renderer.StatePlaying {
			return assets.StatusActive, "投屏中"
		}
	}
	if u.d.PlayerDesc != nil {
		d := u.d.PlayerDesc()
		if d.Name != "" && d.Capability != renderer.CapFull {
			return assets.StatusLimited, "能力受限 · " + d.Capability.Label()
		}
	}
	return assets.StatusIdle, "待机 · 服务已就绪"
}

// statusBadge 返回当前运行状态对应的彩色徽标与文案，供关于窗口展示。
func (u *UI) statusBadge() ([]byte, string) {
	s, label := u.currentStatus()
	return assets.StatusImage(s), label
}
