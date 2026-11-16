package renderer

import (
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pc-cast/logging"
)

type fakeProc struct {
	done     chan struct{}
	waitDone chan struct{}
	killed   atomic.Bool
	once     sync.Once
}

func newFakeProc() *fakeProc {
	return &fakeProc{done: make(chan struct{}), waitDone: make(chan struct{})}
}

func (p *fakeProc) Kill() error { p.killed.Store(true); return nil }

func (p *fakeProc) Wait() error {
	<-p.done
	close(p.waitDone)
	return nil
}

func (p *fakeProc) exit() { p.once.Do(func() { close(p.done) }) }

type procRegistry struct {
	mu    sync.Mutex
	procs []*fakeProc
	argvs [][]string
}

func (r *procRegistry) launch(argv []string) (childProcess, error) {
	p := newFakeProc()
	r.mu.Lock()
	r.procs = append(r.procs, p)
	r.argvs = append(r.argvs, append([]string{}, argv...))
	r.mu.Unlock()
	return p, nil
}

func (r *procRegistry) last() *fakeProc {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.procs) == 0 {
		return nil
	}
	return r.procs[len(r.procs)-1]
}

func (r *procRegistry) lastArgv() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.argvs) == 0 {
		return nil
	}
	return append([]string{}, r.argvs[len(r.argvs)-1]...)
}

func (r *procRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.procs)
}

func (r *procRegistry) exitAll() {
	r.mu.Lock()
	procs := append([]*fakeProc{}, r.procs...)
	r.mu.Unlock()
	for _, p := range procs {
		p.exit()
	}
}

func newTestExternal(t *testing.T, cap Capability, autoPlay bool) (*External, *procRegistry) {
	t.Helper()
	c := candidate{
		id: "vlc", name: "VLC", capability: cap,
		args: []string{"--no-video-title-show"}, fullscreenArg: "--fullscreen",
		seekArgs: func(d time.Duration) []string {
			return []string{"--start-time=" + strconv.FormatFloat(d.Seconds(), 'f', 3, 64)}
		},
	}
	if cap == CapNone {
		c = candidate{id: "none", name: "无可用播放器", capability: CapNone}
	}
	sel := selection{cand: c, path: "C:/VLC/vlc.exe"}
	if cap == CapNone {
		sel.path = ""
	}
	reg := &procRegistry{}
	e := NewExternal(sel, Options{AutoPlay: autoPlay}, logging.New(slog.LevelError, io.Discard))
	e.launchFn = reg.launch
	t.Cleanup(func() {
		reg.exitAll()
		_ = e.Close()
	})
	return e, reg
}

const testURI = "http://192.168.1.9/movie.mp4"

func TestExternalSetURILaunchesProcess(t *testing.T) {
	e, reg := newTestExternal(t, CapLaunch, true)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if err := e.SetURI(testURI, Metadata{URI: testURI, Title: "示例"}); err != nil {
		t.Fatal(err)
	}
	if e.State() != StatePlaying || e.CurrentURI() != testURI {
		t.Fatalf("state=%s uri=%q", e.State(), e.CurrentURI())
	}
	argv := reg.lastArgv()
	if len(argv) == 0 || argv[len(argv)-1] != testURI {
		t.Fatalf("最后一个参数必须是 URL: %#v", argv)
	}
	if reg.count() != 1 {
		t.Fatalf("应只拉起一个进程，实际 %d", reg.count())
	}
}

func TestExternalPauseFreezesPosition(t *testing.T) {
	e, _ := newTestExternal(t, CapLaunch, true)
	_ = e.Start()
	_ = e.SetURI(testURI, Metadata{URI: testURI, Duration: 5 * time.Minute})
	time.Sleep(25 * time.Millisecond)
	if err := e.Pause(); err != nil {
		t.Fatal(err)
	}
	pos1, dur := e.Position()
	if pos1 <= 0 || dur != 5*time.Minute {
		t.Fatalf("pos=%v dur=%v", pos1, dur)
	}
	time.Sleep(25 * time.Millisecond)
	pos2, _ := e.Position()
	if pos2 != pos1 {
		t.Fatalf("暂停后进度仍在推进: %v -> %v", pos1, pos2)
	}
	if e.State() != StatePaused {
		t.Fatalf("state=%s", e.State())
	}
}

func TestExternalPlayAndSeekRestartWithOffset(t *testing.T) {
	e, reg := newTestExternal(t, CapLaunch, true)
	_ = e.Start()
	_ = e.SetURI(testURI, Metadata{URI: testURI})
	if err := e.Pause(); err != nil {
		t.Fatal(err)
	}
	if err := e.Play("1"); err != nil {
		t.Fatal(err)
	}
	if e.State() != StatePlaying || reg.count() != 2 {
		t.Fatalf("Play 应重新拉起进程: state=%s count=%d", e.State(), reg.count())
	}
	if err := e.Seek("REL_TIME", "00:00:45"); err != nil {
		t.Fatal(err)
	}
	argv := reg.lastArgv()
	found := false
	for _, a := range argv {
		if a == "--start-time=45.000" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Seek 未带上跳转参数: %#v", argv)
	}
	pos, _ := e.Position()
	if pos < 45*time.Second || pos > 46*time.Second {
		t.Fatalf("pos=%v", pos)
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if e.State() != StateStopped {
		t.Fatalf("state=%s", e.State())
	}
	pos, _ = e.Position()
	if pos != 0 {
		t.Fatalf("Stop 后进度应归零: %v", pos)
	}
}

// 旧进程的退出回调不能覆盖新进程的状态：这是 kill + relaunch 场景最典型的竞态。
func TestExternalStaleExitDoesNotOverrideState(t *testing.T) {
	e, reg := newTestExternal(t, CapLaunch, true)
	_ = e.Start()
	_ = e.SetURI(testURI, Metadata{URI: testURI})
	stale := reg.last()
	_ = e.Pause()
	_ = e.Play("1")
	fresh := reg.last()
	if stale == fresh {
		t.Fatal("Play 未创建新进程")
	}
	stale.exit()
	<-stale.waitDone
	time.Sleep(10 * time.Millisecond)
	e.mu.RLock()
	running, state := e.running, e.state
	e.mu.RUnlock()
	if !running || state != StatePlaying {
		t.Fatalf("旧进程退出污染了新进程状态: running=%v state=%s", running, state)
	}
}

func TestExternalSystemBackendIgnoresProcessExit(t *testing.T) {
	e, reg := newTestExternal(t, CapSystem, true)
	_ = e.Start()
	_ = e.SetURI(testURI, Metadata{URI: testURI})
	reg.last().exit()
	<-reg.last().waitDone
	time.Sleep(10 * time.Millisecond)
	if e.State() != StatePlaying {
		t.Fatalf("系统默认程序不应因启动器退出而变为 STOPPED: %s", e.State())
	}
}

func TestExternalAutoPlayOff(t *testing.T) {
	e, reg := newTestExternal(t, CapLaunch, false)
	_ = e.Start()
	_ = e.SetURI(testURI, Metadata{URI: testURI})
	if reg.count() != 0 || e.State() != StatePaused {
		t.Fatalf("autoPlay=false 时不应拉起进程: count=%d state=%s", reg.count(), e.State())
	}
	_ = e.Play("1")
	if reg.count() != 1 || e.State() != StatePlaying {
		t.Fatalf("Play 应拉起进程: count=%d state=%s", reg.count(), e.State())
	}
}

func TestExternalNoBackendReportsError(t *testing.T) {
	e, _ := newTestExternal(t, CapNone, true)
	if err := e.Start(); err == nil {
		t.Fatal("无可用后端时 Start 应返回错误")
	}
	if err := e.SetURI(testURI, Metadata{}); err == nil {
		t.Fatal("无可用后端时 SetURI 应返回错误")
	}
}

func TestExternalRuntimeFullscreenRestarts(t *testing.T) {
	e, reg := newTestExternal(t, CapLaunch, true)
	_ = e.Start()
	_ = e.SetURI(testURI, Metadata{URI: testURI})
	e.ApplyRuntimeOptions(true, true)
	argv := reg.lastArgv()
	found := false
	for _, a := range argv {
		if a == "--fullscreen" {
			found = true
		}
	}
	if !found || reg.count() != 2 {
		t.Fatalf("全屏变更应带参数重启: count=%d argv=%#v", reg.count(), argv)
	}
}

func TestExternalConcurrentActions(t *testing.T) {
	e, reg := newTestExternal(t, CapLaunch, true)
	_ = e.Start()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				switch (n + j) % 5 {
				case 0:
					_ = e.SetURI(fmt.Sprintf("http://x/%d-%d.mp4", n, j), Metadata{})
				case 1:
					_ = e.Pause()
				case 2:
					_ = e.Play("1")
				case 3:
					_ = e.Seek("REL_TIME", "00:00:10")
				default:
					_ = e.Volume()
				}
			}
		}(i)
	}
	wg.Wait()
	reg.exitAll()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil { // 幂等
		t.Fatal(err)
	}
}

// newWMPLikeExternal 模拟 Windows Media Player：只有 URL 之后的全屏开关，
// 没有任何命令行 seek 参数（真实环境中 CapLaunch 后端大多如此）。
func newWMPLikeExternal(t *testing.T) (*External, *procRegistry) {
	t.Helper()
	c := candidate{
		id: "wmplayer", name: "Windows Media Player", capability: CapLaunch,
		fullscreenPostArgs: []string{"/fullscreen"},
	}
	sel := selection{cand: c, path: "C:/Program Files/Windows Media Player/wmplayer.exe"}
	reg := &procRegistry{}
	e := NewExternal(sel, Options{AutoPlay: true, Fullscreen: true},
		logging.New(slog.LevelError, io.Discard))
	e.launchFn = reg.launch
	t.Cleanup(func() {
		reg.exitAll()
		_ = e.Close()
	})
	return e, reg
}

// TestExternalSeekWithoutSeekArgsKeepsFullscreen 复现小红书投屏的实测序列：
// SetAVTransportURI 后约 1s 控制点就发一次 Seek，旧实现会 kill 播放器并
// 用完全相同的参数重启，跳转没生效、全屏窗口却被打断（用户看到「刚全屏又缩回去」）。
func TestExternalSeekWithoutSeekArgsKeepsFullscreen(t *testing.T) {
	e, reg := newWMPLikeExternal(t)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if err := e.SetURI(testURI, Metadata{URI: testURI}); err != nil {
		t.Fatal(err)
	}
	argv := reg.lastArgv()
	if len(argv) < 2 || argv[len(argv)-2] != testURI || argv[len(argv)-1] != "/fullscreen" {
		t.Fatalf("WMP 全屏开关必须在 URL 之后: %#v", argv)
	}
	if reg.count() != 1 {
		t.Fatalf("初始应只拉起一次进程，实际 %d", reg.count())
	}

	for _, target := range []string{"00:00:08", "00:00:28"} {
		if err := e.Seek("REL_TIME", target); err != nil {
			t.Fatal(err)
		}
	}
	if reg.count() != 1 {
		t.Fatalf("无 seekArgs 的后端不应因 Seek 重启进程，实际拉起 %d 次", reg.count())
	}
	if reg.last().killed.Load() {
		t.Fatal("播放器进程被误杀，全屏会被打断")
	}
	pos, _ := e.Position()
	if pos < 28*time.Second || pos > 29*time.Second {
		t.Fatalf("仅同步进度时位置应从最后一次 Seek 继续增长: pos=%v", pos)
	}
	if e.State() != StatePlaying {
		t.Fatalf("Seek 后状态应保持 PLAYING，实际 %s", e.State())
	}
}
