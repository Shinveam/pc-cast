// Package trayui 实现系统托盘、右键菜单、设置窗口与软件信息窗口。
package trayui

import (
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"pc-cast/config"
	"pc-cast/dlna"
	"pc-cast/logging"
	"pc-cast/renderer"
)

// Deps 由 app 层注入，trayui 不反向依赖 app，避免包循环。
// Server/Player 使用 getter 函数，使"修改端口/播放器后重启服务"对 UI 透明。
type Deps struct {
	Log        *slog.Logger
	Config     *config.Store
	Server     func() *dlna.Server
	Player     func() renderer.Player
	PlayerDesc func() renderer.Descriptor
	InfoLines  func() [][2]string
	InfoText   func() string
	Quit       func() // 托盘「退出」的实际执行者，由 app 层负责资源清理
}

type UI struct {
	d Deps

	mu         sync.Mutex
	tray       *mygo.Tray
	menu       *mygo.Menu
	statusItem *mygo.MenuItem
	settings   *settingsView
	about      *aboutView

	iconMu      sync.Mutex // 串行化图标刷新，避免状态轮询并发写入
	iconKey     int32      // 已生效的状态；-1 表示未设置，受 iconMu 保护
	iconPending atomic.Bool

	// quitting 为 true 时，窗口的 OnClose 必须放行，否则 mygo 的
	// prepareQuit 会因窗口拒绝关闭而取消整个退出流程。
	quitting       atomic.Bool
	tooltipPending atomic.Bool
	stopCh         chan struct{}
	wg             sync.WaitGroup
	closed         bool
}

func New(d Deps) *UI {
	u := &UI{d: d, stopCh: make(chan struct{}), iconKey: -1}
	u.settings = &settingsView{ui: u}
	u.about = &aboutView{ui: u}
	return u
}

// BeginQuit 在任何退出路径之前调用，放行窗口关闭并主动销毁辅助窗口。
// 幂等：托盘退出、OnBeforeQuit、OS 会话结束可能重复触发。
func (u *UI) BeginQuit() {
	if !u.quitting.CompareAndSwap(false, true) {
		return
	}
	u.destroyWindows()
}

func (u *UI) IsQuitting() bool { return u.quitting.Load() }

// destroyWindows 让 Windows() 列表清空：隐藏的窗口 native 仍在，
// prepareQuit 逐个 close 时仍会触发它们的 OnClose。
func (u *UI) destroyWindows() {
	u.mu.Lock()
	wins := make([]*mygo.Window, 0, 2)
	if u.settings != nil && u.settings.win != nil {
		wins = append(wins, u.settings.win)
		u.settings.win = nil
	}
	if u.about != nil && u.about.win != nil {
		wins = append(wins, u.about.win)
		u.about.win = nil
	}
	u.mu.Unlock()
	for _, w := range wins {
		if !w.IsDestroyed() {
			w.Destroy() // 直接销毁，不派发 OnClose
		}
	}
}

// requestQuit 由托盘菜单调用。默认路径也先 BeginQuit，保证即使 Deps
// 未注入 Quit，退出也不会被残留窗口拦截。
func (u *UI) requestQuit() {
	u.BeginQuit()
	if u.d.Quit != nil {
		u.d.Quit()
		return
	}
	mygo.App.Quit()
}

// Ready 必须在 mygo.App.WhenReady 中调用：托盘与通知都要求应用已运行。
func (u *UI) Ready() error {
	cfg := u.d.Config.Get()
	u.statusItem = &mygo.MenuItem{ID: "status", Label: "状态：空闲", Disabled: true}
	u.menu = mygo.NewMenu([]*mygo.MenuItem{
		u.statusItem,
		{ID: "copy", Label: "复制当前播放地址", Click: func(*mygo.MenuItem, *mygo.Window) { u.copyURI() }},
		mygo.Separator(),
		{ID: "settings", Label: "设置…", Click: func(*mygo.MenuItem, *mygo.Window) { u.openSettings() }},
		{ID: "cast", Label: "启用投屏", Type: mygo.MenuItemCheckbox, Checked: cfg.CastEnabled,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) { u.toggle("cast", it.IsChecked()) }},
		{ID: "fullscreen", Label: "全屏播放", Type: mygo.MenuItemCheckbox, Checked: cfg.Fullscreen,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) { u.toggle("fullscreen", it.IsChecked()) }},
		{ID: "autoplay", Label: "收到投屏自动播放", Type: mygo.MenuItemCheckbox, Checked: cfg.AutoPlay,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) { u.toggle("autoplay", it.IsChecked()) }},
		{ID: "login", Label: "开机自动启动", Type: mygo.MenuItemCheckbox, Checked: cfg.OpenAtLogin,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) { u.toggle("login", it.IsChecked()) }},
		mygo.Separator(),
		{ID: "about", Label: "软件信息", Click: func(*mygo.MenuItem, *mygo.Window) { u.openAbout() }},
		{ID: "config-dir", Label: "打开配置目录", Click: func(*mygo.MenuItem, *mygo.Window) { u.openConfigDir() }},
		mygo.Separator(),
		{Label: "退出", Click: func(*mygo.MenuItem, *mygo.Window) { u.requestQuit() }},
	})

	// 创建时先用待机态占位，随后同步按当前运行状态校正一次，避免启动瞬间
	// 先显示待机图再跳变。彩色状态变体深浅主题共用，无需监听任务栏明暗。
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:             "pc-cast-main",
		Icon:           trayIcon(),
		IconIsTemplate: false, // 彩色图标不交给平台隐式着色
		Title:          "PC-Cast",
		ToolTip:        "PC-Cast · DLNA 投屏接收端",
		Menu:           u.menu,
	})
	if err != nil {
		return err
	}
	u.mu.Lock()
	u.tray = tray
	u.mu.Unlock()

	u.iconMu.Lock()
	st, _ := u.currentStatus()
	if err := tray.SetIcon(trayIconFor(st), false); err != nil {
		logging.Warn(u.d.Log, "tray", "设置初始托盘图标失败", map[string]any{"error": err.Error()})
	} else {
		u.iconKey = int32(st)
	}
	u.iconMu.Unlock()

	u.wg.Add(1)
	go u.statusLoop()
	logging.Info(u.d.Log, "tray", "托盘已就绪", nil)
	return nil
}

// applyTrayIcon 在运行状态变化时重设托盘图标。
// SetIcon 经 onMain 同步等待主线程，因此放到独立 goroutine，与 SetToolTip
// 采用同样的异步模式，避免退出流程互锁；iconPending 防止刷新堆积，
// iconMu 串行化保证并发轮询不会乱序写入，iconKey 让 2s 轮询只在状态
// 真正变化时才触发 Shell_NotifyIconW（否则会持续闪烁）。
func (u *UI) applyTrayIcon() {
	if u.IsQuitting() || !u.iconPending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer u.iconPending.Store(false)
		u.iconMu.Lock()
		defer u.iconMu.Unlock()
		u.mu.Lock()
		tray, closed := u.tray, u.closed
		u.mu.Unlock()
		if closed || tray == nil || u.IsQuitting() {
			return
		}
		st, _ := u.currentStatus()
		key := int32(st)
		if key == u.iconKey {
			return
		}
		if err := tray.SetIcon(trayIconFor(st), false); err != nil {
			logging.Warn(u.d.Log, "tray", "切换托盘图标失败", map[string]any{"error": err.Error()})
			return
		}
		u.iconKey = key
	}()
}

func (u *UI) statusLoop() {
	defer u.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			u.refreshStatus()
		case <-u.stopCh:
			return
		}
	}
}

func (u *UI) refreshStatus() {
	if u.IsQuitting() {
		return
	}
	// 必须放在所有提前 return 之前：总开关关闭、播放器尚未就绪时，
	// 也要把「已关闭 / 待机」态刷到托盘图标上。
	u.applyTrayIcon()
	// 总开关关闭时播放器已被释放，不能用 player 状态覆盖菜单文案。
	if u.d.Config != nil && !u.d.Config.Get().CastEnabled {
		u.setStatusLabel("状态：投屏已关闭")
		return
	}
	p := u.d.Player()
	if p == nil {
		return
	}
	state := p.State()
	uri := p.CurrentURI()
	label := "状态：空闲"
	switch state {
	case renderer.StatePlaying:
		label = "状态：正在播放"
	case renderer.StatePaused:
		label = "状态：已暂停"
	case renderer.StateStopped:
		label = "状态：已停止"
	case renderer.StateTransitioning:
		label = "状态：加载中"
	}
	if uri != "" {
		label += " · " + shortURI(uri)
	}
	u.setStatusLabel(label)
}

// setStatusLabel 更新菜单首项与托盘 tooltip。SetToolTip 走 onMain 同步等待，
// 因此放在独立 goroutine 中，避免与退出流程互锁。
func (u *UI) setStatusLabel(label string) {
	if u.statusItem != nil {
		u.statusItem.SetLabel(label) // postMain 异步，不阻塞退出
	}
	u.mu.Lock()
	tray := u.tray
	u.mu.Unlock()
	if tray != nil && u.tooltipPending.CompareAndSwap(false, true) {
		go func(tip string) {
			defer u.tooltipPending.Store(false)
			tray.SetToolTip(tip)
		}(label)
	}
}

func shortURI(uri string) string {
	if i := strings.LastIndexAny(uri, "/?"); i >= 0 && i < len(uri)-1 {
		uri = uri[i+1:]
	}
	if len(uri) > 42 {
		return uri[:39] + "…"
	}
	return uri
}

func (u *UI) copyURI() {
	p := u.d.Player()
	if p == nil {
		return
	}
	uri := p.CurrentURI()
	if uri == "" {
		u.Notify("PC-Cast", "当前没有投屏内容")
		return
	}
	mygo.Clipboard.WriteText(uri)
	u.Notify("PC-Cast", "播放地址已复制到剪贴板")
}

func (u *UI) toggle(key string, value bool) {
	err := u.d.Config.Update(func(c *config.Config) {
		switch key {
		case "fullscreen":
			c.Fullscreen = value
		case "autoplay":
			c.AutoPlay = value
		case "login":
			c.OpenAtLogin = value
		case "cast":
			c.CastEnabled = value
		}
	})
	if err != nil {
		logging.Warn(u.d.Log, "tray", "保存开关失败", map[string]any{"key": key, "error": err.Error()})
		u.Notify("PC-Cast", "设置保存失败："+err.Error())
	}
}

func (u *UI) openConfigDir() {
	dir, err := config.Dir()
	if err != nil {
		logging.Warn(u.d.Log, "tray", "获取配置目录失败", map[string]any{"error": err.Error()})
		return
	}
	go func() { _ = mygo.Shell.OpenPath(dir) }()
}

// Notify 发送桌面通知；不支持通知的平台静默降级为日志。
// style 省略时使用 48px 彩色主图标，带状态的通知传对应变体。
func (u *UI) Notify(title, body string, style ...NotifyStyle) {
	if !mygo.NotificationsSupported() {
		logging.Info(u.d.Log, "tray", title+" / "+body, nil)
		return
	}
	s := NotifyDefault
	if len(style) > 0 {
		s = style[0]
	}
	img := notificationImage(s)
	go func() {
		n := mygo.NewNotification(mygo.NotificationOptions{
			ID: "pc-cast-status", Title: title, Body: body, Silent: true,
			Image: img,
		})
		if err := n.Show(); err != nil {
			logging.Warn(u.d.Log, "tray", "通知发送失败", map[string]any{"error": err.Error()})
		} else {
			// 通知可能被 Windows 专注助手吞掉，日志用于区分「没发出」与「发了没看见」。
			logging.Info(u.d.Log, "tray", "桌面通知已发送", map[string]any{"title": title})
		}
	}()
}

// Close 只做资源回收，不调用任何 mygo 退出 API。
// wg.Wait 带超时：任何后台 goroutine 卡在主线程调用上都不会拖死退出。
func (u *UI) Close() {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.closed = true
	tray := u.tray
	u.mu.Unlock()
	close(u.stopCh)

	waited := make(chan struct{})
	go func() {
		u.wg.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(1500 * time.Millisecond):
		logging.Warn(u.d.Log, "tray", "状态刷新协程未在超时内退出，继续关闭", nil)
	}
	u.destroyWindows()
	if tray != nil {
		tray.Destroy()
	}
}
