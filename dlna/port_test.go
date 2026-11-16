package dlna

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"pc-cast/logging"
)

func newPortTestServer() *Server {
	return New(Config{}, &fakePlayer{}, logging.New(slog.LevelError, io.Discard))
}

// 手机投屏 APP 会缓存 SSDP LOCATION 里的 IP:PORT。重启接收端后端口若变化，
// 列表里仍显示设备名，但点击时连的是旧端口，连 HTTP 请求都不会到。
func TestOpenListenerPortIsStableAcrossRestart(t *testing.T) {
	s := newPortTestServer()
	ln1, addr1, mode1, err := s.openListener("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if mode1 == "random" {
		_ = ln1.Close()
		t.Skip("固定端口区间全部被占用，已按预期回退随机端口")
	}
	p1 := ln1.Addr().(*net.TCPAddr).Port
	if p1 < PreferredPort || p1 >= PreferredPort+PortProbeCount {
		t.Fatalf("端口 %d 不在固定区间 [%d, %d)", p1, PreferredPort, PreferredPort+PortProbeCount)
	}
	_ = ln1.Close()

	ln2, addr2, _, err := s.openListener("", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	if addr2 != addr1 {
		t.Fatalf("重启后服务端口发生变化：%s -> %s，手机缓存的 LOCATION 会失效", addr1, addr2)
	}
}

// 用户显式指定端口时必须严格使用，占用则报错，绝不悄悄换端口。
func TestOpenListenerExplicitPortIsStrict(t *testing.T) {
	s := newPortTestServer()
	ln, _, _, err := s.openListener("", 0)
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	defer ln.Close()
	if _, _, _, err := s.openListener("", p); err == nil {
		t.Fatalf("显式端口 %d 已被占用，应当直接报错", p)
	}
}

func TestIsVersionAlias(t *testing.T) {
	targets := []target{
		{NT: "upnp:rootdevice"},
		{NT: DeviceTypeMediaRenderer},
		{NT: ServiceAVTransport},
		{NT: ServiceRenderingControl},
		{NT: ServiceConnectionManager},
	}
	cases := []struct {
		st   string
		want bool
	}{
		{"urn:schemas-upnp-org:device:MediaRenderer:2", true},
		{"urn:schemas-upnp-org:service:AVTransport:2", true},
		{"urn:schemas-upnp-org:service:RenderingControl:3", true},
		{DeviceTypeMediaRenderer, false}, // 精确命中，不需要别名
		{"urn:schemas-upnp-org:device:MediaServer:2", false},
		{"ssdp:all", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isVersionAlias(tc.st, targets); got != tc.want {
			t.Errorf("isVersionAlias(%q) = %v, want %v", tc.st, got, tc.want)
		}
	}
}

// alive 广播必须比 max-age 更频繁，否则控制点会在缓存有效期内认为设备离线。
func TestAnnounceIntervalWithinMaxAge(t *testing.T) {
	maxAge := time.Duration(SSDPMaxAge) * time.Second
	if announceEvery >= maxAge {
		t.Fatalf("alive 间隔 %s 不应长于 max-age %s", announceEvery, maxAge)
	}
	if announceEvery*2 > maxAge {
		t.Fatalf("alive 间隔 %s 过长：max-age %s 内不足两次广播", announceEvery, maxAge)
	}
}
