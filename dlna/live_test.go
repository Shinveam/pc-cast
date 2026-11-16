package dlna

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pc-cast/logging"
	"pc-cast/renderer"
)

func TestSinkProtocolInfoSupportsHDLive(t *testing.T) {
	if strings.Contains(ProtocolInfoSink, "AVC_MP4_MP_SD") {
		t.Fatal("Sink 声明了标清档 DLNA PN，控制点会据此推送低码率流")
	}
	for _, want := range []string{
		"AVC_MP4_MP_HD_720p_AAC",
		"AVC_MP4_MP_HD_1080i_AAC",
		"application/vnd.apple.mpegurl",
		"application/x-mpegURL",
		"video/mp2t",
		"application/octet-stream",
		"http-get:*:video/m3u8:*",
		"http-get:*:video/flv:*",
		"http-get:*:video/f4v:*",
		"http-get:*:video/vnd.dlna.mpeg-tts:*",
		"http-get:*:*:*",
	} {
		if !strings.Contains(ProtocolInfoSink, want) {
			t.Errorf("Sink 缺少 %q", want)
		}
	}
}

func TestParseDIDLMultipleResPrefersBest(t *testing.T) {
	raw := `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<item id="1" parentID="0" restricted="1"><dc:title>多清晰度</dc:title>` +
		`<res protocolInfo="http-get:*:video/mp4:SD" bitrate="300000" resolution="640x360" duration="00:01:30.000">http://host/low.mp4</res>` +
		`<res protocolInfo="http-get:*:video/mp4:HD" bitrate="3000000" resolution="1920x1080" duration="00:02:00.000">http://host/high.mp4</res>` +
		`</item></DIDL-Lite>`
	m := ParseDIDL(raw, "http://host/current.mp4")
	if m.ProtocolInfo != "http-get:*:video/mp4:HD" {
		t.Errorf("未选中高清晰度 res 的 protocolInfo: %q", m.ProtocolInfo)
	}
	if m.Duration != 2*time.Minute {
		t.Errorf("Duration = %v", m.Duration)
	}
	if m.URI != "http://host/high.mp4" {
		t.Errorf("多 res 时应选中最高分的地址，实际 %q", m.URI)
	}
}

func TestMakeDIDLLiveBroadcast(t *testing.T) {
	raw := `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" ` +
		`xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">` +
		`<item id="1" parentID="-1" restricted="1">` +
		`<upnp:class>object.item.videoItem.videoBroadcast</upnp:class>` +
		`<res protocolInfo="http-get:*:application/vnd.apple.mpegurl:*">http://host/live.m3u8</res>` +
		`</item></DIDL-Lite>`
	m := ParseDIDL(raw, "")
	if !m.Live {
		t.Fatal("videoBroadcast / HLS 应识别为直播")
	}
	generated := MakeDIDL(renderer.Metadata{URI: "http://host/live.m3u8", Title: "直播频道", Live: true})
	if !strings.Contains(generated, "videoBroadcast") ||
		!strings.Contains(generated, "application/vnd.apple.mpegurl") {
		t.Fatalf("生成的直播 DIDL 不完整: %s", generated)
	}
}

// TestLiveControlFlow 覆盖直播播放链路。
//
// 直播与点播走完全相同的播放路径：接收端不拦截、不弹提示。控制流程按
// 实测结论处理 —— 进度/时长回 00:00:00、Seek 直接转发、Actions 保留 Seek。
// 实测央视频在直播起播后连发两次 Seek，旧实现回 710 SOAP 错误会让控制点
// 判定渲染器异常并主动 Stop，表现为「只播几秒」。
func TestLiveControlFlow(t *testing.T) {
	s, p := newTestServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	url := srv.URL + "/AVTransport/control"
	liveURI := "http://host/live/channel.m3u8?token=1"

	code, body := soapPost(t, url, ServiceAVTransport, "SetAVTransportURI",
		`<InstanceID>0</InstanceID><CurrentURI>`+liveURI+`</CurrentURI><CurrentURIMetaData></CurrentURIMetaData>`)
	if code != http.StatusOK || !strings.Contains(body, "SetAVTransportURIResponse") {
		t.Fatalf("直播投屏被拒绝: %d %s", code, body)
	}
	if !p.CurrentMetadata().Live {
		t.Fatal("直播标记未传递到播放器")
	}
	if p.CurrentURI() != liveURI {
		t.Fatalf("直播 URI 未被原样传递: %q", p.CurrentURI())
	}

	code, body = soapPost(t, url, ServiceAVTransport, "GetPositionInfo", `<InstanceID>0</InstanceID>`)
	if code != http.StatusOK || !strings.Contains(body, "<RelTime>00:00:00</RelTime>") ||
		!strings.Contains(body, "<TrackDuration>00:00:00</TrackDuration>") {
		t.Fatalf("直播 GetPositionInfo 应回 00:00:00: %d %s", code, body)
	}

	code, body = soapPost(t, url, ServiceAVTransport, "GetMediaInfo", `<InstanceID>0</InstanceID>`)
	if code != http.StatusOK || !strings.Contains(body, "<MediaDuration>00:00:00</MediaDuration>") {
		t.Fatalf("直播 GetMediaInfo 应回 00:00:00: %d %s", code, body)
	}

	code, body = soapPost(t, url, ServiceAVTransport, "GetCurrentTransportActions", `<InstanceID>0</InstanceID>`)
	if code != http.StatusOK || !strings.Contains(body, "Seek") {
		t.Fatalf("直播应保留 Seek 动作: %d %s", code, body)
	}

	code, body = soapPost(t, url, ServiceAVTransport, "Seek",
		`<InstanceID>0</InstanceID><Unit>REL_TIME</Unit><Target>00:00:10</Target>`)
	if code != http.StatusOK || !strings.Contains(body, "SeekResponse") {
		t.Fatalf("直播 Seek 不应返回 SOAP 错误: %d %s", code, body)
	}

	// eventBody 会把 LastChange 整体转义，这里直接断言生成源串。
	lastChange := s.avTransportLastChange()
	for _, want := range []string{
		`<CurrentTrackDuration val="00:00:00"/>`,
		`<RelativeTimePosition val="00:00:00"/>`,
	} {
		if !strings.Contains(lastChange, want) {
			t.Errorf("直播 LastChange 缺少 %q: %s", want, lastChange)
		}
	}
	if !strings.Contains(s.eventBody(ServiceAVTransport, true), "LastChange") {
		t.Fatal("GENA 事件缺少 LastChange")
	}
}

// TestLiveClassificationMatrix 确认国内直播 CDN 的链接都能被识别，且点播
// 长签名链接不误判；同时验证所有链接都原样交给播放器、没有任何拦截。
func TestLiveClassificationMatrix(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		live bool
	}{
		{"央视频 HLS 直播", "http://live-dtocnc-cdn.ysp.cctv.cn/ch/2029797202.m3u8?from=player", true},
		{"央视频 TS 回放", "http://hlsplaycnc-cdn.ysp.cctv.cn/x/abc.ts.m3u8?ver=4", true},
		{"HLS", "http://cdn/live-bvc/123/live_1_2.m3u8?token=1", true},
		{"HTTP-FLV", "http://cdn/live-bvc/123/live_1_2.flv?token=1", true},
		{"MPEG-TS", "http://cdn/hls/seg.ts", true},
		{"DASH", "http://cdn/stream/index.mpd", true},
		{"m3u8 藏在参数里", "http://cdn/play?url=http%3A%2F%2Fcdn%2Flive.m3u8", true},
		{"点播 mp4", "http://host/movie.mp4", false},
		{"B 站点播签名链接", "http://upos-sz-estgcos.bilivideo.com/upgcxcode/43290659048-1-192.mp4?bw=317822", false},
		{"小红书点播", "http://sns-video-v3.xhscdn.com/xxx_258.mp4", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakePlayer{state: renderer.StateNoMedia, volume: 80}
			s := New(Config{FriendlyName: "测试接收端", UUID: "test-uuid"}, p,
				logging.New(slog.LevelError, io.Discard))
			srv := httptest.NewServer(s)
			defer srv.Close()
			code, body := soapPost(t, srv.URL+"/AVTransport/control", ServiceAVTransport,
				"SetAVTransportURI",
				`<InstanceID>0</InstanceID><CurrentURI>`+tc.uri+`</CurrentURI><CurrentURIMetaData></CurrentURIMetaData>`)
			if code != http.StatusOK {
				t.Fatalf("投屏被拒绝: %d %s", code, body)
			}
			if got := p.CurrentMetadata().Live; got != tc.live {
				t.Errorf("live = %v, want %v", got, tc.live)
			}
			if got := p.CurrentURI(); got != tc.uri {
				t.Errorf("URI 被改写: %q", got)
			}
		})
	}
}

// TestLiveDoesNotNotifyOrWarn 防止「直播暂不支持」的通知/弹窗链路回归。
// dlna.Config 已不再包含 Notice / Alert / LiveSupported 字段。
func TestLiveDoesNotNotifyOrWarn(t *testing.T) {
	var buf bytes.Buffer
	p := &fakePlayer{state: renderer.StateNoMedia, volume: 80}
	s := New(Config{FriendlyName: "测试接收端", UUID: "test-uuid"}, p,
		logging.New(slog.LevelInfo, &buf))
	srv := httptest.NewServer(s)
	defer srv.Close()
	url := srv.URL + "/AVTransport/control"

	for _, action := range []struct{ name, args string }{
		{"SetAVTransportURI", `<InstanceID>0</InstanceID><CurrentURI>http://cdn/live/a.m3u8</CurrentURI><CurrentURIMetaData></CurrentURIMetaData>`},
		{"SetNextAVTransportURI", `<InstanceID>0</InstanceID><NextURI>http://cdn/live/room.flv</NextURI><NextURIMetaData></NextURIMetaData>`},
	} {
		code, body := soapPost(t, url, ServiceAVTransport, action.name, action.args)
		if code != http.StatusOK {
			t.Fatalf("%s 被拒绝: %d %s", action.name, code, body)
		}
	}
	logText := buf.String()
	for _, forbidden := range []string{"直播内容暂不支持", "无法播放直播流", "暂不支持"} {
		if strings.Contains(logText, forbidden) {
			t.Errorf("直播流程不应再产生 %q 警告: %s", forbidden, logText)
		}
	}
	if strings.Contains(logText, `"level":"WARN"`) {
		t.Errorf("直播流程不应产生 WARN 日志: %s", logText)
	}
}
