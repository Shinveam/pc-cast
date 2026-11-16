package dlna

import (
	"net/http"
	"strconv"
)

// writeXMLBody 一次性写出 XML 响应，并显式声明 Content-Length。
//
// Go 的 net/http 只有在整个响应体小于 2KB 时才会自动补 Content-Length，
// 超过该阈值就改用 Transfer-Encoding: chunked。本机实测三份 SCPD 分别为
// 3.6KB / 4.1KB / 12.8KB，全部走 chunked，而 1.5KB 的 description.xml
// 带 Content-Length 且能被手机正常解析。部分 Android DLNA SDK 自研的
// HTTP 客户端不解码 chunked，会一直等响应结束直到超时，表现为「拉完
// AVTransport SCPD 后放弃设备、反复重新搜索」，且每版都固定卡 8 秒。
//
// 显式 Content-Length 让客户端按长度判定响应结束。这里不额外设置
// Connection: close：它是 hop-by-hop 头，由 Go 的 net/http 自行管理，
// handler 里设置会被过滤掉，反而造成「以为生效、实际没生效」的错觉。
func writeXMLBody(w http.ResponseWriter, status int, body string) {
	b := []byte(body)
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.Header().Set("SERVER", serverToken)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	if len(b) > 0 {
		_, _ = w.Write(b)
	}
}
