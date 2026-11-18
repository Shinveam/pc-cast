package trayui

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"pc-cast/config"
	"pc-cast/dlna"
	"pc-cast/logging"
	"pc-cast/renderer"
)

const autoNetwork = "自动（全部网卡）"

func (u *UI) openSettings() { u.settings.open() }

var logLevels = []string{"debug", "info", "warn", "error"}

type settingsView struct {
	ui  *UI
	win *mygo.Window

	name        string
	port        string
	bindIP      string
	ipOptions   []string
	playerMode  string
	modeLabels  []string
	modeByLabel map[string]string
	playerPath  string
	logLevel    string
	windowSize  string
	windowOpts  []string
	fullscreen  bool
	autoPlay    bool
	openAtLogin bool
	status      string
}

func (v *settingsView) load() {
	c := v.ui.d.Config.Get()
	v.name = c.FriendlyName
	v.port = strconv.Itoa(c.Port)
	v.playerPath = c.PlayerPath
	if v.playerPath == "" {
		v.playerPath = c.MPVPath // 兼容尚未迁移的旧配置
	}
	v.logLevel = c.LogLevel
	v.windowOpts = config.WindowSizeOptions()
	v.windowSize = c.WindowSize
	if v.windowSize == "" {
		v.windowSize = "1080p"
	}
	v.fullscreen = c.Fullscreen
	v.autoPlay = c.AutoPlay
	v.openAtLogin = c.OpenAtLogin
	v.ipOptions = append([]string{autoNetwork}, dlna.LocalIPv4s("")...)
	v.bindIP = autoNetwork
	if c.BindIP != "" {
		found := false
		for _, ip := range v.ipOptions {
			if ip == c.BindIP {
				found = true
				break
			}
		}
		if found {
			v.bindIP = c.BindIP
		}
	}
	v.loadModes(c.PlayerMode)
	v.status = ""
}

func (v *settingsView) loadModes(current string) {
	opts := renderer.ModeOptions()
	labels := make([]string, 0, len(opts)+1)
	byLabel := make(map[string]string, len(opts)+1)
	found := false
	for _, o := range opts {
		labels = append(labels, o.Label)
		byLabel[o.Label] = o.Value
		if o.Value == current {
			found = true
		}
	}
	if current == "" {
		current = renderer.ModeAuto
		found = true
	}
	if !found {
		label := current + "（当前，已不可用）"
		labels = append(labels, label)
		byLabel[label] = current
	}
	v.modeLabels, v.modeByLabel = labels, byLabel
	for label, value := range byLabel {
		if value == current {
			v.playerMode = label
			break
		}
	}
}

func (v *settingsView) currentBackendText() string {
	if v.ui.d.PlayerDesc == nil {
		return ""
	}
	d := v.ui.d.PlayerDesc()
	if d.Name == "" {
		return "尚未初始化"
	}
	return d.Name + " · " + d.Capability.Label()
}

func (v *settingsView) open() {
	v.load()
	v.ui.mu.Lock()
	if v.win == nil || v.win.IsDestroyed() {
		w := mygo.NewWindow(mygo.WindowOptions{
			Title:     "PC-Cast 设置",
			Width:     540,
			Height:    700,
			MinWidth:  440,
			MinHeight: 480,
			Hidden:    true,
			Content:   ui.View(v.view),
		})
		if err := w.SetIcon(windowIcon()); err != nil {
			logging.Warn(v.ui.d.Log, "tray", "设置窗口图标失败", map[string]any{"error": err.Error()})
		}
		w.OnClose(func(e *mygo.CloseEvent) {
			// 退出流程必须放行：mygo 的 prepareQuit 会逐个关闭窗口，
			// 任一窗口 PreventDefault 都会取消整个 Quit。
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

func (v *settingsView) view(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Padding(20).Gap(10).Children(func() {
		ui.Text(c, "PC-Cast 设置").FontSize(20).Bold()
		ui.Text(c, "默认使用程序内置的 mpv 播放器；也可在下方切换为本机安装的播放器。修改端口、网卡或播放器后服务会自动重启。").FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, "右键托盘图标可开启 / 关闭投屏功能；关闭后手机将无法发现此设备。").FontSize(11).TextColor(t.TextMuted)
		ui.Divider(c)
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Gap(12).Children(func() {
				ui.Field(c, "设备名称", func() { ui.TextInput(c, &v.name).Fill() })
				ui.Field(c, "HTTP 端口（0 = 自动分配）", func() { ui.TextInput(c, &v.port).Fill() })
				ui.Field(c, "绑定网卡", func() { ui.Select(c, &v.bindIP, v.ipOptions).Fill() })
				ui.Divider(c)
				ui.Text(c, "当前生效："+v.currentBackendText()).FontSize(11).TextColor(t.TextMuted)
				ui.Field(c, "播放器", func() { ui.Select(c, &v.playerMode, v.modeLabels).Fill() })
				ui.Field(c, "自定义播放器路径", func() { ui.TextInput(c, &v.playerPath).Fill() })
				ui.Text(c, "选择「自定义路径…」后填写可执行文件绝对路径；mpv / VLC 会启用完整 IPC 控制。").FontSize(11).TextColor(t.TextMuted)
				ui.Divider(c)
				ui.Field(c, "日志级别", func() { ui.Select(c, &v.logLevel, logLevels).Fill() })
				ui.Divider(c)
				ui.Field(c, "播放窗口（默认 1080p）", func() { ui.Select(c, &v.windowSize, v.windowOpts).Fill() })
				ui.Text(c, "窗口尺寸不等于片源清晰度：清晰度由手机端投屏时选择的流决定，请在视频 APP 的投屏设置里选 1080P/4K。auto 表示跟随视频原始尺寸。").FontSize(11).TextColor(t.TextMuted)
				ui.Text(c, "勾选「全屏播放」后，窗口尺寸设置不再生效；全屏优先。").FontSize(11).TextColor(t.TextMuted)
				ui.Divider(c)
				ui.Checkbox(c, &v.fullscreen, "全屏播放")
				ui.Checkbox(c, &v.autoPlay, "收到投屏后自动播放")
				ui.Checkbox(c, &v.openAtLogin, "开机自动启动")
			})
		})
		ui.Text(c, v.status).FontSize(11).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c, "重新探测").OnClick(v.refresh)
			ui.Button(c, "恢复默认").OnClick(v.reset)
			ui.Spacer(c)
			ui.Button(c, "取消").OnClick(func() { v.win.Hide() })
			ui.PrimaryButton(c, "保存").OnClick(v.save)
		})
	})
}

func (v *settingsView) refresh() {
	mode := v.modeByLabel[v.playerMode]
	v.loadModes(mode)
	v.setStatus("已重新探测本机播放器：" + v.currentBackendText())
}

func (v *settingsView) reset() {
	d := config.Default()
	v.name = d.FriendlyName
	v.port = strconv.Itoa(d.Port)
	v.bindIP = autoNetwork
	v.playerPath = d.PlayerPath
	v.loadModes(d.PlayerMode)
	v.logLevel = d.LogLevel
	v.windowOpts = config.WindowSizeOptions()
	v.windowSize = d.WindowSize
	v.fullscreen = d.Fullscreen
	v.autoPlay = d.AutoPlay
	v.openAtLogin = d.OpenAtLogin
	v.status = "已恢复默认值，点击保存生效（设备 UUID 保持不变）"
	if v.win != nil {
		v.win.Invalidate()
	}
}

func (v *settingsView) save() {
	port, err := strconv.Atoi(strings.TrimSpace(v.port))
	if err != nil || port < 0 || port > 65535 {
		v.setStatus("端口必须是 0~65535 的整数")
		return
	}
	bind := ""
	if v.bindIP != autoNetwork {
		bind = v.bindIP
	}
	mode := v.modeByLabel[v.playerMode]
	if mode == "" {
		mode = renderer.ModeAuto
	}
	customPath := strings.TrimSpace(v.playerPath)
	if mode == renderer.ModeCustom && customPath == "" {
		v.setStatus("自定义模式需要填写播放器可执行文件路径")
		return
	}
	name, logLevel := v.name, v.logLevel
	windowSize := v.windowSize
	fullscreen, autoPlay, login := v.fullscreen, v.autoPlay, v.openAtLogin
	err = v.ui.d.Config.Update(func(c *config.Config) {
		c.FriendlyName = name
		c.Port = port
		c.BindIP = bind
		c.PlayerMode = mode
		c.PlayerPath = customPath
		c.MPVPath = "" // 清理旧版字段
		c.LogLevel = logLevel
		c.WindowSize = windowSize
		c.Fullscreen = fullscreen
		c.AutoPlay = autoPlay
		c.OpenAtLogin = login
	})
	if err != nil {
		v.setStatus("保存失败：" + err.Error())
		return
	}
	// 保存前窗口没有任何可见反馈：成功时同时给出窗口内状态、桌面通知，
	// 并关闭设置窗口。Hide 不触发 OnClose，也不会走应用退出流程。
	v.setStatus("已保存并生效")
	v.ui.Notify("PC-Cast", "设置已保存并生效")
	if v.win != nil {
		v.win.Hide()
	}
}

func (v *settingsView) setStatus(s string) {
	v.status = s
	if v.win != nil {
		v.win.Invalidate()
	}
}
