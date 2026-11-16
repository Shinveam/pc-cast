package dlna

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"pc-cast/logging"
)

// newHeaderTestServer 使用真实 TCP 服务器；httptest.ResponseRecorder 会直接
// 记录 body，不会触发 net/http 的 chunked 分支，无法复现手机端的问题。
func newHeaderTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := New(Config{FriendlyName: "测试接收端", UUID: "test-uuid-headers"},
		&fakePlayer{}, logging.New(slog.LevelError, io.Discard))
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv
}

func assertIdentityTransfer(t *testing.T, resp *http.Response, wantLen, wantBodyLen int) []byte {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Content-Length"); got == "" {
		t.Errorf("缺少 Content-Length，Transfer-Encoding=%q", resp.Header.Get("Transfer-Encoding"))
	} else if n, _ := strconv.Atoi(got); n != wantLen {
		t.Errorf("Content-Length=%s，实际 %d 字节", got, wantLen)
	}
	if te := resp.Header.Get("Transfer-Encoding"); te != "" {
		t.Errorf("响应仍为 chunked: %q（老 DLNA SDK 会读到超时）", te)
	}
	if len(resp.TransferEncoding) != 0 {
		t.Errorf("客户端仍按分块解码: %v", resp.TransferEncoding)
	}
	if resp.ContentLength != int64(wantLen) {
		t.Errorf("客户端看到的长度 %d，期望 %d", resp.ContentLength, wantLen)
	}
	if len(body) != wantBodyLen {
		t.Errorf("实际读取 %d 字节，期望 %d", len(body), wantBodyLen)
	}
	return body
}

// TestXMLResponsesDeclareContentLength 覆盖所有设备/服务描述端点。
// 这些 XML 都超过 net/http 的 2KB 自动缓冲阈值，修复前全部走 chunked。
func TestXMLResponsesDeclareContentLength(t *testing.T) {
	srv := newHeaderTestServer(t)
	paths := []string{"/description.xml"}
	for _, svc := range serviceList() {
		paths = append(paths, svc.SCPDURL)
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			resp, err := http.Get(srv.URL + p)
			if err != nil {
				t.Fatal(err)
			}
			want := int(resp.ContentLength)
			if want <= 0 {
				// 修复前 chunked 时 ContentLength 为 -1，先读出长度再断言。
				b, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				t.Fatalf("响应没有有效长度（chunked=%q），body %d 字节", resp.Header.Get("Transfer-Encoding"), len(b))
			}
			body := assertIdentityTransfer(t, resp, want, want)
			if !strings.HasPrefix(string(body), `<?xml version="1.0"`) {
				t.Errorf("XML 声明缺失: %.80s", body)
			}
		})
	}
}

func TestSCPDHeadKeepsContentLength(t *testing.T) {
	srv := newHeaderTestServer(t)
	for _, svc := range serviceList() {
		req, _ := http.NewRequest(http.MethodHead, srv.URL+svc.SCPDURL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		want := int(resp.ContentLength)
		if want <= 0 {
			t.Fatalf("%s HEAD 没有 Content-Length", svc.SCPDURL)
		}
		body := assertIdentityTransfer(t, resp, want, 0)
		if len(body) != 0 {
			t.Errorf("%s HEAD 不应返回 body，实际 %d 字节", svc.SCPDURL, len(body))
		}
	}
}

func soapRequest(t *testing.T, url, action, args string) *http.Response {
	t.Helper()
	env := `<?xml version="1.0"?><s:Envelope xmlns:s="` + SOAPEnvelopeNS + `"><s:Body>` +
		`<u:` + action + ` xmlns:u="` + ServiceAVTransport + `">` + args +
		`</u:` + action + `></s:Body></s:Envelope>`
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(env))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", `"`+ServiceAVTransport+`#`+action+`"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSOAPResponsesDeclareContentLength(t *testing.T) {
	srv := newHeaderTestServer(t)
	url := srv.URL + "/AVTransport/control"

	t.Run("正常响应", func(t *testing.T) {
		resp := soapRequest(t, url, "GetTransportInfo", "<InstanceID>0</InstanceID>")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("SOAP 状态 %d", resp.StatusCode)
		}
		want := int(resp.ContentLength)
		if want <= 0 {
			t.Fatalf("SOAP 响应没有 Content-Length，chunked=%q", resp.Header.Get("Transfer-Encoding"))
		}
		body := assertIdentityTransfer(t, resp, want, want)
		if !strings.Contains(string(body), "GetTransportInfoResponse") {
			t.Errorf("SOAP body 异常: %.120s", body)
		}
	})

	t.Run("错误响应", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader("not xml"))
		req.Header.Set("SOAPACTION", `"`+ServiceAVTransport+`#Play"`)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		want := int(resp.ContentLength)
		if want <= 0 {
			t.Fatalf("SOAP fault 没有 Content-Length，chunked=%q", resp.Header.Get("Transfer-Encoding"))
		}
		assertIdentityTransfer(t, resp, want, want)
	})
}
