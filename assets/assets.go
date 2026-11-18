// Package assets 内嵌 PC-Cast 的图标资源，供系统托盘、桌面通知、窗口标题栏
// 与 Windows 可执行文件资源共用。
//
// 设计母版见 assets/*.svg，位图由 .workbuddy/tools/gen_icons.go 生成：
//   - 彩色主图标：蓝色 squircle + Cast 缺口屏 + 信号弧 + 播放三角
//   - 状态变体：未播放（琥珀）/ 投屏中（绿）/ 投屏已关闭（灰阶），
//     语义色角标区分，深浅主题共用同一张图，不随系统主题反色；StatusIdle 仅作兜底
//   - 系统托盘使用 32px 状态变体，通知与关于窗口使用 64px 状态变体
//
// 所有返回值都是内嵌数据的副本，调用方可以安全持有或修改。
package assets

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	_ "image/png"
)

//go:embed pc-cast.ico
var icoBytes []byte

//go:embed pc-cast-256.png
var appIcon []byte

//go:embed notify-48.png
var notifyImage []byte

//go:embed status-idle.png
var statusIdle []byte

//go:embed status-active.png
var statusActive []byte

//go:embed status-limited.png
var statusLimited []byte

//go:embed status-disabled.png
var statusDisabled []byte

//go:embed status-idle-32.png
var trayIdle []byte

//go:embed status-active-32.png
var trayActive []byte

//go:embed status-limited-32.png
var trayLimited []byte

//go:embed status-disabled-32.png
var trayDisabled []byte

// Status 是图标表达的运行状态，与规范展板「04 状态变体」一一对应。
type Status uint8

const (
	StatusIdle     Status = iota // 待机；仅作未知状态回退，运行状态不再取此值
	StatusActive                 // 投屏中
	StatusLimited                // 未在播放（含完整后端待机与弱后端能力受限）
	StatusDisabled               // 投屏已关闭（cast_enabled = false）
)

func clone(b []byte) []byte { return bytes.Clone(b) }

// AppIcon 返回 256px 彩色主图标，用于窗口标题栏、任务栏与关于窗口。
func AppIcon() []byte { return clone(appIcon) }

// NotifyImage 返回 48px 彩色通知图标，用于普通桌面通知。
func NotifyImage() []byte { return clone(notifyImage) }

// StatusImage 返回 64px 彩色状态变体，用于通知与关于窗口的状态徽标。
// 未知状态回退到待机，避免传入非法枚举时给出 nil 图像（通知 API 会把
// nil 当作「无图」）。
func StatusImage(s Status) []byte {
	switch s {
	case StatusActive:
		return clone(statusActive)
	case StatusLimited:
		return clone(statusLimited)
	case StatusDisabled:
		return clone(statusDisabled)
	default:
		return clone(statusIdle)
	}
}

// TrayStatusIcon 返回 32px 彩色状态变体，用于系统托盘。
// 语义色角标与 StatusImage 完全一致；不直接复用 64px 图，因为 mygo 的
// iconFromPNG 按 PNG 原始尺寸创建 HICON，64→16 的系统缩放会让细线发糊。
// 彩色图标深浅主题共用，因此不需要按任务栏明暗准备两份。
func TrayStatusIcon(s Status) []byte {
	switch s {
	case StatusActive:
		return clone(trayActive)
	case StatusLimited:
		return clone(trayLimited)
	case StatusDisabled:
		return clone(trayDisabled)
	default:
		return clone(trayIdle)
	}
}

// ICO 返回多尺寸 Windows 图标文件（16/32/48/64/128/256），供安装包使用。
func ICO() []byte { return clone(icoBytes) }

// Validate 校验全部内嵌位图的格式与尺寸，被单元测试和 make icons 复用。
func Validate() error {
	checks := []struct {
		name string
		data []byte
		w, h int
	}{
		{"pc-cast-256.png", appIcon, 256, 256},
		{"notify-48.png", notifyImage, 48, 48},
		{"status-idle.png", statusIdle, 64, 64},
		{"status-active.png", statusActive, 64, 64},
		{"status-limited.png", statusLimited, 64, 64},
		{"status-disabled.png", statusDisabled, 64, 64},
		{"status-idle-32.png", trayIdle, 32, 32},
		{"status-active-32.png", trayActive, 32, 32},
		{"status-limited-32.png", trayLimited, 32, 32},
		{"status-disabled-32.png", trayDisabled, 32, 32},
	}
	for _, c := range checks {
		cfg, format, err := image.DecodeConfig(bytes.NewReader(c.data))
		if err != nil {
			return fmt.Errorf("%s 解码失败: %w", c.name, err)
		}
		if format != "png" || cfg.Width != c.w || cfg.Height != c.h {
			return fmt.Errorf("%s 规格不符: %s %dx%d, 期望 png %dx%d",
				c.name, format, cfg.Width, cfg.Height, c.w, c.h)
		}
	}
	if len(icoBytes) < 6 {
		return fmt.Errorf("pc-cast.ico 过短: %d 字节", len(icoBytes))
	}
	return nil
}
