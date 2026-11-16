package renderer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"pc-cast/logging"
)

// childProcess 把 exec.Cmd 收敛成两个动作，便于注入假进程做并发测试。
type childProcess interface {
	Kill() error
	Wait() error
}

type launcher func(argv []string) (childProcess, error)

type osProcess struct{ cmd *exec.Cmd }

func (p *osProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (p *osProcess) Wait() error { return p.cmd.Wait() }

func osLauncher(argv []string) (childProcess, error) {
	if len(argv) == 0 {
		return nil, errors.New("空的播放器命令")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	prepareCommand(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &osProcess{cmd: cmd}, nil
}

// External 用于「只能拉起进程播放」的后端（VLC / PotPlayer / ffplay / 系统默认程序等）。
// 它没有 IPC，因此暂停/跳转通过「记录位置 → 杀进程 → 带 seek 参数重启」实现，
// 播放进度以墙钟估算。所有共享状态由 mu 保护，进程退出回调靠 gen 区分新旧实例。
type External struct {
	desc     Descriptor
	cand     candidate
	log      *slog.Logger
	launchFn launcher

	mu            sync.RWMutex
	proc          childProcess
	gen           uint64
	running       bool
	closed        bool
	uri           string
	meta          Metadata
	state         State
	posBase       time.Duration
	startedAt     time.Time
	duration      time.Duration
	volume        int
	mute          bool
	fullScr       bool
	autoPlay      bool
	width         int
	height        int
	hook          func()
	notifyStarted bool

	versionOnce sync.Once
	version     string

	notify chan struct{}
	stopCh chan struct{}
	wg     sync.WaitGroup
}

func NewExternal(sel selection, opts Options, log *slog.Logger) *External {
	if log == nil {
		log = logging.New(slog.LevelInfo, io.Discard)
	}
	e := &External{
		desc: Descriptor{
			ID: sel.cand.id, Name: sel.cand.name, Path: sel.path,
			Capability: sel.cand.capability, Note: capabilityNote(sel.cand.capability),
		},
		cand:     sel.cand,
		log:      log,
		launchFn: osLauncher,
		state:    StateNoMedia,
		volume:   100,
		fullScr:  opts.Fullscreen,
		autoPlay: opts.AutoPlay,
		width:    opts.WindowWidth,
		height:   opts.WindowHeight,
		notify:   make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
	}
	return e
}

func (e *External) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("播放器已关闭")
	}
	if !e.notifyStarted {
		e.wg.Add(1)
		go e.notifyLoop()
	}
	if e.desc.Capability == CapNone {
		return errors.New("未找到任何可用播放器，请在设置中指定播放器路径")
	}
	logging.Info(e.log, "player", "播放器后端已就绪", map[string]any{
		"backend": e.desc.ID, "name": e.desc.Name, "path": e.desc.Path,
		"capability": string(e.desc.Capability),
	})
	return nil
}

func (e *External) notifyLoop() {
	defer e.wg.Done()
	for {
		select {
		case <-e.notify:
			e.mu.RLock()
			hook := e.hook
			e.mu.RUnlock()
			if hook != nil {
				hook()
			}
		case <-e.stopCh:
			return
		}
	}
}

func (e *External) signalChange() {
	select {
	case e.notify <- struct{}{}:
	default:
	}
}

// killLocked 只发 Kill，不 Wait：Wait 必须由对应的 wait goroutine 完成，
// 否则会出现重复 Wait 或持锁等待进程退出的死锁。
func (e *External) killLocked() {
	if e.proc != nil {
		e.gen++ // 使旧 wait goroutine 的退出回调失效
		_ = e.proc.Kill()
		e.proc = nil
	}
	e.running = false
}

func (e *External) launchLocked(pos time.Duration) error {
	if e.closed {
		return errors.New("播放器已关闭")
	}
	e.killLocked()
	if e.desc.Path == "" {
		return errors.New("未找到任何可用播放器，请在设置中指定播放器路径")
	}
	argv := buildArgvWithSize(e.cand, e.desc.Path, e.uri, pos, e.fullScr, e.width, e.height)
	if e.fullScr && !e.cand.hasFullscreen() {
		// 静默丢弃用户的全屏设置是最难排查的一类问题，这里明确留证据。
		logging.Warn(e.log, "player", "当前后端不支持命令行全屏参数，全屏设置不会生效",
			map[string]any{"backend": e.desc.ID, "name": e.desc.Name})
	}
	proc, err := e.launchFn(argv)
	if err != nil {
		return fmt.Errorf("启动 %s 失败: %w", e.desc.Name, err)
	}
	e.gen++
	gen := e.gen
	e.proc = proc
	e.running = true
	system := e.desc.Capability == CapSystem
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		_ = proc.Wait() // 回收进程，避免僵尸进程 / 句柄泄漏
		e.mu.Lock()
		if e.gen == gen && !e.closed {
			e.proc = nil
			e.running = false
			if !system {
				// 系统默认程序拉起后立即返回，不能据此判定播放结束。
				e.state = StateStopped
				e.posBase = 0
			}
		}
		e.mu.Unlock()
		e.signalChange()
	}()
	logging.Info(e.log, "player", "已拉起播放器", map[string]any{
		"backend": e.desc.ID, "uri": e.uri, "seek_seconds": pos.Seconds(),
		"fullscreen": e.fullScr, "window_size": fmt.Sprintf("%dx%d", e.width, e.height),
	})
	return nil
}

func (e *External) currentPosLocked() time.Duration {
	pos := e.posBase
	if e.state == StatePlaying && !e.startedAt.IsZero() {
		pos += time.Since(e.startedAt)
	}
	if e.duration > 0 && pos > e.duration {
		pos = e.duration
	}
	if pos < 0 {
		pos = 0
	}
	return pos
}

func (e *External) SetURI(uri string, meta Metadata) error {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return errors.New("播放地址为空")
	}
	e.mu.Lock()
	e.uri, e.meta = uri, meta
	e.posBase, e.duration = 0, meta.Duration
	e.startedAt = time.Time{}
	if e.autoPlay {
		err := e.launchLocked(0)
		if err == nil {
			e.state, e.startedAt = StatePlaying, time.Now()
		} else {
			e.state = StateStopped
		}
		e.mu.Unlock()
		if err != nil {
			return err
		}
	} else {
		e.killLocked()
		e.state = StatePaused // 等待控制点发 Play，此时不启动进程
		e.mu.Unlock()
	}
	e.signalChange()
	logging.Info(e.log, "player", "收到投屏", map[string]any{
		"uri": uri, "title": meta.Title, "backend": e.desc.ID,
	})
	return nil
}

func (e *External) Play(speed string) error {
	e.mu.Lock()
	if e.uri == "" {
		e.mu.Unlock()
		return errors.New("没有可播放的媒体")
	}
	if !e.running {
		if err := e.launchLocked(e.posBase); err != nil {
			e.mu.Unlock()
			return err
		}
	}
	e.state, e.startedAt = StatePlaying, time.Now()
	e.mu.Unlock()
	e.signalChange()
	return nil
}

func (e *External) Pause() error {
	e.mu.Lock()
	pos := e.currentPosLocked()
	e.posBase = pos
	e.startedAt = time.Time{}
	e.state = StatePaused
	e.killLocked() // 没有 IPC，只能真正停掉进程来实现「暂停」
	e.mu.Unlock()
	e.signalChange()
	logging.Debug(e.log, "player", "基础模式暂停", map[string]any{"at_seconds": pos.Seconds()})
	return nil
}

func (e *External) Stop() error {
	e.mu.Lock()
	e.killLocked()
	e.state = StateStopped
	e.posBase = 0
	e.startedAt = time.Time{}
	e.mu.Unlock()
	e.signalChange()
	return nil
}

func (e *External) Seek(unit, target string) error {
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
	e.mu.Lock()
	wasPlaying := e.state == StatePlaying
	e.posBase = d
	if wasPlaying {
		if e.cand.seekArgs != nil {
			// 只有支持命令行 seek 的后端（ffplay 等）才需要重启进程。
			if err := e.launchLocked(d); err != nil {
				e.mu.Unlock()
				return err
			}
		} else {
			// WMP / MPC / PotPlayer / system 没有 seek 参数，重启也跳不到
			// 目标位置，反而会杀掉当前全屏窗口：控制点（实测小红书在投屏
			// 后约 1s 就发一次 Seek）会看到「刚全屏又缩回窗口」。
			// 这里只同步内部进度，保持画面与全屏状态连续。
			logging.Info(e.log, "player", "后端不支持命令行跳转，仅同步进度不重启播放器",
				map[string]any{"backend": e.desc.ID, "seek_seconds": d.Seconds()})
		}
		e.startedAt = time.Now()
	} else {
		e.startedAt = time.Time{}
	}
	e.mu.Unlock()
	e.signalChange()
	return nil
}

func (e *External) SetVolume(v int) error {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	e.mu.Lock()
	e.volume = v
	e.mu.Unlock()
	logging.Debug(e.log, "player", "后端不支持 IPC 音量控制，已记帐", map[string]any{"volume": v})
	e.signalChange()
	return nil
}

func (e *External) Volume() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.volume
}

func (e *External) SetMute(on bool) error {
	e.mu.Lock()
	e.mute = on
	e.mu.Unlock()
	logging.Debug(e.log, "player", "后端不支持 IPC 静音控制，已记帐", map[string]any{"mute": on})
	e.signalChange()
	return nil
}

func (e *External) Mute() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mute
}

func (e *External) State() State {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}

func (e *External) Position() (time.Duration, time.Duration) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.meta.Live {
		// 直播没有时长，墙钟估算会无上限增长，直接返回零值。
		return 0, 0
	}
	pos := e.currentPosLocked()
	dur := e.duration
	if e.meta.Duration > 0 {
		dur = e.meta.Duration
	}
	return pos, dur
}

func (e *External) CurrentURI() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.uri
}

func (e *External) CurrentMetadata() Metadata {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.meta
}

func (e *External) SetChangeHook(hook func()) {
	e.mu.Lock()
	e.hook = hook
	e.mu.Unlock()
}

func (e *External) ApplyRuntimeOptions(fullscreen, autoPlay bool) {
	e.mu.Lock()
	changed := e.fullScr != fullscreen
	e.fullScr, e.autoPlay = fullscreen, autoPlay
	if changed && e.running && e.state == StatePlaying {
		pos := e.currentPosLocked()
		e.posBase = pos
		if err := e.launchLocked(pos); err == nil {
			e.startedAt = time.Now()
		}
	}
	e.mu.Unlock()
}

// ApplyWindowSize 记录新尺寸。无 IPC 的后端无法在运行中改窗口，
// 下次启动播放器进程时生效，不打断当前播放。
func (e *External) ApplyWindowSize(w, h int) {
	e.mu.Lock()
	e.width, e.height = w, h
	e.mu.Unlock()
}

func (e *External) Path() string { return e.desc.Path }
func (e *External) Descriptor() Descriptor {
	d := e.desc
	d.Version = e.Version()
	return d
}

func (e *External) Version() string {
	e.versionOnce.Do(func() {
		if e.desc.Capability == CapSystem {
			e.version = "系统默认程序"
			return
		}
		if e.desc.Capability == CapNone || e.desc.Path == "" {
			e.version = "未找到"
			return
		}
		args := e.cand.versionArgs
		if len(args) == 0 {
			e.version = "未知"
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, e.desc.Path, args...)
		prepareCommand(cmd)
		out, err := cmd.Output()
		if err != nil {
			e.version = "未知"
			return
		}
		line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if len(line) > 80 {
			line = line[:80]
		}
		e.version = line
	})
	return e.version
}

func (e *External) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.killLocked()
	e.mu.Unlock()
	close(e.stopCh)
	e.wg.Wait()
	logging.Info(e.log, "player", "播放器已退出", map[string]any{"backend": e.desc.ID})
	return nil
}
