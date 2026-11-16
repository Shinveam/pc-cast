package dlna

import (
	"strings"
	"testing"
	"time"

	"pc-cast/renderer"
)

func TestParseDIDL(t *testing.T) {
	raw := `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<item id="1" parentID="0" restricted="1"><dc:title>测试影片</dc:title>` +
		`<res protocolInfo="http-get:*:video/mp4:*" duration="00:01:30.000">http://192.168.1.2/a.mp4</res>` +
		`</item></DIDL-Lite>`
	m := ParseDIDL(raw, "http://fallback/x.mp4")
	if m.URI != "http://192.168.1.2/a.mp4" {
		t.Errorf("URI = %q", m.URI)
	}
	if m.Title != "测试影片" {
		t.Errorf("Title = %q", m.Title)
	}
	if m.ProtocolInfo != "http-get:*:video/mp4:*" {
		t.Errorf("ProtocolInfo = %q", m.ProtocolInfo)
	}
	if m.Duration != 90*time.Second {
		t.Errorf("Duration = %v", m.Duration)
	}
	if m.RawXML != raw {
		t.Error("RawXML 未保留")
	}
}

func TestParseDIDLEmptyFallsBack(t *testing.T) {
	m := ParseDIDL("", "http://fallback/x.mp4")
	if m.URI != "http://fallback/x.mp4" {
		t.Fatalf("URI = %q", m.URI)
	}
	if m.RawXML != "" {
		t.Error("RawXML 应为空")
	}
}

func TestMakeDIDL(t *testing.T) {
	m := MakeDIDL(ParseDIDL("", "http://host/movie%20one.mp4"))
	if !strings.Contains(m, `<dc:title>movie%20one.mp4</dc:title>`) {
		t.Errorf("生成的 DIDL 缺少标题: %s", m)
	}
	if !strings.Contains(m, "http://host/movie%20one.mp4") {
		t.Errorf("生成的 DIDL 缺少 URI: %s", m)
	}
	// 特殊字符必须被转义
	s := MakeDIDL(renderer.Metadata{Title: "A & B <C>", URI: "http://host/a.mp4"})
	if strings.Contains(s, "<C>") || !strings.Contains(s, "&amp;") {
		t.Errorf("未正确转义: %s", s)
	}
}

func TestParseClockDuration(t *testing.T) {
	d, err := parseClockDuration("01:02:03.500")
	if err != nil || d != time.Hour+2*time.Minute+3500*time.Millisecond {
		t.Fatalf("parseClockDuration = %v, %v", d, err)
	}
	if _, err := parseClockDuration("bad"); err == nil {
		t.Fatal("非法 duration 应报错")
	}
}
