// Package app 负责组件装配、生命周期与配置变更编排。
package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"pc-cast/config"
	"pc-cast/dlna"
	"pc-cast/logging"
	"pc-cast/renderer"
	"pc-cast/trayui"
)

type Application struct {
	log     *slog.Logger
	logFile *os.File
	cfg     *config.Store
	ui      *trayui.UI

	startMu        sync.Mutex // 串行化 startServices，避免并发重建组件
	quitOnce       sync.Once  // 退出流程只启动一次
	mu             sync.Mutex
	player         renderer.Player
	playerDesc     renderer.Descriptor
	server         *dlna.Server
	servicesCancel context.CancelFunc
	networkKey     string
	restarting     bool
	restartPending bool // 重启期间又发生配置变更，结束后需再跑一轮以收敛到最新配置
	closed         bool
	loadWarning    string
}

// Run 是应用入口，mygo 的事件循环必须在主 goroutine 上运行。
func Run() error {
	a := &Application{}
	a.init()
	return a.run()
}

func (a *Application) init() {
	cfg, err := config.Load()
	if err != nil {
		a.loadWarning = err.Error()
		cfg = config.Default()
	}
	a.cfg = config.NewStore(cfg)
	a.setupLogger(cfg)
	if a.loadWarning != "" {
		logging.Warn(a.log, "app", "配置文件读取失败，已使用默认配置", map[string]any{"error": a.loadWarning})
	}

	a.ui = trayui.New(trayui.Deps{
		Log:        a.log,
		Config:     a.cfg,
		Server:     a.currentServer,
		Player:     a.currentPlayer,
		PlayerDesc: a.currentDesc,
		InfoLines:  a.infoLines,
		InfoText:   a.infoText,
		Quit:       a.requestQuit,
	})
}

func (a *Application) setupLogger(cfg config.Config) {
	path := config.LogsPath()
	_ = os.MkdirAll(dirOf(path), 0o755)
	// 追加而非清空：用户反馈的「刚才推的直播链接」往往发生在上一次运行，
	// O_TRUNC 会把唯一的取证记录抹掉。超过 2 MiB 时轮转出一份 .1。
	rotateLog(path)
	writers := []io.Writer{tolerantWriter{os.Stdout}}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		a.logFile = f
		writers = append(writers, f)
	}
	a.log = logging.New(logging.ParseLevel(cfg.LogLevel), writers...)
	logging.Info(a.log, "app", "PC-Cast 启动", map[string]any{
		"version": BuildInfo().Version, "os": runtime.GOOS, "arch": runtime.GOARCH,
	})
}

func rotateLog(path string) {
	const maxSize = 2 << 20
	st, err := os.Stat(path)
	if err != nil || st.Size() <= maxSize {
		return
	}
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
}

// tolerantWriter 忽略控制台写入失败：Windows GUI 子系统下 os.Stdout 不可用，
// 若直接放进 io.MultiWriter，一次失败就会中断后续的文件日志写入。
type tolerantWriter struct{ w io.Writer }

func (t tolerantWriter) Write(p []byte) (int, error) {
	_, _ = t.w.Write(p)
	return len(p), nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

func (a *Application) currentPlayer() renderer.Player {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.player
}

func (a *Application) currentDesc() renderer.Descriptor {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.playerDesc
}

func (a *Application) currentServer() *dlna.Server {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.server
}

// startServices 按当前配置自动选择播放器并创建 DLNA 服务。
// 播放器构造放在 mu 之外：mpv 的 IPC 握手可能耗时数秒，不能阻塞托盘状态刷新。
func (a *Application) startServices() ([]string, error) {
	a.startMu.Lock()
	defer a.startMu.Unlock()

	a.mu.Lock()
	if a.closed {
		// 退出流程中若仍有配置变更触发的重启，必须拒绝，否则会重新拉起服务。
		a.mu.Unlock()
		return nil, nil
	}
	already := a.servicesCancel != nil
	a.mu.Unlock()
	if already {
		return nil, nil
	}
	c := a.cfg.Get()
	if !c.CastEnabled {
		// 总开关关闭：不创建播放器、不监听 HTTP、不发 SSDP alive，
		// 手机端无法发现设备。托盘仍常驻，可随时重新勾选「启用投屏」。
		logging.Info(a.log, "app", "投屏功能已关闭，跳过服务启动", nil)
		return nil, nil
	}
	w, h := config.ParseWindowSize(c.WindowSize)
	p, desc, notes := renderer.NewPlayer(renderer.Options{
		Mode: c.PlayerMode, CustomPath: c.PlayerPath,
		Fullscreen: c.Fullscreen, AutoPlay: c.AutoPlay,
		WindowWidth: w, WindowHeight: h,
	}, a.log)
	s := dlna.New(dlna.Config{
		FriendlyName: c.FriendlyName, UUID: c.DeviceUUID, Port: c.Port, BindIP: c.BindIP,
	}, p, a.log)
	ctx, cancel := context.WithCancel(context.Background())

	a.mu.Lock()
	a.player, a.playerDesc, a.server = p, desc, s
	a.servicesCancel = cancel
	a.networkKey = s.NetworkKey()
	a.mu.Unlock()

	warnings := append([]string{}, notes...)
	if err := s.Start(); err != nil {
		cancel()
		_ = p.Close()
		a.mu.Lock()
		a.servicesCancel = nil
		a.player, a.playerDesc, a.server = nil, renderer.Descriptor{}, nil
		a.networkKey = ""
		a.mu.Unlock()
		return warnings, err
	}
	warnings = append(warnings, s.Warnings()...)
	go a.watchNetwork(ctx, s)
	return warnings, nil
}

func (a *Application) stopServices() {
	// 与 startServices 共用 startMu：避免「启动到一半被退出」留下未回收的播放器进程。
	a.startMu.Lock()
	defer a.startMu.Unlock()
	a.mu.Lock()
	cancel := a.servicesCancel
	p, s := a.player, a.server
	a.servicesCancel = nil
	// 关闭后清空引用：托盘状态刷新、软件信息与配置回调都不应再读到
	// 已经 Close 过的播放器/服务（投屏总开关关闭时尤其重要）。
	a.player, a.playerDesc, a.server = nil, renderer.Descriptor{}, nil
	a.networkKey = ""
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if s != nil {
		_ = s.Close()
	}
	if p != nil {
		_ = p.Close()
	}
}

// watchNetwork 在网卡/IP 变化时重新发送 SSDP alive 宣告。
func (a *Application) watchNetwork(ctx context.Context, s *dlna.Server) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			key := s.NetworkKey()
			a.mu.Lock()
			changed := key != "" && key != a.networkKey
			if changed {
				a.networkKey = key
			}
			a.mu.Unlock()
			if changed {
				logging.Info(a.log, "app", "检测到网络变化", map[string]any{"ips": key})
				s.Refresh()
			}
		case <-ctx.Done():
			return
		}
	}
}

func (a *Application) restartServices() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	if a.restarting {
		// 快速连点「启用投屏」等开关时不能丢弃第二次变更：
		// 标记待处理，当前轮结束后按最新配置再重启一次。
		a.restartPending = true
		a.mu.Unlock()
		return
	}
	a.restarting = true
	a.mu.Unlock()
	go func() {
		defer func() {
			a.mu.Lock()
			a.restarting = false
			pending := a.restartPending
			a.restartPending = false
			a.mu.Unlock()
			if pending {
				a.restartServices()
			}
		}()
		a.stopServices()
		time.Sleep(300 * time.Millisecond)
		warnings, err := a.startServices()
		if err != nil {
			logging.Error(a.log, "app", "服务重启失败", map[string]any{"error": err.Error()})
			a.ui.Notify("PC-Cast", "服务重启失败："+err.Error(), trayui.NotifyLimited)
			return
		}
		note := "设置已生效，投屏服务已重启"
		if !a.cfg.Get().CastEnabled {
			note = "投屏功能已关闭"
		}
		a.notifyStartup(warnings, note)
	}()
}

func (a *Application) onConfigChange(old, next config.Config) {
	logging.Info(a.log, "config", "配置已更新", map[string]any{"device": next.FriendlyName})
	if p := a.currentPlayer(); p != nil {
		p.ApplyRuntimeOptions(next.Fullscreen, next.AutoPlay)
		if old.WindowSize != next.WindowSize {
			if ws, ok := p.(renderer.WindowSizer); ok {
				w, h := config.ParseWindowSize(next.WindowSize)
				ws.ApplyWindowSize(w, h)
				logging.Info(a.log, "config", "播放窗口尺寸已更新", map[string]any{
					"preset": next.WindowSize, "width": w, "height": h,
				})
			}
		}
	}
	if s := a.currentServer(); s != nil {
		s.SetFriendlyName(next.FriendlyName)
	}
	if err := mygo.App.SetOpenAtLogin(next.OpenAtLogin); err != nil {
		logging.Warn(a.log, "config", "设置开机自启失败", map[string]any{"error": err.Error()})
	}
	if old.PlayerMode != next.PlayerMode || old.PlayerPath != next.PlayerPath ||
		old.MPVPath != next.MPVPath || old.Port != next.Port || old.BindIP != next.BindIP ||
		old.CastEnabled != next.CastEnabled {
		// 投屏总开关变化会停掉/重建 DLNA 服务与播放器，复用既有重启链路。
		a.restartServices()
	}
	if old.LogLevel != next.LogLevel {
		a.ui.Notify("PC-Cast", "日志级别将在下次启动应用后生效")
	}
}

func (a *Application) notifyStartup(warnings []string, note string) {
	title := "PC-Cast 已启动"
	lines := make([]string, 0, 4)
	if s := a.currentServer(); s != nil {
		lines = append(lines, "服务地址："+s.BaseURL())
	}
	if d := a.currentDesc(); d.Name != "" {
		lines = append(lines, "播放器："+d.Name+"（"+d.Capability.Label()+"）")
	}
	if !a.cfg.Get().CastEnabled {
		lines = append(lines, "投屏功能当前为关闭状态，右键托盘勾选「启用投屏」即可恢复")
	}
	if note != "" {
		title = "PC-Cast"
		lines = append([]string{note}, lines...)
	}
	body := strings.Join(lines, "\n")
	if len(warnings) > 0 {
		body += "\n" + strings.Join(warnings, "\n")
	}
	// 通知图标按状态取变体：总开关关闭用灰阶，能力受限（如未装 mpv / VLC）用琥珀角标。
	style := trayui.NotifyDefault
	if !a.cfg.Get().CastEnabled {
		style = trayui.NotifyDisabled
	} else if len(warnings) > 0 {
		style = trayui.NotifyLimited
	}
	a.ui.Notify(title, body, style)
	logging.Info(a.log, "app", "启动通知", map[string]any{"title": title, "warnings": warnings})
}

func (a *Application) infoLines() [][2]string {
	lines := BuildInfo().Lines()
	base, udn := "-", "-"
	if s := a.currentServer(); s != nil {
		base, udn = s.BaseURL(), s.UDN()
	}
	cfg := a.cfg.Get()
	w, h := config.ParseWindowSize(cfg.WindowSize)
	windowSize := cfg.WindowSize
	if w > 0 && h > 0 {
		windowSize = fmt.Sprintf("%s（%d×%d）", cfg.WindowSize, w, h)
	} else {
		windowSize = "auto（跟随视频原始尺寸）"
	}
	d := a.currentDesc()
	playerName := "未就绪"
	if d.Name != "" {
		playerName = d.Name + " · " + d.Capability.Label()
	}
	path := d.Path
	if path == "" {
		path = "自动探测"
	}
	version := d.Version
	if version == "" {
		if p := a.currentPlayer(); p != nil {
			version = p.Version()
		}
	}
	if version == "" {
		version = "-"
	}
	note := d.Note
	if note == "" {
		note = "-"
	}
	cfgPath, _ := config.Path()
	castState := "已开启"
	if !cfg.CastEnabled {
		castState = "已关闭（手机无法发现此设备）"
	}
	lines = append(lines,
		[2]string{"设备名称", cfg.FriendlyName},
		[2]string{"设备 UUID", udn},
		[2]string{"服务地址", base},
		[2]string{"投屏功能", castState},
		[2]string{"播放窗口", windowSize},
		[2]string{"当前播放器", playerName},
		[2]string{"播放器版本", version},
		[2]string{"播放器路径", path},
		[2]string{"控制能力", note},
		[2]string{"可用后端", availableBackends()},
		[2]string{"配置文件", cfgPath},
		[2]string{"日志文件", config.LogsPath()},
	)
	return lines
}

func availableBackends() string {
	ds := renderer.Available()
	if len(ds) == 0 {
		return "（未探测到）"
	}
	names := make([]string, 0, len(ds))
	for _, d := range ds {
		names = append(names, d.Name)
	}
	return strings.Join(names, "、")
}

func (a *Application) infoText() string {
	var b strings.Builder
	for i, kv := range a.infoLines() {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(kv[0] + ": " + kv[1])
	}
	return b.String()
}

// requestQuit 是托盘「退出」的入口。清理必须放在后台 goroutine：
// 主线程事件循环要继续响应托盘/窗口 API 的 onMain，否则 shutdown 里的
// wg.Wait 会与状态刷新的同步主线程调用互锁，进程永远卡住。
func (a *Application) requestQuit() {
	a.quitOnce.Do(func() {
		logging.Info(a.log, "app", "收到退出请求", nil)
		a.ui.BeginQuit()
		go a.quitSequence()
	})
}

func (a *Application) quitSequence() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.shutdown()
	}()
	select {
	case <-done:
		logging.Info(a.log, "app", "资源清理完成，请求结束事件循环", nil)
	case <-time.After(4 * time.Second):
		logging.Warn(a.log, "app", "资源清理超时，仍继续退出", nil)
	}
	// mygo 自带 3s stopGrace；这里再兜一层，防止任何退出回调卡住主线程。
	time.AfterFunc(6*time.Second, func() { os.Exit(0) })
	mygo.App.Quit()
}

func (a *Application) shutdown() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	a.mu.Unlock()
	a.ui.Close()
	a.stopServices()
	logging.Info(a.log, "app", "PC-Cast 已退出", nil)
	if a.logFile != nil {
		_ = a.logFile.Close()
	}
}

func (a *Application) run() error {
	mygo.App.SetName("PC-Cast")
	mygo.App.SetVersion(BuildInfo().Version)
	if runtime.GOOS == "darwin" {
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	}
	if !mygo.App.RequestSingleInstanceLock() {
		logging.Info(a.log, "app", "已有实例在运行，本实例退出", nil)
		return nil
	}
	mygo.App.OnSecondInstance(func([]string, string) {
		a.ui.Notify("PC-Cast", "程序已在运行，请使用托盘图标操作")
	})
	mygo.App.OnWindowAllClosed(func() {}) // 托盘常驻，不因窗口关闭而退出
	// OnBeforeQuit 早于窗口关闭：置退出标志，让设置/关于窗口的 OnClose 放行。
	// 托盘退出、OS 会话结束、SIGTERM 等所有路径都会经过这里。
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.ui.BeginQuit() })
	mygo.App.OnQuitCanceled(func() {
		logging.Warn(a.log, "app", "退出被取消（窗口或监听器拒绝了关闭）", nil)
	})
	// OnWillQuit 运行在主线程，绝不能在这里同步等待任何后台 goroutine：
	// 托盘/窗口 API 的 onMain 也需要主线程，会形成互锁。
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { go a.shutdown() })
	a.cfg.OnChange(a.onConfigChange)
	if a.cfg.Get().OpenAtLogin {
		if err := mygo.App.SetOpenAtLogin(true); err != nil {
			logging.Warn(a.log, "app", "同步开机自启状态失败", map[string]any{"error": err.Error()})
		}
	}

	mygo.App.WhenReady(func() {
		if err := a.ui.Ready(); err != nil {
			logging.Error(a.log, "app", "托盘创建失败", map[string]any{"error": err.Error()})
		}
		go func() {
			warnings, err := a.startServices()
			if err != nil {
				logging.Error(a.log, "app", "服务启动失败", map[string]any{"error": err.Error()})
				a.ui.Notify("PC-Cast 启动失败", fmt.Sprintf("DLNA 服务未能启动：%v", err))
				return
			}
			a.notifyStartup(warnings, "")
		}()
	})
	if err := mygo.App.Run(); err != nil {
		return fmt.Errorf("应用事件循环异常: %w", err)
	}
	return nil
}
