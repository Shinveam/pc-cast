package renderer

import (
	"fmt"
	"log/slog"
	"strings"

	"pc-cast/internal/bundled"
	"pc-cast/logging"
)

// ModeOption 是设置页「播放器」下拉的一项。
type ModeOption struct {
	Value string
	Label string
}

// ModeOptions 返回可选的播放器模式（自动 + 内置 + 本机探测到的后端 + 自定义）。
func ModeOptions() []ModeOption {
	opts := []ModeOption{{ModeAuto, "自动选择（推荐）"}}
	if bundled.Available() {
		opts = append(opts, ModeOption{ModeBuiltin, "内置 mpv · 完整控制"})
	}
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

	for attempt := 0; attempt < 6; attempt++ {
		base := make([]candidate, 0, len(catalogFn())+1)
		for _, c := range catalogFn() {
			if !excluded[c.id] {
				base = append(base, c)
			}
		}
		// 内置播放器始终作为首选注入：auto 模式命中最先的优先级，
		// 显式指定 builtin 时 choose 也能按 ID 精确选中它。
		cands := withBuiltin(base)
		if excluded[ModeBuiltin] {
			cands = base
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
		if d.Capability != CapFull {
			notes = append(notes, "当前后端不支持完整 IPC 控制（暂停/进度为估算值）；如需完整控制请安装 mpv 或 VLC")
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

	notes = append(notes, "未找到任何可用播放器，请在「设置 → 播放器」中指定可执行文件路径，或重新安装程序以恢复内置播放器")
	none := selection{cand: candidate{id: "none", name: "无可用播放器", capability: CapNone}}
	p := NewExternal(none, opts, log)
	_ = p.Start()
	return p, Describe(p), notes
}

func buildPlayer(sel selection, opts Options, log *slog.Logger) Player {
	// 内置候选：此刻才真正解压并取路径（probe 阶段刻意不解压）。
	if sel.cand.id == ModeBuiltin {
		p, err := bundled.Path()
		if err != nil {
			logging.Warn(log, "player", "内置播放器不可用", map[string]any{"error": err.Error()})
			// 返回一个必然 Start 失败的占位，由 NewPlayer 的降级循环跳到下一后端。
			return NewExternal(selection{cand: candidate{id: ModeBuiltin, name: "内置 mpv", capability: CapNone}}, opts, log)
		}
		mpvOpts := opts
		mpvOpts.MPVPath = p
		mpvOpts.Builtin = true
		return New(mpvOpts, log)
	}
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
