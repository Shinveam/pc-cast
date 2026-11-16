package renderer

import (
	"fmt"
	"log/slog"
	"strings"

	"pc-cast/logging"
)

// ModeOption 是设置页「播放器」下拉的一项。
type ModeOption struct {
	Value string
	Label string
}

// ModeOptions 返回可选的播放器模式（自动 + 本机探测到的后端 + 自定义）。
func ModeOptions() []ModeOption {
	opts := []ModeOption{{ModeAuto, "自动选择（推荐）"}}
	seen := make(map[string]bool)
	for _, d := range Available() {
		if d.ID == "none" || seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		opts = append(opts, ModeOption{d.ID, fmt.Sprintf("%s · %s", d.Name, d.Capability.Label())})
	}
	return append(opts, ModeOption{ModeCustom, "自定义路径…"})
}

// NewPlayer 按配置与运行环境挑选播放后端。
// 返回 Player、后端描述与降级说明（用于启动通知）。
// 即使所有后端都不可用，也会返回可工作的占位 Player，保证 DLNA 服务仍可被发现。
func NewPlayer(opts Options, log *slog.Logger) (Player, Descriptor, []string) {
	excluded := make(map[string]bool)
	notes := make([]string, 0, 2)

	for attempt := 0; attempt < 4; attempt++ {
		cands := make([]candidate, 0, len(catalogFn()))
		for _, c := range catalogFn() {
			if !excluded[c.id] {
				cands = append(cands, c)
			}
		}
		sel := choose(opts.Mode, opts.CustomPath, cands, probe)
		if sel.fallback != "" {
			notes = append(notes, sel.fallback)
		}
		if sel.cand.capability == CapNone {
			break
		}

		p := buildPlayer(sel, opts, log)
		if err := p.Start(); err != nil {
			_ = p.Close()
			excluded[sel.cand.id] = true
			notes = append(notes, fmt.Sprintf("%s 启动失败（%v），已尝试其它后端", sel.cand.name, err))
			logging.Warn(log, "player", "播放后端启动失败，准备降级", map[string]any{
				"backend": sel.cand.id, "error": err.Error(),
			})
			continue
		}
		d := Describe(p)
		if d.Capability == CapSystem {
			notes = append(notes, "未检测到 mpv/VLC 等播放器，已使用系统默认程序打开（无法暂停、停止或上报进度）")
		}
		if d.Capability != CapFull {
			notes = append(notes, "安装 mpv 或 VLC 可获得完整控制（真实进度、暂停、跳转、音量），并让 HLS 直播流持续稳定播放")
		}
		if len(notes) > 0 {
			d.Note = joinNotes(notes) + "；" + d.Note
		}
		logging.Info(log, "player", "已选择播放后端", map[string]any{
			"backend": d.ID, "name": d.Name, "capability": string(d.Capability),
			"auto": opts.Mode == "" || opts.Mode == ModeAuto,
		})
		return p, d, notes
	}

	notes = append(notes, "未找到任何可用播放器，请在「设置 → 播放器」中指定可执行文件路径")
	none := selection{cand: candidate{id: "none", name: "无可用播放器", capability: CapNone}}
	p := NewExternal(none, opts, log)
	_ = p.Start()
	return p, Describe(p), notes
}

func buildPlayer(sel selection, opts Options, log *slog.Logger) Player {
	if sel.cand.capability != CapFull {
		return NewExternal(sel, opts, log)
	}
	switch backendOf(sel.cand, sel.path) {
	case "vlc":
		return NewVLC(sel, opts, log)
	default:
		mpvOpts := opts
		mpvOpts.MPVPath = sel.path
		return New(mpvOpts, log)
	}
}

func joinNotes(notes []string) string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return strings.Join(out, "；")
}
