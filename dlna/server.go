// Package dlna 实现 UPnP/DLNA MediaRenderer:1 接收端（SSDP + SOAP + GENA）。
package dlna

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"pc-cast/logging"
	"pc-cast/renderer"
)

type Config struct {
	FriendlyName string
	UUID         string
	Port         int
	BindIP       string
}

type Server struct {
	player renderer.Player
	log    *slog.Logger

	mu          sync.RWMutex
	name        string
	uuid        string
	bindIPVal   string
	port        int
	playMode    string
	nextURI     string
	nextMeta    string
	warnings    []string
	started     bool
	closed      bool
	description string

	listener net.Listener
	httpSrv  *http.Server
	ssdp     *ssdpService
	subs     *subscriptions

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(cfg Config, player renderer.Player, log *slog.Logger) *Server {
	if log == nil {
		log = logging.New(slog.LevelInfo, io.Discard)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		player:    player,
		log:       log,
		name:      cfg.FriendlyName,
		uuid:      cfg.UUID,
		bindIPVal: cfg.BindIP,
		port:      cfg.Port,
		playMode:  "NORMAL",
		ctx:       ctx,
		cancel:    cancel,
	}
	s.subs = newSubscriptions(s, log)
	return s
}

func (s *Server) udn() string {
	s.mu.RLock()
	u := s.uuid
	s.mu.RUnlock()
	if strings.HasPrefix(u, "uuid:") {
		return u
	}
	return "uuid:" + u
}

func (s *Server) UDN() string { return s.udn() }

func (s *Server) bindIP() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bindIPVal
}

func (s *Server) Port() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.port
}

func (s *Server) baseURLFor(ip string) string {
	if ip == "" {
		ips := localIPv4s(s.bindIP())
		if len(ips) > 0 {
			ip = ips[0]
		} else {
			ip = "127.0.0.1"
		}
	}
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(s.Port()))
}

func (s *Server) BaseURL() string { return s.baseURLFor("") }

func (s *Server) Warnings() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.warnings...)
}

func (s *Server) NetworkKey() string {
	return strings.Join(localIPv4s(s.bindIP()), ",")
}

func (s *Server) SetFriendlyName(name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
}

func (s *Server) Refresh() {
	s.mu.RLock()
	ssdp := s.ssdp
	s.mu.RUnlock()
	if ssdp != nil {
		ssdp.refresh()
	}
}

func (s *Server) Start() error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	configured := s.port
	bindIP := s.bindIPVal
	s.mu.Unlock()

	ln, addr, mode, err := s.openListener(bindIP, configured)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listener = ln
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.httpSrv = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          httpErrorLogger(s.log),
	}
	port := s.port
	s.mu.Unlock()

	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.Error(s.log, "dlna", "HTTP 服务异常退出", map[string]any{"error": err.Error()})
		}
	}()
	go func() {
		defer s.wg.Done()
		s.subs.reapLoop(s.ctx)
	}()

	s.mu.Lock()
	s.ssdp = newSSDP(s, s.ctx, s.log)
	ssdp := s.ssdp
	s.mu.Unlock()
	if err := ssdp.start(); err != nil {
		s.mu.Lock()
		s.warnings = append(s.warnings, err.Error())
		s.mu.Unlock()
		logging.Error(s.log, "dlna", "SSDP 启动失败", map[string]any{"error": err.Error()})
	}

	s.player.SetChangeHook(s.onPlayerChange)
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	logging.Info(s.log, "dlna", "DLNA 接收端已启动", map[string]any{
		"listen": addr, "port": port, "base_url": s.BaseURL(), "udn": s.udn(),
		"port_mode": mode,
	})
	return nil
}

// openListener 建立 HTTP 监听器，返回实际监听地址与端口选择方式。
//
//	configured > 0：严格监听用户指定端口，占用则报错，绝不悄悄换端口。
//	configured = 0：优先监听 PreferredPort，被占用则依次 +1 探测；
//	                 全部失败才退回系统随机端口，保证服务仍可用。
//
// port_mode 会写进启动日志，便于核对手机端缓存的 LOCATION 是否仍有效。
func (s *Server) openListener(host string, configured int) (net.Listener, string, string, error) {
	if configured > 0 {
		addr := net.JoinHostPort(host, strconv.Itoa(configured))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, addr, "configured", fmt.Errorf("监听 HTTP 服务失败 (%s): %w", addr, err)
		}
		return ln, addr, "configured", nil
	}
	for i := 0; i < PortProbeCount; i++ {
		addr := net.JoinHostPort(host, strconv.Itoa(PreferredPort+i))
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			mode := "fixed"
			if i > 0 {
				mode = "fixed+increment"
			}
			return ln, addr, mode, nil
		}
	}
	addr := net.JoinHostPort(host, "0")
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, addr, "random", fmt.Errorf("监听 HTTP 服务失败 (%s): %w", addr, err)
	}
	logging.Warn(s.log, "dlna", "固定端口均被占用，改用系统随机端口",
		map[string]any{"preferred_port": PreferredPort, "probed": PortProbeCount,
			"hint": "手机端可能缓存旧端口，必要时重启手机投屏 APP 刷新设备列表"})
	return ln, addr, "random", nil
}

func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ssdp := s.ssdp
	httpSrv := s.httpSrv
	s.mu.Unlock()

	s.cancel()
	if ssdp != nil {
		ssdp.close() // 内部会发送 ssdp:byebye
	}
	if httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = httpSrv.Shutdown(ctx)
		cancel()
	}
	s.wg.Wait()
	logging.Info(s.log, "dlna", "DLNA 接收端已关闭", nil)
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Path
	// 部分 SDK 用 base + "/" + url 拼接，会产生 //AVTransport/control
	// 这类多余斜杠；自定义 Handler 不像 ServeMux 会自动清理，需手动归一。
	path := "/" + strings.TrimLeft(rawPath, "/")
	// 协商阶段（发现之后、SOAP 之前）记录完整关键头。POST 控制请求已有
	// dispatchAction 的动作日志，这里只记非 POST，避免轮询刷屏。
	if r.Method != http.MethodPost {
		logging.Info(s.log, "http", "收到协商请求", map[string]any{
			"remote": r.RemoteAddr, "method": r.Method, "path": path,
			"raw_path": rawPath, "proto": r.Proto,
			"headers": map[string]any{
				"host": r.Host, "user_agent": r.UserAgent(),
				"connection": r.Header.Get("Connection"), "accept": r.Header.Get("Accept"),
				"nt": r.Header.Get("NT"), "callback": r.Header.Get("CALLBACK"),
				"sid": r.Header.Get("SID"), "timeout": r.Header.Get("TIMEOUT"),
				"soapaction": r.Header.Get("SOAPACTION"),
				"extra":      extraHeaders(r),
			},
		})
	}
	for _, svc := range serviceList() {
		prefix := "/" + svc.Path + "/"
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		switch {
		case path == svc.SCPDURL:
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
				return
			}
			body := buildSCPD(svc.URN)
			writeXMLBody(w, http.StatusOK, body)
			logging.Info(s.log, "dlna", "服务描述被访问",
				map[string]any{"remote": r.RemoteAddr, "path": path,
					"user_agent": r.UserAgent(), "bytes": len(body), "transfer": "identity"})
			return
		case path == svc.ControlURL:
			if r.Method != http.MethodPost {
				http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
				return
			}
			s.handleControl(w, r, svc.URN)
			return
		case path == svc.EventURL:
			s.subs.handle(w, r, svc.URN)
			return
		}
	}
	if path == "/description.xml" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		base := s.descriptionBase(r.Host)
		body := s.descriptionFor(r.Host)
		writeXMLBody(w, http.StatusOK, body)
		logging.Info(s.log, "dlna", "设备描述被访问", map[string]any{
			"remote": r.RemoteAddr, "host": r.Host, "user_agent": r.UserAgent(),
			"url_base": base + "/", "listen_port": s.Port(),
			"bytes": len(body), "transfer": "identity",
		})
		return
	}
	http.NotFound(w, r)
	logging.Info(s.log, "dlna", "未知路径请求", map[string]any{
		"remote": r.RemoteAddr, "path": path, "method": r.Method, "user_agent": r.UserAgent(),
	})
}

// descriptionBase 生成与 SSDP LOCATION 严格一致的 URLBase。
//
// 绝不能直接信任 Host 头。实测央视频等 Android 投屏 SDK 连接的是 8768
// 端口，却手写 "Host: 192.168.1.2"（不带端口）。旧实现直接
// "http://" + r.Host，导致 URLBase 退化成 80 端口；控制点按它解析
// controlURL / eventSubURL 后，SOAP 与 SUBSCRIBE 全部发到 80 端口，
// 服务端一条都收不到 —— 日志表现为「拉完 SCPD 就再无请求、随后重新
// M-SEARCH」。另一种做法是不下发 URLBase、只给相对服务 URL，让控制点
// 全部按 LOCATION 解析；本实现保留 URLBase 并把服务 URL 绝对化，以同时
// 兼容依赖 URLBase 与依赖 LOCATION 的两类 SDK。
//
// 这里主机名沿用 Host（保证多网卡时与手机实际访问的地址一致），端口
// 恒取实际监听端口，与 SSDP LOCATION 的 origin 完全一致。
func (s *Server) descriptionBase(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return s.BaseURL()
	}
	name := h
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		if _, err := strconv.Atoi(h[i+1:]); err == nil {
			name = h[:i] // 丢弃 Host 自带的端口，统一用监听端口
		}
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, "["), "]")
	if name == "" {
		return s.BaseURL()
	}
	port := s.Port()
	if port <= 0 {
		// 尚未调用 Start（单测用 httptest 直接挂 handler）：若 Host 自带
		// 端口就沿用，否则退回 BaseURL，避免生成 ":0" 这种无效地址。
		if i := strings.LastIndexByte(h, ':'); i >= 0 {
			if p, err := strconv.Atoi(h[i+1:]); err == nil && p > 0 {
				return "http://" + h
			}
		}
		return s.BaseURL()
	}
	return "http://" + net.JoinHostPort(name, strconv.Itoa(port))
}

func (s *Server) descriptionFor(host string) string {
	base := s.descriptionBase(host)
	s.mu.RLock()
	name := s.name
	s.mu.RUnlock()
	return buildDescription(descriptionData{
		URLBase:      base + "/",
		FriendlyName: name,
		UDN:          s.udn(),
		Services:     serviceList(),
	})
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request, urn string) {
	action, err := parseSOAP(r)
	if err != nil {
		logging.Warn(s.log, "soap", "解析请求失败", map[string]any{"error": err.Error()})
		writeSOAPFault(w, 401, err.Error())
		return
	}
	if action.ServiceURN != "" && action.ServiceURN != urn {
		logging.Debug(s.log, "soap", "SOAPACTION 与 URL 服务不一致，以 URL 为准",
			map[string]any{"header": action.ServiceURN, "url": urn})
	}
	s.dispatchAction(w, r, urn, action.Name, action.Args)
}

func (s *Server) onPlayerChange() {
	s.subs.notifyService(ServiceAVTransport, false)
	s.subs.notifyService(ServiceRenderingControl, false)
}

// eventBody 生成 GENA propertyset。LastChange 的内容本身是 XML，需转义后作为字符串传递。
func (s *Server) eventBody(urn string, _ bool) string {
	switch urn {
	case ServiceAVTransport:
		return propertySet(map[string]string{"LastChange": s.avTransportLastChange()})
	case ServiceRenderingControl:
		return propertySet(map[string]string{"LastChange": s.renderingControlLastChange()})
	case ServiceConnectionManager:
		return propertySet(map[string]string{
			"SourceProtocolInfo":   "",
			"SinkProtocolInfo":     ProtocolInfoSink,
			"CurrentConnectionIDs": "0",
		})
	default:
		return ""
	}
}

func (s *Server) avTransportLastChange() string {
	state := s.player.State()
	meta := s.player.CurrentMetadata()
	pos, duration := s.player.Position()
	if meta.Duration > 0 {
		duration = meta.Duration
	}
	uri := s.player.CurrentURI()
	didl := MakeDIDL(meta)
	durationText := renderer.FormatRelTime(duration)
	positionText := renderer.FormatRelTime(pos)
	// 直播没有可定位的时长：回 00:00:00，而不是 NOT_IMPLEMENTED。
	// 部分控制点（含央视频）把 NOT_IMPLEMENTED 视为设备不支持该能力，
	// 会主动发 Stop 断开投屏。
	if meta.Live {
		durationText = "00:00:00"
		positionText = "00:00:00"
	}
	s.mu.RLock()
	mode := s.playMode
	s.mu.RUnlock()
	vars := [][2]string{
		{"TransportState", string(state)},
		{"TransportStatus", "OK"},
		{"CurrentSpeed", "1"},
		{"CurrentURI", uri},
		{"CurrentURIMetaData", didl},
		{"NumberOfTracks", "1"},
		{"CurrentTrack", "1"},
		{"CurrentTrackDuration", durationText},
		{"CurrentTrackMetaData", didl},
		{"CurrentTrackURI", uri},
		{"RelativeTimePosition", positionText},
		{"AbsoluteTimePosition", "NOT_IMPLEMENTED"},
		{"CurrentPlayMode", mode},
		{"PlaybackStorageMedium", "NETWORK"},
	}
	var b strings.Builder
	b.WriteString(`<Event xmlns="` + EventNSAVTransport + `"><InstanceID val="0">`)
	for _, kv := range vars {
		b.WriteString(`<` + kv[0] + ` val="` + xmlEscape(kv[1]) + `"/>`)
	}
	b.WriteString(`</InstanceID></Event>`)
	return b.String()
}

func (s *Server) renderingControlLastChange() string {
	var b strings.Builder
	b.WriteString(`<Event xmlns="` + EventNSRenderingControl + `"><InstanceID val="0">`)
	b.WriteString(`<Volume val="` + strconv.Itoa(s.player.Volume()) + `" channel="Master"/>`)
	b.WriteString(`<Mute val="` + boolTo01(s.player.Mute()) + `" channel="Master"/>`)
	b.WriteString(`</InstanceID></Event>`)
	return b.String()
}

// LocalIPv4s 供设置界面枚举可绑定网卡。
func LocalIPv4s(bind string) []string { return localIPv4s(bind) }
