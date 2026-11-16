package dlna

import (
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestParseCallbackCompatibility(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"标准尖括号", "<http://192.168.1.78:8080/event>", "http://192.168.1.78:8080/event"},
		{"裸 URL", "http://192.168.1.78:8080/event", "http://192.168.1.78:8080/event"},
		{"多个 callback 取第一个", "<http://a/event> <http://b/event>", "http://a/event"},
		{"空值", "", ""},
		{"非 URL", "garbage", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCallback(tc.in); got != tc.want {
				t.Fatalf("parseCallback(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// v0.3.6 把 SSDPMaxAge 压到 120 后，GENA 订阅超时曾被一起缩短，
// 这里锁死两者互相独立。
func TestGENATimeoutIsIndependentOfSSDPMaxAge(t *testing.T) {
	if GENASubscriptionSeconds == SSDPMaxAge {
		t.Fatal("GENA 订阅超时应独立于 SSDP max-age")
	}
	if got := parseTimeout(""); got != time.Duration(GENASubscriptionSeconds)*time.Second {
		t.Fatalf("缺省订阅超时 = %s, want %ds", got, GENASubscriptionSeconds)
	}
	if got := parseTimeout("Second-300"); got != 300*time.Second {
		t.Fatalf("显式超时解析错误: %s", got)
	}
	if got := parseTimeout("600"); got != 600*time.Second {
		t.Fatalf("裸秒数解析错误: %s", got)
	}
}

func TestWriteSubscribeOKHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	writeSubscribeOK(w, "uuid:test-sid")
	res := w.Result()
	if res.StatusCode != 200 {
		t.Fatalf("状态码 = %d", res.StatusCode)
	}
	if got := res.Header.Get("SID"); got != "uuid:test-sid" {
		t.Fatalf("SID = %q", got)
	}
	if got := res.Header.Get("TIMEOUT"); got != "Second-"+strconv.Itoa(GENASubscriptionSeconds) {
		t.Fatalf("TIMEOUT = %q", got)
	}
	if res.Header.Get("SERVER") == "" {
		t.Fatal("缺少 SERVER 头")
	}
	if _, ok := res.Header["Ext"]; !ok {
		t.Fatal("GENA 响应缺少 EXT 头")
	}
}
