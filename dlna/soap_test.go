package dlna

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseSOAPBody(t *testing.T) {
	body := `<?xml version="1.0"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` +
		`<u:SetAVTransportURI xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">` +
		`<InstanceID>0</InstanceID><CurrentURI>http://192.168.1.5/movie.mp4</CurrentURI>` +
		`<CurrentURIMetaData>&lt;DIDL-Lite&gt;meta&lt;/DIDL-Lite&gt;</CurrentURIMetaData>` +
		`</u:SetAVTransportURI></s:Body></s:Envelope>`
	name, args, err := parseSOAPBody([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if name != "SetAVTransportURI" {
		t.Errorf("action = %q", name)
	}
	if args["InstanceID"] != "0" || args["CurrentURI"] != "http://192.168.1.5/movie.mp4" {
		t.Errorf("args = %#v", args)
	}
	if !strings.Contains(args["CurrentURIMetaData"], "<DIDL-Lite>") {
		t.Errorf("转义的参数未还原: %q", args["CurrentURIMetaData"])
	}
}

func TestParseSOAPBodyRejectsGarbage(t *testing.T) {
	if _, _, err := parseSOAPBody([]byte("not xml")); err == nil {
		t.Fatal("非法 XML 应返回错误")
	}
}

func TestWriteActionResponseEscapesValues(t *testing.T) {
	w := httptest.NewRecorder()
	writeActionResponse(w, ServiceAVTransport, "GetMediaInfo", [][2]string{
		{"CurrentURI", "http://host/a.mp4?x=1&y=2"},
		{"CurrentURIMetaData", "<DIDL-Lite/>"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "x=1&amp;y=2") || !strings.Contains(body, "&lt;DIDL-Lite/&gt;") {
		t.Errorf("响应未正确转义: %s", body)
	}
	if !strings.Contains(body, "GetMediaInfoResponse") {
		t.Errorf("缺少 action 响应节点: %s", body)
	}
}

func TestWriteSOAPFault(t *testing.T) {
	w := httptest.NewRecorder()
	writeSOAPFault(w, 401, "Invalid Action & More")
	body := w.Body.String()
	if w.Code != http.StatusInternalServerError || !strings.Contains(body, "<errorCode>401</errorCode>") {
		t.Fatalf("fault 不正确: %d %s", w.Code, body)
	}
	if !strings.Contains(body, "Invalid Action &amp; More") {
		t.Errorf("fault 描述未转义: %s", body)
	}
}
