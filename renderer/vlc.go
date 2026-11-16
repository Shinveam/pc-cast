package renderer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"pc-cast/logging"
)

// vlcStatus 是 VLC Lua HTTP 接口 /requests/status.json 中我们关心的字段。
// time / length 单位为秒；直播流的 length 通常为 0 或 -1。
type vlcStatus struct {
	State      string  `json:"state"`
	Time       float64 `json:"time"`
	Length     float64 `json:"length"`
	Position   float64 `json:"position"`
	Volume     float64 `json:"volume"`
	Fullscreen bool    `json:"fullscreen"`
}

// vlcSpawn 拉起常驻 VLC 并等 HTTP 接口就绪，返回进程、baseURL 与密码。
// 抽成可注入函数，便于用 httptest 覆盖状态机与并发而不真的启动播放器。
type vlcSpawn func(v *VLC) (childProcess, string, string, error)

// VLC 通过 VLC 3+ 的 Lua HTTP 接口实现完整控制（CapFull）。
//
// 进程模型：启动时以 --intf=dummy 拉起一个**常驻但无主界面**的 VLC，
// 因此不会弹出空窗口；播放时由 VLC 自己创建独立的视频输出窗口。
// 状态来自 400ms 间隔的 status.json 轮询，控制命令走 /requests/status.json?command=...。
// 这样即使没装 mpv，也能拿到真实进度、暂停、跳转、音量，并支持 HLS 直播流。
type VLC struct {
	desc Descriptor
	opts Options
	log  *slog.Logger

	spawn vlcSpawn

	mu       sync.RWMutex
	proc     childProcess
	gen      uint64
	started  bool
	starting bool
	closed   bool
	baseURL  string
	password string
	httpc    *http.Client

	// 以下字段是 status.json 的本地镜像，全部由 mu 保护。
	state    State
	pos      time.Duration
	duration time.Duration
	volume   int
	mute     bool
	uri      string
	meta     Metadata

	hook     func()
	fullScr  bool
	autoPlay bool
	width    int
	height   int

	notify   chan struct{}
	stopCh   chan struct{}
	wg       sync.WaitGroup
	pollOnce sync.Once

	versionOnce sync.Once
	version     string
}

// NewVLC 构造 VLC 后端；实际进程在 Start 时拉起，握手失败由 factory 降级。
func NewVLC(sel selection, opts Options, log *slog.Logger) *VLC {
	if log == nil {
		log = logging.New(slog.LevelInfo, io.Discard)
	}
	return &VLC{
		desc: Descriptor{
			ID: sel.cand.id, Name: sel.cand.name, Path: sel.path,
			Capability: CapFull, Note: capabilityNote(CapFull),
		},
		opts:     opts,
		log:      log,
		spawn:    spawnVLC,
		state:    StateNoMedia,
		volume:   100,
		fullScr:  opts.Fullscreen,
		autoPlay: opts.AutoPlay,
		width:    opts.WindowWidth,
		height:   opts.WindowHeight,
		notify:   make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
	}
}

func (v *VLC) Start() error {
	v.mu.Lock()
	if v.started {
		v.mu.Unlock()
		return nil
	}
	if v.closed {
		v.mu.Unlock()
		return errors.New("VLC 已关闭")
	}
	if v.starting {
		v.mu.Unlock()
		return errors.New("VLC 正在启动")
	}
	v.starting = true
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.starting = false
		v.mu.Unlock()
	}()

	if v.desc.Path == "" {
		return errors.New("未找到 VLC 可执行文件")
	}
	proc, base, password, err := v.spawn(v)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}

	v.mu.Lock()
	v.proc, v.baseURL, v.password, v.httpc, v.started = proc, base, password, client, true
	v.gen++
	gen := v.gen
	v.mu.Unlock()

	v.wg.Add(1)
	go func() {
		defer v.wg.Done()
		_ = proc.Wait() // 回收进程；Kill 后由这里完成 Wait，避免重复 Wait
		v.mu.Lock()
		if v.gen == gen && !v.closed {
			v.proc, v.started = nil, false
			v.state = StateStopped
		}
		v.mu.Unlock()
		v.signalChange()
		logging.Warn(v.log, "vlc", "VLC 进程已退出", map[string]any{"gen": gen})
	}()
	v.pollOnce.Do(func() {
		v.wg.Add(1)
		go v.pollLoop()
	})
	v.wg.Add(1)
	go v.notifyLoop()

	logging.Info(v.log, "vlc", "VLC HTTP 控制接口已就绪", map[string]any{
		"path": v.desc.Path, "endpoint": base,
	})
	// pl_forcepause / pl_forceresume 是 VLC 3.x Lua HTTP 接口提供的幂等命令；
	// 老版本会静默忽略，这里只记一条告警，不阻断启动也不影响播放。
	if major := vlcMajorVersion(v.Version()); major > 0 && major < 3 {
		logging.Warn(v.log, "vlc", "VLC 版本过旧，暂停/恢复命令可能不完整（建议 3.0+）",
			map[string]any{"version": v.Version()})
	}
	return nil
}

func vlcMajorVersion(s string) int {
	for _, field := range strings.Fields(s) {
		parts := strings.Split(field, ".")
		if len(parts) < 2 {
			continue
		}
		if n, err := strconv.Atoi(parts[0]); err == nil && n > 0 && n < 10 {
			return n
		}
	}
	return 0
}

func (v *VLC) signalChange() {
	select {
	case v.notify <- struct{}{}:
	default:
	}
}

func (v *VLC) notifyLoop() {
	defer v.wg.Done()
	for {
		select {
		case <-v.notify:
			v.mu.RLock()
			hook := v.hook
			v.mu.RUnlock()
			if hook != nil {
				hook()
			}
		case <-v.stopCh:
			return
		}
	}
}

func (v *VLC) pollLoop() {
	defer v.wg.Done()
	t := time.NewTicker(400 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-v.stopCh:
			return
		case <-t.C:
			v.refreshStatus()
		}
	}
}

// refreshStatus 把 VLC 的真实状态映射到 Player 接口。
// 只在状态变化时推 GENA 事件；进度变化由 GetPositionInfo 主动拉取，避免每 400ms 推事件。
func (v *VLC) refreshStatus() {
	st, err := v.command("", nil)
	if err != nil {
		return // 接口尚未就绪或请求超时，保持上一次状态
	}
	v.mu.Lock()
	newState := StatePlaying
	switch strings.ToLower(strings.TrimSpace(st.State)) {
	case "playing":
		newState = StatePlaying
	case "paused":
		newState = StatePaused
	case "stopped", "":
		if v.uri == "" {
			newState = StateNoMedia
		} else {
			newState = StateStopped
		}
	}
	pos := time.Duration(st.Time * float64(time.Second))
	dur := time.Duration(st.Length * float64(time.Second))
	if v.meta.Live || st.Length <= 0 || pos < 0 {
		pos, dur = 0, 0
	}
	if !v.mute && st.Volume > 0 {
		vol := int(st.Volume / 2.56) // VLC: 256 = 100%
		if vol < 0 {
			vol = 0
		}
		if vol > 100 {
			vol = 100
		}
		v.volume = vol
	}
	changed := v.state != newState
	v.state, v.pos, v.duration = newState, pos, dur
	v.mu.Unlock()
	if changed {
		v.signalChange()
	}
}

// command 向 Lua HTTP 接口发送命令；响应本身就是 status.json。
func (v *VLC) command(name string, params url.Values) (*vlcStatus, error) {
	v.mu.RLock()
	base, pw, client, started := v.baseURL, v.password, v.httpc, v.started
	v.mu.RUnlock()
	if !started || client == nil || base == "" {
		return nil, errors.New("VLC 未启动")
	}
	return vlcRequest(client, base, pw, name, params)
}

func vlcRequest(client *http.Client, base, password, command string, params url.Values) (*vlcStatus, error) {
	q := url.Values{}
	for k, vs := range params {
		q[k] = append([]string{}, vs...)
	}
	if command != "" {
		q.Set("command", command)
	}
	u := base + "/requests/status.json"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("", password) // VLC 要求用户名留空
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("VLC HTTP 状态 %d", resp.StatusCode)
	}
	var st vlcStatus
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("解析 VLC 状态失败: %w", err)
	}
	return &st, nil
}

func (v *VLC) SetURI(uri string, meta Metadata) error {
	if strings.TrimSpace(uri) == "" {
		return errors.New("播放地址为空")
	}
	// 先清空播放列表，避免上一部片源残留导致意外的 Next / 自动续播。
	if _, err := v.command("pl_empty", nil); err != nil {
		return fmt.Errorf("VLC 清空播放列表失败: %w", err)
	}
	if _, err := v.command("in_play", url.Values{"input": []string{uri}}); err != nil {
		return fmt.Errorf("VLC 加载媒体失败: %w", err)
	}
	v.mu.Lock()
	v.uri, v.meta, v.pos, v.duration = uri, meta, 0, 0
	v.state = StateTransitioning
	if !v.autoPlay {
		v.state = StatePaused
	}
	v.mu.Unlock()
	if v.autoPlay {
		_, _ = v.command("pl_forceresume", nil) // 从上一部的暂停态中恢复
	} else {
		_, _ = v.command("pl_forcepause", nil)
	}
	v.signalChange()
	logging.Info(v.log, "vlc", "收到投屏", map[string]any{
		"uri": uri, "title": meta.Title, "live": meta.Live,
	})
	return nil
}

func (v *VLC) Play(speed string) error {
	v.mu.RLock()
	uri, state := v.uri, v.state
	v.mu.RUnlock()
	if uri == "" {
		return errors.New("没有可播放的媒体")
	}
	if state == StateStopped || state == StateNoMedia {
		if _, err := v.command("in_play", url.Values{"input": []string{uri}}); err != nil {
			return err
		}
	}
	if _, err := v.command("pl_forceresume", nil); err != nil {
		// VLC 2.x 没有 force* 命令，退回 toggle 语义的 pl_pause。
		if _, err2 := v.command("pl_pause", nil); err2 != nil {
			return err
		}
	}
	if speed != "" && speed != "1" {
		if f, err := strconv.ParseFloat(speed, 64); err == nil && f > 0 {
			_, _ = v.command("rate", url.Values{"val": []string{strconv.FormatFloat(f, 'f', 2, 64)}})
		}
	}
	v.mu.Lock()
	v.state = StatePlaying
	v.mu.Unlock()
	v.signalChange()
	return nil
}

func (v *VLC) Pause() error {
	if _, err := v.command("pl_forcepause", nil); err != nil {
		if _, err2 := v.command("pl_pause", nil); err2 != nil {
			return err
		}
	}
	v.mu.Lock()
	v.state = StatePaused
	v.mu.Unlock()
	v.signalChange()
	return nil
}

func (v *VLC) Stop() error {
	if _, err := v.command("pl_stop", nil); err != nil {
		return err
	}
	_, _ = v.command("pl_empty", nil)
	v.mu.Lock()
	v.state, v.pos, v.duration = StateStopped, 0, 0
	v.mu.Unlock()
	v.signalChange()
	return nil
}

func (v *VLC) Seek(unit, target string) error {
	v.mu.RLock()
	live := v.meta.Live
	v.mu.RUnlock()
	// 控制点在直播起播后也会立即发 Seek（实测央视频连发两次）。这里返回
	// 成功但绝不下发 seek 命令：对直播是 no-op。若返回错误，dlna 层会回
	// SOAP fault，控制点据此判定渲染器异常并主动断开投屏。
	if live {
		return nil
	}
	var d time.Duration
	switch strings.ToUpper(unit) {
	case "", "REL_TIME", "ABS_TIME":
		var err error
		d, err = ParseRelTime(target)
		if err != nil {
			return err
		}
	case "TRACK_NR":
		return nil // 单文件播放，返回成功避免控制点反复重试
	default:
		return fmt.Errorf("不支持的 Seek 单位 %q", unit)
	}
	val := strconv.FormatInt(int64(d/time.Second), 10)
	if _, err := v.command("seek", url.Values{"val": []string{val}}); err != nil {
		return err
	}
	v.mu.Lock()
	v.pos = d
	v.mu.Unlock()
	v.signalChange()
	return nil
}

func (v *VLC) SetVolume(vol int) error {
	if vol < 0 {
		vol = 0
	}
	if vol > 100 {
		vol = 100
	}
	v.mu.Lock()
	v.volume = vol
	mute := v.mute
	v.mu.Unlock()
	if !mute {
		if _, err := v.command("volume", url.Values{"val": []string{strconv.Itoa(int(float64(vol) * 2.56))}}); err != nil {
			return err
		}
	}
	v.signalChange()
	return nil
}

func (v *VLC) Volume() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.volume
}

func (v *VLC) SetMute(on bool) error {
	v.mu.Lock()
	v.mute = on
	vol := v.volume
	v.mu.Unlock()
	val := int(float64(vol) * 2.56)
	if on {
		val = 0
	}
	if _, err := v.command("volume", url.Values{"val": []string{strconv.Itoa(val)}}); err != nil {
		return err
	}
	v.signalChange()
	return nil
}

func (v *VLC) Mute() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.mute
}

func (v *VLC) State() State {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.state
}

func (v *VLC) Position() (time.Duration, time.Duration) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.meta.Live {
		return 0, 0
	}
	return v.pos, v.duration
}

func (v *VLC) CurrentURI() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.uri
}

func (v *VLC) CurrentMetadata() Metadata {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.meta
}

func (v *VLC) SetChangeHook(hook func()) {
	v.mu.Lock()
	v.hook = hook
	v.mu.Unlock()
}

func (v *VLC) ApplyRuntimeOptions(fullscreen, autoPlay bool) {
	v.mu.Lock()
	changed := v.fullScr != fullscreen
	v.fullScr, v.autoPlay = fullscreen, autoPlay
	started := v.started
	v.mu.Unlock()
	if !started || !changed {
		return
	}
	// VLC 的 fullscreen 命令是 toggle，无条件发送可能把状态切反。
	// status.json 已带真实 fullscreen 值，只在不一致时才发命令。
	st, err := v.command("", nil)
	if err != nil || st.Fullscreen == fullscreen {
		return
	}
	if _, err := v.command("fullscreen", nil); err != nil {
		logging.Warn(v.log, "vlc", "运行期切换全屏失败", map[string]any{"error": err.Error()})
	}
}

// ApplyWindowSize 常驻 VLC 进程无法在运行中改窗口尺寸，记录后下次启动生效。
func (v *VLC) ApplyWindowSize(w, h int) {
	v.mu.Lock()
	v.width, v.height = w, h
	v.mu.Unlock()
	logging.Debug(v.log, "vlc", "窗口尺寸已更新，将在下次启动 VLC 时生效",
		map[string]any{"width": w, "height": h})
}

func (v *VLC) Path() string { return v.desc.Path }

func (v *VLC) Version() string {
	v.versionOnce.Do(func() {
		if v.desc.Path == "" {
			v.version = "未找到"
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, v.desc.Path, "--version")
		prepareCommand(cmd)
		out, err := cmd.Output()
		if err != nil {
			v.version = "未知"
			return
		}
		line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if len(line) > 80 {
			line = line[:80]
		}
		v.version = line
	})
	return v.version
}

func (v *VLC) Descriptor() Descriptor {
	d := v.desc
	d.Capability = CapFull
	d.Note = capabilityNote(CapFull)
	d.Version = v.Version()
	return d
}

func (v *VLC) Close() error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	proc := v.proc
	v.proc = nil
	v.mu.Unlock()

	close(v.stopCh) // 先停轮询与通知循环，避免 Kill 后继续发请求
	if proc != nil {
		_ = proc.Kill()
	}
	v.wg.Wait()
	logging.Info(v.log, "vlc", "VLC 已退出", nil)
	return nil
}

// spawnVLC 以 dummy 主界面 + Lua HTTP 附加接口常驻运行，不带媒体启动，
// 因此应用启动时只显示托盘，播放时由 VLC 自行创建视频窗口。
func spawnVLC(v *VLC) (childProcess, string, string, error) {
	port, err := freePort()
	if err != nil {
		return nil, "", "", fmt.Errorf("分配 VLC HTTP 端口失败: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		return nil, "", "", err
	}
	v.mu.RLock()
	fs, w, h := v.fullScr, v.width, v.height
	v.mu.RUnlock()

	argv := []string{
		"--intf=dummy",
		"--extraintf=http",
		"--http-host=127.0.0.1",
		fmt.Sprintf("--http-port=%d", port),
		"--http-password=" + token,
		"--no-one-instance", // 禁止复用已有 VLC 实例，否则 HTTP 接口不在本进程
		"--no-embedded-video",
		"--no-video-title-show",
		"--no-qt-privacy-ask",
		"--no-qt-updates-notif",
		"--dummy-quiet",
	}
	if fs {
		argv = append(argv, "--fullscreen")
	} else if w > 0 && h > 0 {
		argv = append(argv, vlcGeometry(w, h)...)
	}

	cmd := exec.Command(v.desc.Path, argv...)
	prepareCommand(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, "", "", fmt.Errorf("启动 VLC 失败: %w", err)
	}
	proc := &osProcess{cmd: cmd}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 1500 * time.Millisecond}

	deadline := time.Now().Add(8 * time.Second)
	var lastErr error
	for {
		if _, err := vlcRequest(client, base, token, "", nil); err == nil {
			return proc, base, token, nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			_ = proc.Kill()
			go func() { _ = proc.Wait() }()
			return nil, "", "", fmt.Errorf("VLC HTTP 接口启动超时: %w", lastErr)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成 VLC HTTP 密码失败: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
