package dlna

import (
	"encoding/xml"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pc-cast/logging"
	"pc-cast/renderer"
)

type fakePlayer struct {
	mu       sync.Mutex
	uri      string
	meta     renderer.Metadata
	state    renderer.State
	volume   int
	mute     bool
	hook     func()
	seekUnit string
	seekTo   string
}

func (f *fakePlayer) Start() error    { return nil }
func (f *fakePlayer) Close() error    { return nil }
func (f *fakePlayer) Path() string    { return "/fake/mpv" }
func (f *fakePlayer) Version() string { return "mpv fake 1.0" }
func (f *fakePlayer) SetURI(uri string, meta renderer.Metadata) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uri, f.meta, f.state = uri, meta, renderer.StatePlaying
	return nil
}
func (f *fakePlayer) Play(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = renderer.StatePlaying
	return nil
}
func (f *fakePlayer) Pause() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = renderer.StatePaused
	return nil
}
func (f *fakePlayer) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = renderer.StateStopped
	return nil
}
func (f *fakePlayer) Seek(unit, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seekUnit, f.seekTo = unit, target
	return nil
}
func (f *fakePlayer) SetVolume(v int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volume = v
	return nil
}
func (f *fakePlayer) Volume() int          { f.mu.Lock(); defer f.mu.Unlock(); return f.volume }
func (f *fakePlayer) SetMute(v bool) error { f.mu.Lock(); defer f.mu.Unlock(); f.mute = v; return nil }
func (f *fakePlayer) Mute() bool           { f.mu.Lock(); defer f.mu.Unlock(); return f.mute }
func (f *fakePlayer) State() renderer.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}
func (f *fakePlayer) Position() (time.Duration, time.Duration) {
	return 30 * time.Second, 120 * time.Second
}
func (f *fakePlayer) CurrentURI() string { f.mu.Lock(); defer f.mu.Unlock(); return f.uri }
func (f *fakePlayer) CurrentMetadata() renderer.Metadata {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.meta
}
func (f *fakePlayer) SetChangeHook(h func())         { f.mu.Lock(); defer f.mu.Unlock(); f.hook = h }
func (f *fakePlayer) ApplyRuntimeOptions(bool, bool) {}

func newTestServer(t *testing.T) (*Server, *fakePlayer) {
	t.Helper()
	p := &fakePlayer{state: renderer.StateNoMedia, volume: 80}
	s := New(Config{FriendlyName: "测试接收端", UUID: "test-uuid"}, p,
		logging.New(slog.LevelError, io.Discard))
	return s, p
}

func soapEnvelope(action, urn, args string) string {
	return `<?xml version="1.0"?><s:Envelope xmlns:s="` + SOAPEnvelopeNS + `"><s:Body>` +
		`<u:` + action + ` xmlns:u="` + urn + `">` + args + `</u:` + action + `>` +
		`</s:Body></s:Envelope>`
}

func soapPost(t *testing.T, url, urn, action, args string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(soapEnvelope(action, urn, args)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("SOAPACTION", `"`+urn+"#"+action+`"`)
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestDescriptionDocument(t *testing.T) {
	s, _ := newTestServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/description.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	for _, want := range []string{"测试接收端", "uuid:test-uuid", DeviceTypeMediaRenderer,
		ServiceAVTransport, ServiceRenderingControl, ServiceConnectionManager,
		"/AVTransport/scpd.xml", "/AVTransport/control", "/AVTransport/event", srv.URL} {
		if !strings.Contains(body, want) {
			t.Errorf("description.xml 缺少 %q", want)
		}
	}
}

func TestSCPDDocuments(t *testing.T) {
	s, _ := newTestServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	for _, svc := range serviceList() {
		resp, err := http.Get(srv.URL + svc.SCPDURL)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", svc.SCPDURL, resp.StatusCode)
		}
		if !strings.Contains(string(b), "<actionList>") || !strings.Contains(string(b), "<serviceStateTable>") {
			t.Errorf("%s 内容不完整", svc.SCPDURL)
		}
	}
}

func TestAVTransportControlFlow(t *testing.T) {
	s, p := newTestServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	url := srv.URL + "/AVTransport/control"

	code, body := soapPost(t, url, ServiceAVTransport, "SetAVTransportURI",
		`<InstanceID>0</InstanceID><CurrentURI>http://192.168.1.9/v.mp4</CurrentURI>`+
			`<CurrentURIMetaData>&lt;DIDL-Lite&gt;&lt;dc:title&gt;示例&lt;/dc:title&gt;&lt;/DIDL-Lite&gt;</CurrentURIMetaData>`)
	if code != http.StatusOK || !strings.Contains(body, "SetAVTransportURIResponse") {
		t.Fatalf("SetAVTransportURI: %d %s", code, body)
	}
	if p.CurrentURI() != "http://192.168.1.9/v.mp4" {
		t.Fatalf("URI = %q", p.CurrentURI())
	}

	code, body = soapPost(t, url, ServiceAVTransport, "GetTransportInfo", `<InstanceID>0</InstanceID>`)
	if code != http.StatusOK || !strings.Contains(body, "<CurrentTransportState>PLAYING</CurrentTransportState>") {
		t.Fatalf("GetTransportInfo: %d %s", code, body)
	}

	code, body = soapPost(t, url, ServiceAVTransport, "GetPositionInfo", `<InstanceID>0</InstanceID>`)
	if code != http.StatusOK || !strings.Contains(body, "<RelTime>00:00:30</RelTime>") ||
		!strings.Contains(body, "<TrackDuration>00:02:00</TrackDuration>") {
		t.Fatalf("GetPositionInfo: %d %s", code, body)
	}

	code, body = soapPost(t, url, ServiceAVTransport, "GetMediaInfo", `<InstanceID>0</InstanceID>`)
	if code != http.StatusOK || !strings.Contains(body, "http://192.168.1.9/v.mp4") {
		t.Fatalf("GetMediaInfo: %d %s", code, body)
	}

	if code, body = soapPost(t, url, ServiceAVTransport, "Pause", `<InstanceID>0</InstanceID>`); code != http.StatusOK {
		t.Fatalf("Pause: %d %s", code, body)
	}
	if p.State() != renderer.StatePaused {
		t.Fatalf("state = %v", p.State())
	}

	if code, body = soapPost(t, url, ServiceAVTransport, "Seek",
		`<InstanceID>0</InstanceID><Unit>REL_TIME</Unit><Target>00:00:45</Target>`); code != http.StatusOK {
		t.Fatalf("Seek: %d %s", code, body)
	}
	p.mu.Lock()
	unit, to := p.seekUnit, p.seekTo
	p.mu.Unlock()
	if unit != "REL_TIME" || to != "00:00:45" {
		t.Fatalf("seek = %s/%s", unit, to)
	}
}

func TestRenderingControlAndConnectionManager(t *testing.T) {
	s, p := newTestServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()

	code, body := soapPost(t, srv.URL+"/RenderingControl/control", ServiceRenderingControl,
		"SetVolume", `<InstanceID>0</InstanceID><Channel>Master</Channel><DesiredVolume>55</DesiredVolume>`)
	if code != http.StatusOK || p.Volume() != 55 {
		t.Fatalf("SetVolume: %d %s, volume=%d", code, body, p.Volume())
	}
	code, body = soapPost(t, srv.URL+"/RenderingControl/control", ServiceRenderingControl,
		"GetMute", `<InstanceID>0</InstanceID><Channel>Master</Channel>`)
	if code != http.StatusOK || !strings.Contains(body, "<CurrentMute>0</CurrentMute>") {
		t.Fatalf("GetMute: %d %s", code, body)
	}
	code, body = soapPost(t, srv.URL+"/ConnectionManager/control", ServiceConnectionManager,
		"GetProtocolInfo", ``)
	if code != http.StatusOK || !strings.Contains(body, "http-get:*:video/mp4:") {
		t.Fatalf("GetProtocolInfo: %d %s", code, body)
	}
}

func TestUnknownActionReturnsFault(t *testing.T) {
	s, _ := newTestServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	code, body := soapPost(t, srv.URL+"/AVTransport/control", ServiceAVTransport, "NoSuchAction", "")
	if code != http.StatusInternalServerError || !strings.Contains(body, "<errorCode>401</errorCode>") {
		t.Fatalf("fault: %d %s", code, body)
	}
}

func TestSubscribeSendsInitialEvent(t *testing.T) {
	got := make(chan string, 1)
	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "NOTIFY" {
			select {
			case got <- r.Header.Get("SID"):
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer cb.Close()

	s, _ := newTestServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	req, _ := http.NewRequest("SUBSCRIBE", srv.URL+"/AVTransport/event", nil)
	req.Header.Set("CALLBACK", "<"+cb.URL+">")
	req.Header.Set("NT", "upnp:event")
	req.Header.Set("TIMEOUT", "Second-1800")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("SID") == "" {
		t.Fatalf("SUBSCRIBE: %d SID=%q", resp.StatusCode, resp.Header.Get("SID"))
	}
	select {
	case sid := <-got:
		if sid != resp.Header.Get("SID") {
			t.Fatalf("NOTIFY SID = %q", sid)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未收到初始 GENA 事件")
	}
}

func TestLastChangeIsEscaped(t *testing.T) {
	s, _ := newTestServer(t)
	body := s.eventBody(ServiceAVTransport, true)
	if !strings.Contains(body, "&lt;Event") || !strings.Contains(body, "LastChange") {
		t.Fatalf("LastChange 未正确转义: %s", body)
	}
}

// 部分 SDK 会把 base 与以 / 开头的 URL 直接拼接，产生双斜杠路径；
// 自定义 Handler 必须容忍，否则控制请求会落到 404。
func TestDoubleSlashControlPathIsTolerated(t *testing.T) {
	s, _ := newTestServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	code, body := soapPost(t, srv.URL+"//AVTransport/control", ServiceAVTransport,
		"GetTransportInfo", `<InstanceID>0</InstanceID>`)
	if code != http.StatusOK || !strings.Contains(body, "GetTransportInfoResponse") {
		t.Fatalf("//AVTransport/control: %d %s", code, body)
	}
}

// 央视频等投屏 SDK 连的是 8768，却把 Host 头写成不带端口的 "192.168.1.2"。
// 旧实现直接用该 Host 拼 URLBase，控制点会把 SOAP / GENA 请求发到 80 端口，
// 服务端收不到任何控制请求。这里锁死「URLBase 端口 == 实际监听端口」。
func TestDescriptionBaseAlwaysMatchesLocation(t *testing.T) {
	s, _ := newTestServer(t)
	s.mu.Lock()
	s.port = 8768
	s.mu.Unlock()

	cases := []struct {
		host string
		want string
	}{
		{"192.168.1.2", "http://192.168.1.2:8768"},
		{"192.168.1.2:8768", "http://192.168.1.2:8768"},
		{"192.168.1.2:80", "http://192.168.1.2:8768"},
		{"[fe80::1]", "http://[fe80::1]:8768"},
		{"", s.BaseURL()},
	}
	for _, c := range cases {
		if got := s.descriptionBase(c.host); got != c.want {
			t.Errorf("host=%q descriptionBase=%q, want %q", c.host, got, c.want)
		}
		body := s.descriptionFor(c.host)
		for _, want := range []string{
			"<URLBase>" + c.want + "/</URLBase>",
			"<SCPDURL>" + c.want + "/AVTransport/scpd.xml</SCPDURL>",
			"<controlURL>" + c.want + "/AVTransport/control</controlURL>",
			"<eventSubURL>" + c.want + "/AVTransport/event</eventSubURL>",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("host=%q 设备描述缺少 %q", c.host, want)
			}
		}
	}
	if got, want := s.descriptionBase("192.168.1.2"), s.baseURLFor("192.168.1.2"); got != want {
		t.Errorf("URLBase %q 与 SSDP LOCATION origin %q 不一致", got, want)
	}
}

// 端到端复现：真实 HTTP 请求携带不带端口的 Host 头时，返回的
// description.xml 仍必须把 URLBase 与三个服务 URL 指向监听端口。
func TestDescriptionDocumentWithPortlessHost(t *testing.T) {
	s, _ := newTestServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	s.mu.Lock()
	s.port = port
	s.mu.Unlock()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/description.xml", nil)
	req.Host = "127.0.0.1" // 模拟控制点手写的不带端口 Host 头
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	base := "http://127.0.0.1:" + portStr + "/"
	for _, want := range []string{
		"<URLBase>" + base + "</URLBase>",
		"<SCPDURL>" + base + "AVTransport/scpd.xml</SCPDURL>",
		"<controlURL>" + base + "AVTransport/control</controlURL>",
		"<eventSubURL>" + base + "AVTransport/event</eventSubURL>",
		"<SCPDURL>" + base + "ConnectionManager/scpd.xml</SCPDURL>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("设备描述缺少 %q", want)
		}
	}
	var doc struct{ XMLName xml.Name }
	if err := xml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("description.xml 不是合法 XML: %v", err)
	}
}
