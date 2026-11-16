package renderer

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pc-cast/logging"
)

const testVLCPassword = "test-token"

// fakeVLC 模拟 VLC Lua HTTP 接口：每个 /requests/status.json 请求既执行命令，
// 又返回当前状态快照，与真实 VLC 行为一致。
type fakeVLC struct {
	mu       sync.Mutex
	state    string
	time     float64
	length   float64
	volume   float64
	queries  []string
	authFail bool
}

func newFakeVLC() *fakeVLC {
	return &fakeVLC{state: "stopped", length: 120, volume: 256}
}

func (f *fakeVLC) set(mutate func(*fakeVLC)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mutate(f)
}

func (f *fakeVLC) handler(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != "" || pass != testVLCPassword {
		f.mu.Lock()
		f.authFail = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, r.URL.RawQuery)
	switch cmd := q.Get("command"); cmd {
	case "pl_empty":
		f.state, f.time = "stopped", 0
	case "in_play":
		f.state, f.time = "playing", 0
	case "pl_forceresume", "pl_play":
		f.state = "playing"
	case "pl_forcepause", "pl_pause":
		f.state = "paused"
	case "pl_stop":
		f.state, f.time = "stopped", 0
	case "seek":
		if n, err := strconv.ParseFloat(q.Get("val"), 64); err == nil {
			f.time = n
		}
	case "volume":
		if n, err := strconv.ParseFloat(q.Get("val"), 64); err == nil {
			f.volume = n
		}
	}
	_ = json.NewEncoder(w).Encode(vlcStatus{
		State: f.state, Time: f.time, Length: f.length, Volume: f.volume,
	})
}

func (f *fakeVLC) hasCommand(t *testing.T, cmd string, wantVal ...string) {
	t.Helper()
	f.mu.Lock()
	queries := append([]string{}, f.queries...)
	f.mu.Unlock()
	for _, q := range queries {
		values, err := url.ParseQuery(q)
		if err != nil || values.Get("command") != cmd {
			continue
		}
		if len(wantVal) == 0 {
			return
		}
		if values.Get(wantVal[0]) == wantVal[1] {
			return
		}
	}
	t.Fatalf("未找到命令 %s %v，实际请求: %v", cmd, wantVal, queries)
}

// neverEndingProc 让 Start 的 wait goroutine 在 Kill 之前一直阻塞。
type neverEndingProc struct {
	once sync.Once
	done chan struct{}
}

func (p *neverEndingProc) Kill() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *neverEndingProc) Wait() error {
	<-p.done
	return nil
}

func newTestVLC(t *testing.T, srv *httptest.Server, opts Options) *VLC {
	t.Helper()
	sel := selection{
		cand: candidate{id: "vlc", name: "VLC", capability: CapFull},
		path: "/fake/vlc",
	}
	v := NewVLC(sel, opts, logging.New(slog.LevelError, io.Discard))
	v.spawn = func(*VLC) (childProcess, string, string, error) {
		return &neverEndingProc{done: make(chan struct{})}, srv.URL, testVLCPassword, nil
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待状态更新超时")
}

func TestVLCStatusAndCommands(t *testing.T) {
	f := newFakeVLC()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	v := newTestVLC(t, srv, Options{AutoPlay: true, WindowWidth: 1280, WindowHeight: 720})
	if err := v.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return v.State() == StateNoMedia }, 2*time.Second)

	if err := v.SetURI("http://host/movie.mp4", Metadata{Title: "测试影片"}); err != nil {
		t.Fatal(err)
	}
	f.hasCommand(t, "pl_empty")
	f.hasCommand(t, "in_play", "input", "http://host/movie.mp4")
	f.hasCommand(t, "pl_forceresume")
	if v.CurrentURI() != "http://host/movie.mp4" {
		t.Fatalf("URI = %q", v.CurrentURI())
	}
	waitFor(t, func() bool { return v.State() == StatePlaying }, 2*time.Second)

	f.set(func(s *fakeVLC) { s.time, s.length = 45, 120 })
	waitFor(t, func() bool {
		pos, dur := v.Position()
		return pos == 45*time.Second && dur == 120*time.Second
	}, 2*time.Second)

	if err := v.Pause(); err != nil {
		t.Fatal(err)
	}
	f.hasCommand(t, "pl_forcepause")
	if v.State() != StatePaused {
		t.Fatalf("Pause 后状态 = %v", v.State())
	}
	if err := v.Play(""); err != nil {
		t.Fatal(err)
	}
	f.hasCommand(t, "pl_forceresume")

	if err := v.Seek("REL_TIME", "00:00:30"); err != nil {
		t.Fatal(err)
	}
	f.hasCommand(t, "seek", "val", "30")

	if err := v.SetVolume(50); err != nil {
		t.Fatal(err)
	}
	f.hasCommand(t, "volume", "val", "128")
	waitFor(t, func() bool { return v.Volume() == 50 }, 2*time.Second)

	if err := v.SetMute(true); err != nil {
		t.Fatal(err)
	}
	f.hasCommand(t, "volume", "val", "0")
	if !v.Mute() {
		t.Fatal("静音状态未记录")
	}
	if err := v.SetMute(false); err != nil {
		t.Fatal(err)
	}

	if err := v.Stop(); err != nil {
		t.Fatal(err)
	}
	f.hasCommand(t, "pl_stop")
	if v.State() != StateStopped {
		t.Fatalf("Stop 后状态 = %v", v.State())
	}

	f.mu.Lock()
	authFail := f.authFail
	f.mu.Unlock()
	if authFail {
		t.Fatal("VLC HTTP 鉴权失败：Basic Auth 用户名必须留空")
	}
}

func TestVLCLiveStreamHasNoPosition(t *testing.T) {
	f := newFakeVLC()
	f.length = 0 // 直播流没有时长
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	v := newTestVLC(t, srv, Options{AutoPlay: true})
	if err := v.Start(); err != nil {
		t.Fatal(err)
	}
	if err := v.SetURI("http://host/live/channel.m3u8?token=abc", Metadata{Live: true}); err != nil {
		t.Fatal(err)
	}
	f.set(func(s *fakeVLC) { s.time, s.length = 37, 0 })
	waitFor(t, func() bool { return v.State() == StatePlaying }, 2*time.Second)
	if pos, dur := v.Position(); pos != 0 || dur != 0 {
		t.Fatalf("直播不应上报进度: pos=%v dur=%v", pos, dur)
	}
	// 直播 Seek 必须静默成功（no-op）：返回错误会让 dlna 层回 SOAP fault，
	// 控制点据此主动断开投屏，表现为「只播几秒」。
	if err := v.Seek("REL_TIME", "00:00:10"); err != nil {
		t.Fatalf("直播 Seek 应静默成功: %v", err)
	}
}

func TestVLCStartFailureReturnsError(t *testing.T) {
	sel := selection{cand: candidate{id: "vlc", name: "VLC", capability: CapFull}, path: "/fake/vlc"}
	v := NewVLC(sel, Options{}, logging.New(slog.LevelError, io.Discard))
	v.spawn = func(*VLC) (childProcess, string, string, error) {
		return nil, "", "", errors.New("VLC HTTP 接口启动超时")
	}
	if err := v.Start(); err == nil {
		t.Fatal("HTTP 握手失败时 Start 应返回错误，让 factory 降级")
	}
	_ = v.Close()
}

func TestVLCConcurrentActions(t *testing.T) {
	f := newFakeVLC()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	v := newTestVLC(t, srv, Options{AutoPlay: true})
	if err := v.Start(); err != nil {
		t.Fatal(err)
	}
	if err := v.SetURI("http://host/movie.mp4", Metadata{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				_ = v.SetVolume(j)
				_ = v.SetMute(j%2 == 0)
				_ = v.Volume()
				_ = v.Mute()
				_ = v.State()
				_, _ = v.Position()
				_ = v.CurrentURI()
				_ = v.CurrentMetadata()
				v.SetChangeHook(func() {})
				v.ApplyWindowSize(1280, 720)
			}
		}(i)
	}
	wg.Wait()
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	authFail := f.authFail
	f.mu.Unlock()
	if authFail {
		t.Fatal("并发请求出现鉴权失败")
	}
}

func TestVLCWindowSizeRecorded(t *testing.T) {
	f := newFakeVLC()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	v := newTestVLC(t, srv, Options{WindowWidth: 1280, WindowHeight: 720})
	if err := v.Start(); err != nil {
		t.Fatal(err)
	}
	v.ApplyWindowSize(1920, 1080)
	v.mu.RLock()
	w, h := v.width, v.height
	v.mu.RUnlock()
	if w != 1920 || h != 1080 {
		t.Fatalf("窗口尺寸未更新: %dx%d", w, h)
	}
	if d := v.Descriptor(); d.Capability != CapFull || d.ID != "vlc" {
		t.Fatalf("VLC 描述信息异常: %#v", d)
	}
}

func TestIsLive(t *testing.T) {
	cases := []struct {
		uri  string
		meta Metadata
		want bool
	}{
		{"http://host/live.m3u8?token=1", Metadata{}, true},
		{"http://host/channel/live.m3u", Metadata{}, true},
		{"http://host/a.mp4", Metadata{ProtocolInfo: "http-get:*:application/vnd.apple.mpegurl:*"}, true},
		{"http://host/a.ts", Metadata{ProtocolInfo: "http-get:*:video/mp2t:*"}, true},
		{"http://host/a.mp4", Metadata{Live: true}, true},
		{"http://host/a.mp4?bw=317822", Metadata{}, false},
		{"http://host/a.mp4", Metadata{Duration: 120 * time.Second}, false},
		// 国内直播 CDN：HTTP-FLV、TS、DASH、m3u8 藏在 query、live-bvc 路径。
		{"https://d1--cn-gotcha104.bilivideo.com/live-bvc/123/live_123_456.flv?token=1", Metadata{}, true},
		{"http://host/live-bvc/123/live_1_2.m3u8?bvc=live", Metadata{}, true},
		{"http://host/hls/segment.ts", Metadata{}, true},
		{"http://host/stream/index.mpd?token=1", Metadata{}, true},
		{"http://host/play?url=http%3A%2F%2Fcdn%2Flive.m3u8", Metadata{}, true},
		{"http://host/a.mp4", Metadata{ProtocolInfo: "http-get:*:video/flv:*"}, true},
		{"rtmp://host/live/room", Metadata{}, true},
		// 点播反证：明确文件后缀、VOD 参数、已知时长，不能被 live 字样误伤。
		{"http://upos-sz-estgcos.bilivideo.com/upgcxcode/48/90/43290659048/43290659048-1-192.mp4?e=ig8euxZM2rNcNbRH&bvc=vod&deadline=1794736220&dl=0", Metadata{}, false},
		{"http://host/movie.mp4?live=1", Metadata{}, false},
		{"http://host/live/cover.mkv", Metadata{Duration: 90 * time.Second}, false},
	}
	for _, tc := range cases {
		if got := IsLive(tc.uri, tc.meta); got != tc.want {
			t.Errorf("IsLive(%q) = %v, want %v", tc.uri, got, tc.want)
		}
	}
}

func TestBackendOf(t *testing.T) {
	cases := []struct {
		cand candidate
		path string
		want string
	}{
		{candidate{id: "mpv"}, "/usr/bin/mpv", "mpv"},
		{candidate{id: "vlc"}, `C:\Program Files\VideoLAN\VLC\vlc.exe`, "vlc"},
		{candidate{id: "custom"}, `C:\portable\vlc-portable.exe`, "vlc"},
		{candidate{id: "custom"}, `C:\portable\mpv.exe`, "mpv"},
		{candidate{id: "wmplayer"}, `C:\Program Files\Windows Media Player\wmplayer.exe`, "external"},
		{candidate{id: "system"}, `C:\Windows\System32\rundll32.exe`, "external"},
	}
	for _, tc := range cases {
		if got := backendOf(tc.cand, tc.path); got != tc.want {
			t.Errorf("backendOf(%s, %s) = %s, want %s", tc.cand.id, tc.path, got, tc.want)
		}
	}
}

func TestBuildArgvWithWindowSize(t *testing.T) {
	c := candidate{id: "ffplay", geometryArgs: ffplayGeometry}
	argv := buildArgvWithSize(c, "ffplay", "http://host/a.mp4", 0, false, 1280, 720)
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-x 1280", "-y 720"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少窗口参数 %q: %v", want, argv)
		}
	}
	if argv[len(argv)-1] != "http://host/a.mp4" {
		t.Fatalf("URL 必须是最后一个参数: %v", argv)
	}
	full := buildArgvWithSize(c, "ffplay", "http://host/a.mp4", 0, true, 1280, 720)
	if strings.Contains(strings.Join(full, " "), "-x 1280") {
		t.Fatalf("全屏时不应再传窗口尺寸: %v", full)
	}
}
