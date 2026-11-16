package dlna

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"

	"pc-cast/logging"
)

const (
	serverToken = "PC-Cast/1.0 UPnP/1.0 DLNADOC/1.50"
	// 高频 alive：不少国内投屏 APP 依赖持续收到 NOTIFY 才认为设备在线，
	// 只按 UPnP 规范每 max-age/2 广播一次时，列表里看得到却点不动。
	// 间隔过长会让它把列表里的设备判为失效并反复重新 M-SEARCH。
	announceEvery = 5 * time.Second
	otherLogEvery = 30 * time.Second
)

type target struct {
	NT  string
	USN string
}

type ssdpService struct {
	parent *Server
	log    *slog.Logger
	ctx    context.Context

	mu       sync.Mutex
	recv     *net.UDPConn
	senders  []*net.UDPConn
	localIPs []string
	wg       sync.WaitGroup
	closed   bool

	// 非 M-SEARCH 组播包（其它设备的 NOTIFY、投屏 SDK 的私有探测包）取证。
	// 时间窗内只记一条汇总，避免组播风暴把日志刷爆。
	otherMu    sync.Mutex
	otherCount int
	otherLast  time.Time
}

func newSSDP(parent *Server, ctx context.Context, log *slog.Logger) *ssdpService {
	return &ssdpService{parent: parent, ctx: ctx, log: log}
}

func (s *ssdpService) targets() []target {
	udn := s.parent.udn()
	t := []target{
		{"upnp:rootdevice", udn + "::upnp:rootdevice"},
		{udn, udn},
		{DeviceTypeMediaRenderer, udn + "::" + DeviceTypeMediaRenderer},
	}
	for _, svc := range serviceList() {
		t = append(t, target{svc.URN, udn + "::" + svc.URN})
	}
	return t
}

func (s *ssdpService) start() error {
	recv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: SSDPMulticastPort})
	if err != nil {
		return fmt.Errorf("监听 SSDP 端口 %d 失败（可能被系统 SSDP 服务占用）: %w", SSDPMulticastPort, err)
	}
	s.mu.Lock()
	s.recv = recv
	s.mu.Unlock()

	pc := ipv4.NewPacketConn(recv)
	ifaces, _ := net.Interfaces()
	for i := range ifaces {
		ifi := ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		if err := pc.JoinGroup(&ifi, &net.UDPAddr{IP: net.ParseIP(SSDPMulticastAddr)}); err != nil {
			logging.Debug(s.log, "ssdp", "加入组播失败", map[string]any{"interface": ifi.Name, "error": err.Error()})
		}
	}

	s.rebuildSenders()
	s.wg.Add(2)
	go s.readLoop()
	go s.announceLoop()
	s.announce("ssdp:alive")
	logging.Info(s.log, "ssdp", "SSDP 已启动", map[string]any{"ips": strings.Join(s.currentIPs(), ",")})
	return nil
}

// rebuildSenders 为每个本机 IPv4 建立独立的发送 socket，保证宣告的源地址与 LOCATION 一致。
func (s *ssdpService) rebuildSenders() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.senders {
		_ = c.Close()
	}
	s.senders = nil
	s.localIPs = localIPv4s(s.parent.bindIP())
	for _, ip := range s.localIPs {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(ip), Port: 0})
		if err != nil {
			logging.Warn(s.log, "ssdp", "创建发送 socket 失败", map[string]any{"ip": ip, "error": err.Error()})
			continue
		}
		s.senders = append(s.senders, c)
	}
	if len(s.senders) == 0 {
		logging.Warn(s.log, "ssdp", "未找到可用 IPv4 地址，设备可能无法被发现", nil)
	}
}

func (s *ssdpService) currentIPs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.localIPs...)
}

func (s *ssdpService) refresh() {
	s.rebuildSenders()
	s.announce("ssdp:alive")
	logging.Info(s.log, "ssdp", "网络变化，重新宣告", map[string]any{"ips": strings.Join(s.currentIPs(), ",")})
}

func (s *ssdpService) readLoop() {
	defer s.wg.Done()
	buf := make([]byte, 8192)
	for {
		n, from, err := s.recv.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
				logging.Warn(s.log, "ssdp", "读取组播失败", map[string]any{"error": err.Error()})
				return
			}
		}
		msg := string(buf[:n])
		if !strings.HasPrefix(strings.ToUpper(msg), "M-SEARCH") {
			s.noteOtherPacket(from, msg)
			continue
		}
		s.handleMSearch(msg, from)
	}
}

// noteOtherPacket 让「控制点发过包但没走标准 DLNA」这件事留下证据。
// 本机自己发出的 alive 会被组播回环，属于噪音，直接忽略。
func (s *ssdpService) noteOtherPacket(from *net.UDPAddr, msg string) {
	for _, ip := range s.currentIPs() {
		if from.IP.String() == ip {
			return
		}
	}
	s.otherMu.Lock()
	s.otherCount++
	if !s.otherLast.IsZero() && time.Since(s.otherLast) < otherLogEvery {
		s.otherMu.Unlock()
		return
	}
	s.otherLast = time.Now()
	count := s.otherCount
	s.otherMu.Unlock()

	first := strings.TrimSpace(strings.SplitN(msg, "\n", 2)[0])
	if len(first) > 120 {
		first = first[:117] + "…"
	}
	logging.Info(s.log, "ssdp", "收到非 M-SEARCH 组播包",
		map[string]any{"from": from.String(), "first_line": first, "total": count,
			"hint": "若来自手机且随后没有设备描述/SOAP 请求，说明控制点用了私有探测协议"})
}

func (s *ssdpService) handleMSearch(msg string, from *net.UDPAddr) {
	lines := strings.Split(strings.ReplaceAll(msg, "\r\n", "\n"), "\n")
	st, mx := "", 1
	for _, line := range lines[1:] {
		if i := strings.IndexByte(line, ':'); i > 0 {
			key := strings.ToUpper(strings.TrimSpace(line[:i]))
			val := strings.TrimSpace(line[i+1:])
			switch key {
			case "ST":
				st = val
			case "MX":
				if _, err := fmt.Sscanf(val, "%d", &mx); err != nil || mx <= 0 {
					mx = 1
				}
			}
		}
	}
	if st == "" {
		return
	}
	// 在 MX 窗口内随机延迟，避免与其它 UPnP 设备的响应碰撞。
	if mx > 0 {
		time.Sleep(time.Duration(rand.Intn(min(mx, 3)*100)) * time.Millisecond)
	}
	targets := s.targets()
	matched := make([]target, 0)
	for _, t := range targets {
		if st == "ssdp:all" || st == t.NT {
			matched = append(matched, t)
		}
	}
	compat := false
	if len(matched) == 0 && isVersionAlias(st, targets) {
		// 部分控制点（含国内视频 APP）搜索 MediaRenderer:2 / AVTransport:2，
		// 而本设备实现的是 :1。UPnP 要求响应的 ST 与请求严格一致，
		// 因此用请求值构造响应目标，避免设备在这些 APP 的列表里消失。
		matched = append(matched, target{NT: st, USN: s.parent.udn() + "::" + st})
		compat = true
		logging.Info(s.log, "ssdp", "兼容响应版本化设备搜索",
			map[string]any{"from": from.String(), "st": st})
	}
	if len(matched) == 0 {
		// 控制点搜索了设备却没得到响应，是「手机搜不到 / 投屏没反应」的
		// 第一手证据。以前这里静默返回，无法区分「没发包」与「ST 不匹配」。
		logging.Info(s.log, "ssdp", "收到不匹配的设备搜索",
			map[string]any{"from": from.String(), "st": st})
		return
	}
	ip := s.pickIP(from.IP)
	for _, t := range matched {
		s.sendResponse(from, ip, t)
	}
	logging.Info(s.log, "ssdp", "响应 M-SEARCH",
		map[string]any{"from": from.String(), "st": st, "matched": len(matched), "compat": compat})
}

// isVersionAlias 判断 st 是否是本设备已声明目标的更高版本号（:2 / :3）。
func isVersionAlias(st string, targets []target) bool {
	for _, v := range []string{":2", ":3"} {
		if !strings.HasSuffix(st, v) {
			continue
		}
		base := strings.TrimSuffix(st, v) + ":1"
		for _, t := range targets {
			if t.NT == base {
				return true
			}
		}
	}
	return false
}

func (s *ssdpService) sendResponse(from *net.UDPAddr, ip string, t target) {
	body := strings.Join([]string{
		"HTTP/1.1 200 OK",
		"CACHE-CONTROL: max-age=" + fmt.Sprint(SSDPMaxAge),
		"DATE: " + time.Now().UTC().Format(httpTimeFormat),
		"EXT:",
		"LOCATION: " + s.parent.baseURLFor(ip) + "/description.xml",
		"SERVER: " + serverToken,
		"ST: " + t.NT,
		"USN: " + t.USN,
		"BOOTID.UPNP.ORG: 1",
		"CONFIGID.UPNP.ORG: 1",
		"", "",
	}, "\r\n")
	if _, err := s.recv.WriteToUDP([]byte(body), from); err != nil {
		logging.Warn(s.log, "ssdp", "发送 M-SEARCH 响应失败", map[string]any{"error": err.Error()})
	}
}

const httpTimeFormat = "Mon, 02 Jan 2006 15:04:05 GMT"

func (s *ssdpService) announce(nts string) {
	group := &net.UDPAddr{IP: net.ParseIP(SSDPMulticastAddr), Port: SSDPMulticastPort}
	s.mu.Lock()
	senders := append([]*net.UDPConn(nil), s.senders...)
	ips := append([]string(nil), s.localIPs...)
	s.mu.Unlock()
	for i, c := range senders {
		ip := ""
		if i < len(ips) {
			ip = ips[i]
		}
		for _, t := range s.targets() {
			lines := []string{
				"NOTIFY * HTTP/1.1",
				"HOST: " + SSDPMulticastAddr + ":" + fmt.Sprint(SSDPMulticastPort),
			}
			if nts == "ssdp:alive" {
				lines = append(lines,
					"CACHE-CONTROL: max-age="+fmt.Sprint(SSDPMaxAge),
					"LOCATION: "+s.parent.baseURLFor(ip)+"/description.xml",
					"SERVER: "+serverToken,
				)
			}
			lines = append(lines, "NT: "+t.NT, "NTS: "+nts, "USN: "+t.USN, "", "")
			if _, err := c.WriteToUDP([]byte(strings.Join(lines, "\r\n")), group); err != nil {
				logging.Warn(s.log, "ssdp", "发送宣告失败", map[string]any{"nts": nts, "error": err.Error()})
			}
		}
	}
}

func (s *ssdpService) announceLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(announceEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.announce("ssdp:alive")
		case <-s.ctx.Done():
			return
		}
	}
}

// pickIP 优先选择与请求方处于同一子网的本地地址。
func (s *ssdpService) pickIP(remote net.IP) string {
	s.mu.Lock()
	ips := append([]string(nil), s.localIPs...)
	bind := s.parent.bindIP()
	s.mu.Unlock()
	if bind != "" {
		return bind
	}
	if remote == nil || remote.To4() == nil {
		if len(ips) > 0 {
			return ips[0]
		}
		return "127.0.0.1"
	}
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil || !n.Contains(remote) {
				continue
			}
			for _, ip := range ips {
				if ip == n.IP.To4().String() {
					return ip
				}
			}
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return "127.0.0.1"
}

func (s *ssdpService) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	recv := s.recv
	s.mu.Unlock()

	s.announce("ssdp:byebye")
	if recv != nil {
		_ = recv.Close()
	}
	s.mu.Lock()
	for _, c := range s.senders {
		_ = c.Close()
	}
	s.senders = nil
	s.mu.Unlock()
	s.wg.Wait()
}

// localIPv4s 枚举可用的非回环 IPv4，私有地址优先。
func localIPv4s(bind string) []string {
	if bind != "" {
		if ip := net.ParseIP(bind).To4(); ip != nil {
			return []string{ip.String()}
		}
		return nil
	}
	seen := make(map[string]bool)
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := n.IP.To4()
			if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
				continue
			}
			seen[v4.String()] = true
		}
	}
	ips := make([]string, 0, len(seen))
	for ip := range seen {
		ips = append(ips, ip)
	}
	sort.Slice(ips, func(i, j int) bool {
		pi, pj := privateRank(ips[i]), privateRank(ips[j])
		if pi == pj {
			return ips[i] < ips[j]
		}
		return pi < pj
	})
	return ips
}

func privateRank(ip string) int {
	p := net.ParseIP(ip).To4()
	if p == nil {
		return 9
	}
	switch {
	case p[0] == 192 && p[1] == 168:
		return 0
	case p[0] == 10:
		return 1
	case p[0] == 172 && p[1] >= 16 && p[1] <= 31:
		return 2
	default:
		return 3
	}
}
