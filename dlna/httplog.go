package dlna

import (
	"log"
	"log/slog"
	"net/http"
	"strings"

	"pc-cast/logging"
)

// extraHeaders 记录已挑出的关键头之外的全部请求头。国内投屏 APP 常在
// 协商阶段带私有头（X_*、MAN、MX、SOAPACTION 等），这些是判断「控制点
// 是否在走私有协议 / 是否把请求发到了别的端口」的重要取证线索。
func extraHeaders(r *http.Request) map[string]string {
	known := map[string]bool{
		"host": true, "user-agent": true, "connection": true,
		"accept": true, "nt": true, "callback": true, "sid": true, "timeout": true,
		"content-length": true, "content-type": true,
	}
	out := make(map[string]string)
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if known[lk] {
			continue
		}
		out[lk] = strings.Join(v, ",")
	}
	return out
}

// slogErrorWriter 把 net/http 在协议层直接拒绝的请求（畸形请求行、非法
// method、超限 header 等）转记到 slog。这类请求根本不会进入 ServeHTTP，
// 是区分「手机没发请求」与「请求被 Go 丢弃」的唯一取证手段。
type slogErrorWriter struct{ log *slog.Logger }

func (w slogErrorWriter) Write(p []byte) (int, error) {
	logging.Warn(w.log, "http", "HTTP 协议层错误",
		map[string]any{"detail": strings.TrimSpace(string(p))})
	return len(p), nil
}

func httpErrorLogger(l *slog.Logger) *log.Logger {
	return log.New(slogErrorWriter{l}, "", 0)
}
