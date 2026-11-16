package renderer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pc-cast/logging"
)

type Options struct {
	// MPVPath 由 NewPlayer 探测后注入；留空时回退到 PATH 中的 mpv。
	MPVPath      string
	Mode         string // auto / 后端 ID / custom
	CustomPath   string // Mode == custom 时使用的可执行文件
	Fullscreen   bool
	AutoPlay     bool
	WindowWidth  int // 0 表示跟随视频原始尺寸
	WindowHeight int
}

type ipcMessage struct {
	RequestID int64           `json:"request_id"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
	Event     string          `json:"event"`
	Name      string          `json:"name"`
	Reason    string          `json:"reason"`
}

type ipcResponse struct {
	err  string
	data json.RawMessage
}

// MPV 以常驻子进程方式运行 mpv，通过 JSON IPC 控制播放。
type MPV struct {
	opts Options
	log  *slog.Logger

	mu       sync.RWMutex
	cmd      *exec.Cmd
	conn     io.ReadWriteCloser
	addr     string
	path     string
	started  bool
	starting bool
	closed   bool
	hook     func()
	fullScr  bool
	autoPlay bool
	width    int
	height   int

	uri           string
	meta          Metadata
	paused        bool
	idle          bool
	stopped       bool
	transitioning bool
	pos           float64
	duration      float64
	volume        float64
	mute          bool

	writeMu   sync.Mutex
	reqID     atomic.Int64
	pendingMu sync.Mutex
	pending   map[int64]chan ipcResponse

	notify chan struct{}
	stopCh chan struct{}
	wg     sync.WaitGroup
}

func New(opts Options, log *slog.Logger) *MPV {
	if log == nil {
		log = logging.New(slog.LevelInfo, io.Discard)
	}
	return &MPV{
		opts:     opts,
		log:      log,
		fullScr:  opts.Fullscreen,
		autoPlay: opts.AutoPlay,
		width:    opts.WindowWidth,
		height:   opts.WindowHeight,
		volume:   100,
		idle:     true,
		pending:  make(map[int64]chan ipcResponse),
		notify:   make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
	}
}

func (m *MPV) Start() error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	if m.closed {
		m.mu.Unlock()
		return errors.New("mpv 已关闭")
	}
	if m.starting {
		m.mu.Unlock()
		return errors.New("mpv 正在启动")
	}
	m.starting = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.starting = false
		m.mu.Unlock()
	}()

	path, err := m.resolvePath()
	if err != nil {
		return err
	}
	addr := ipcAddress()
	cleanupIPC(addr)

	args := []string{
		"--idle=yes",
		"--force-window=no", // 启动时不弹出播放器窗口，只有托盘图标
		"--no-terminal",
		"--input-ipc-server=" + addr,
		"--title=PC-Cast",
		"--msg-level=all=warn",
		"--keep-open=no",
	}
	m.mu.RLock()
	fs, w, h := m.fullScr, m.width, m.height
	m.mu.RUnlock()
	if fs {
		args = append(args, "--fs")
	} else if w > 0 && h > 0 {
		// geometry 只约束窗口初始尺寸，不改变视频解码分辨率。
		args = append(args, fmt.Sprintf("--geometry=%dx%d", w, h))
	}

	cmd := exec.Command(path, args...)
	prepareCommand(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 mpv 失败: %w", err)
	}

	// mpv 创建 IPC 端点需要一点时间，轮询等待。
	var conn io.ReadWriteCloser
	deadline := time.Now().Add(6 * time.Second)
	for {
		conn, err = dialIPC(addr)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			cleanupIPC(addr)
			return fmt.Errorf("连接 mpv IPC 超时: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	m.mu.Lock()
	m.cmd, m.conn, m.addr, m.path, m.started = cmd, conn, addr, path, true
	m.mu.Unlock()

	m.wg.Add(2)
	go m.readLoop(conn)
	go m.notifyLoop()

	props := []string{"pause", "idle-active", "time-pos", "duration", "volume", "mute", "path"}
	for i, prop := range props {
		if _, err := m.send([]any{"observe_property", i + 1, prop}); err != nil {
			logging.Warn(m.log, "mpv", "订阅属性失败", map[string]any{"property": prop, "error": err.Error()})
		}
	}
	for _, prop := range []string{"pause", "idle-active", "volume", "mute"} {
		data, err := m.call([]any{"get_property", prop})
		if err == nil {
			m.applyProperty(prop, data)
		}
	}
	logging.Info(m.log, "mpv", "mpv 已就绪", map[string]any{"path": path, "ipc": addr})
	return nil
}

func (m *MPV) readLoop(conn io.Reader) {
	defer m.wg.Done()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 4096), 1024*1024)
	for sc.Scan() {
		var msg ipcMessage
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		if msg.RequestID != 0 {
			m.pendingMu.Lock()
			ch := m.pending[msg.RequestID]
			delete(m.pending, msg.RequestID)
			m.pendingMu.Unlock()
			if ch != nil {
				ch <- ipcResponse{err: msg.Error, data: msg.Data}
			}
			continue
		}
		m.handleEvent(msg)
	}
}

func (m *MPV) handleEvent(msg ipcMessage) {
	switch msg.Event {
	case "property-change":
		m.applyProperty(msg.Name, msg.Data)
	case "start-file":
		m.mu.Lock()
		m.transitioning = true
		m.mu.Unlock()
		m.signalChange()
	case "file-loaded", "playback-restart":
		m.mu.Lock()
		m.transitioning, m.idle, m.stopped = false, false, false
		m.mu.Unlock()
		m.signalChange()
	case "end-file", "idle":
		m.mu.Lock()
		m.idle, m.transitioning, m.paused, m.pos = true, false, false, 0
		m.mu.Unlock()
		m.signalChange()
	}
}

func (m *MPV) applyProperty(name string, data json.RawMessage) {
	if len(data) == 0 {
		return
	}
	notify := true
	switch name {
	case "pause":
		var v bool
		if json.Unmarshal(data, &v) == nil {
			m.mu.Lock()
			m.paused = v
			if !v && m.uri != "" {
				m.stopped = false
			}
			m.mu.Unlock()
		}
	case "idle-active":
		var v bool
		if json.Unmarshal(data, &v) == nil {
			m.mu.Lock()
			m.idle = v
			m.mu.Unlock()
		}
	case "time-pos":
		var v float64
		if json.Unmarshal(data, &v) == nil {
			m.mu.Lock()
			m.pos = v
			m.mu.Unlock()
		}
		notify = false // 播放进度由 GetPositionInfo 主动拉取，不推事件
	case "duration":
		var v float64
		if json.Unmarshal(data, &v) == nil {
			m.mu.Lock()
			m.duration = v
			m.mu.Unlock()
		}
	case "volume":
		var v float64
		if json.Unmarshal(data, &v) == nil {
			m.mu.Lock()
			m.volume = v
			m.mu.Unlock()
		}
	case "mute":
		var v bool
		if json.Unmarshal(data, &v) == nil {
			m.mu.Lock()
			m.mute = v
			m.mu.Unlock()
		}
	default:
		notify = false
	}
	if notify {
		m.signalChange()
	}
}

func (m *MPV) signalChange() {
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

func (m *MPV) notifyLoop() {
	defer m.wg.Done()
	for {
		select {
		case <-m.notify:
			m.mu.RLock()
			hook := m.hook
			m.mu.RUnlock()
			if hook != nil {
				hook()
			}
		case <-m.stopCh:
			return
		}
	}
}

func (m *MPV) send(command []any) (int64, error) {
	m.mu.RLock()
	conn, started := m.conn, m.started
	m.mu.RUnlock()
	if !started || conn == nil {
		return 0, errors.New("mpv 未启动")
	}
	id := m.reqID.Add(1)
	ch := make(chan ipcResponse, 1)
	m.pendingMu.Lock()
	m.pending[id] = ch
	m.pendingMu.Unlock()
	payload, err := json.Marshal(map[string]any{"command": command, "request_id": id})
	if err != nil {
		return 0, err
	}
	payload = append(payload, '\n')
	m.writeMu.Lock()
	_, err = conn.Write(payload)
	m.writeMu.Unlock()
	if err != nil {
		m.pendingMu.Lock()
		delete(m.pending, id)
		m.pendingMu.Unlock()
		return 0, err
	}
	return id, nil
}

func (m *MPV) call(command []any) (json.RawMessage, error) {
	id, err := m.send(command)
	if err != nil {
		return nil, err
	}
	m.pendingMu.Lock()
	ch := m.pending[id]
	m.pendingMu.Unlock()
	if ch == nil {
		return nil, errors.New("IPC 请求已丢失")
	}
	defer func() {
		m.pendingMu.Lock()
		delete(m.pending, id)
		m.pendingMu.Unlock()
	}()
	select {
	case resp := <-ch:
		if resp.err != "" && resp.err != "success" {
			return nil, errors.New(resp.err)
		}
		return resp.data, nil
	case <-time.After(3 * time.Second):
		return nil, fmt.Errorf("mpv IPC 超时: %v", command)
	case <-m.stopCh:
		return nil, errors.New("mpv 正在退出")
	}
}

func (m *MPV) SetURI(uri string, meta Metadata) error {
	if uri == "" {
		return errors.New("播放地址为空")
	}
	if _, err := m.call([]any{"loadfile", uri, "replace"}); err != nil {
		return fmt.Errorf("加载媒体失败: %w", err)
	}
	m.mu.Lock()
	m.uri, m.meta = uri, meta
	m.stopped, m.transitioning, m.pos, m.duration = false, true, 0, 0
	m.mu.Unlock()
	m.mu.RLock()
	auto := m.autoPlay
	m.mu.RUnlock()
	if _, err := m.call([]any{"set_property", "pause", !auto}); err != nil {
		return fmt.Errorf("设置播放状态失败: %w", err)
	}
	m.signalChange()
	logging.Info(m.log, "mpv", "收到投屏", map[string]any{"uri": uri, "title": meta.Title})
	return nil
}

func (m *MPV) Play(speed string) error {
	if speed != "" && speed != "1" {
		if f, err := strconv.ParseFloat(speed, 64); err == nil && f > 0 {
			_, _ = m.call([]any{"set_property", "speed", f})
		}
	}
	if _, err := m.call([]any{"set_property", "pause", false}); err != nil {
		return err
	}
	m.mu.Lock()
	m.paused, m.stopped = false, false
	m.mu.Unlock()
	m.signalChange()
	return nil
}

func (m *MPV) Pause() error {
	if _, err := m.call([]any{"set_property", "pause", true}); err != nil {
		return err
	}
	m.mu.Lock()
	m.paused = true
	m.mu.Unlock()
	m.signalChange()
	return nil
}

func (m *MPV) Stop() error {
	if _, err := m.call([]any{"stop"}); err != nil {
		return err
	}
	m.mu.Lock()
	m.stopped, m.idle, m.paused, m.transitioning, m.pos = true, true, false, false, 0
	m.mu.Unlock()
	m.signalChange()
	return nil
}

func (m *MPV) Seek(unit, target string) error {
	switch strings.ToUpper(unit) {
	case "", "REL_TIME", "ABS_TIME":
		d, err := ParseRelTime(target)
		if err != nil {
			return err
		}
		_, err = m.call([]any{"seek", d.Seconds(), "absolute"})
		return err
	case "TRACK_NR":
		n, err := strconv.Atoi(strings.TrimSpace(target))
		if err != nil || n < 1 {
			return fmt.Errorf("无效的 TRACK_NR %q", target)
		}
		_, err = m.call([]any{"set_property", "playlist-pos", n - 1})
		return err
	default:
		return fmt.Errorf("不支持的 Seek 单位 %q", unit)
	}
}

func (m *MPV) SetVolume(v int) error {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	if _, err := m.call([]any{"set_property", "volume", v}); err != nil {
		return err
	}
	m.mu.Lock()
	m.volume = float64(v)
	m.mu.Unlock()
	m.signalChange()
	return nil
}

func (m *MPV) Volume() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return int(m.volume)
}

func (m *MPV) SetMute(on bool) error {
	if _, err := m.call([]any{"set_property", "mute", on}); err != nil {
		return err
	}
	m.mu.Lock()
	m.mute = on
	m.mu.Unlock()
	m.signalChange()
	return nil
}

func (m *MPV) Mute() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mute
}

func (m *MPV) State() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped {
		return StateStopped
	}
	if m.transitioning {
		return StateTransitioning
	}
	if m.uri == "" || m.idle {
		return StateNoMedia
	}
	if m.paused {
		return StatePaused
	}
	return StatePlaying
}

func (m *MPV) Position() (time.Duration, time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return time.Duration(m.pos * float64(time.Second)), time.Duration(m.duration * float64(time.Second))
}

func (m *MPV) CurrentURI() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.uri
}

func (m *MPV) CurrentMetadata() Metadata {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.meta
}

func (m *MPV) SetChangeHook(hook func()) {
	m.mu.Lock()
	m.hook = hook
	m.mu.Unlock()
}

func (m *MPV) ApplyRuntimeOptions(fullscreen, autoPlay bool) {
	m.mu.Lock()
	old := m.fullScr
	m.fullScr, m.autoPlay = fullscreen, autoPlay
	started := m.started
	m.mu.Unlock()
	if started && old != fullscreen {
		_, _ = m.call([]any{"set_property", "fs", fullscreen})
	}
}

// ApplyWindowSize 让窗口尺寸设置在运行期生效。mpv 支持运行时改 geometry；
// 老版本或全屏状态下静默降级为「下次启动生效」，不打断当前播放。
func (m *MPV) ApplyWindowSize(w, h int) {
	m.mu.Lock()
	m.width, m.height = w, h
	started, fs := m.started, m.fullScr
	m.mu.Unlock()
	if !started || fs || w <= 0 || h <= 0 {
		return
	}
	_, _ = m.call([]any{"set_property", "geometry", fmt.Sprintf("%dx%d", w, h)})
}

func (m *MPV) Path() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.path
}

func (m *MPV) Version() string {
	path := m.Path()
	if path == "" {
		p, err := m.resolvePath()
		if err != nil {
			return "未安装"
		}
		path = p
	}
	cmd := exec.Command(path, "--version")
	prepareCommand(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "未知"
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

// resolvePath 只做路径校验；跨平台的候选探测统一由 detect.go 负责，
// 避免同一份安装路径清单在两处漂移。
func (m *MPV) resolvePath() (string, error) {
	m.mu.RLock()
	configured := m.opts.MPVPath
	m.mu.RUnlock()
	if configured != "" {
		if p, err := exec.LookPath(configured); err == nil {
			return p, nil
		}
		if st, err := os.Stat(configured); err == nil && !st.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("指定的 mpv 路径不可用: %s", configured)
	}
	if p, err := exec.LookPath("mpv"); err == nil {
		return p, nil
	}
	return "", errors.New("未找到 mpv 可执行文件")
}

func (m *MPV) Descriptor() Descriptor {
	return Descriptor{
		ID: "mpv", Name: "mpv", Path: m.Path(), Version: m.Version(),
		Capability: CapFull, Note: capabilityNote(CapFull),
	}
}

func (m *MPV) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	conn, cmd, addr, started := m.conn, m.cmd, m.addr, m.started
	m.mu.Unlock()

	close(m.stopCh)
	if started && conn != nil {
		_, _ = m.send([]any{"quit"})
		_ = conn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	cleanupIPC(addr)
	m.wg.Wait()
	logging.Info(m.log, "mpv", "mpv 已退出", nil)
	return nil
}
