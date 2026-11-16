package dlna

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"pc-cast/logging"
)

type subscription struct {
	sid      string
	service  string
	callback string
	expires  time.Time
	seq      uint32
	mu       sync.Mutex
}

func (s *subscription) nextSeq() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seq == 0 {
		s.seq = 1
	}
	n := s.seq
	s.seq++
	return n
}

type subscriptions struct {
	parent *Server
	log    *slog.Logger
	client *http.Client

	mu    sync.RWMutex
	bySID map[string]*subscription
}

func newSubscriptions(parent *Server, log *slog.Logger) *subscriptions {
	return &subscriptions{
		parent: parent,
		log:    log,
		client: &http.Client{Timeout: 3 * time.Second},
		bySID:  make(map[string]*subscription),
	}
}

func newSID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "uuid:" + hex.EncodeToString(b[:])
}

func parseCallback(v string) string {
	v = strings.TrimSpace(v)
	start := strings.IndexByte(v, '<')
	if start < 0 {
		// 部分 SDK 直接给裸 URL；取第一个 http:// 开头的片段兼容。
		for _, part := range strings.Fields(v) {
			if strings.HasPrefix(strings.ToLower(part), "http://") {
				return strings.Trim(part, "<>")
			}
		}
		return ""
	}
	end := strings.IndexByte(v[start+1:], '>')
	if end < 0 {
		return ""
	}
	return v[start+1 : start+1+end]
}

func parseTimeout(v string) time.Duration {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(strings.ToLower(v), "second-") {
		if n, err := strconv.Atoi(v[len("second-"):]); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return time.Duration(GENASubscriptionSeconds) * time.Second
}

func (s *subscriptions) handle(w http.ResponseWriter, r *http.Request, urn string) {
	// 控制点拉完 SCPD 之后通常紧接着发 SUBSCRIBE；这一步以前只有 Debug 日志，
	// 无法区分「没发订阅」与「订阅被拒」，是「搜得到、点不动」的关键取证点。
	logging.Info(s.log, "gena", "收到事件订阅请求", map[string]any{
		"remote": r.RemoteAddr, "method": r.Method, "service": urn,
		"sid": r.Header.Get("SID"), "nt": r.Header.Get("NT"),
		"callback": r.Header.Get("CALLBACK"), "timeout": r.Header.Get("TIMEOUT"),
		"user_agent": r.UserAgent(),
	})
	switch strings.ToUpper(r.Method) {
	case "SUBSCRIBE":
		s.subscribe(w, r, urn)
	case "UNSUBSCRIBE":
		s.unsubscribe(w, r)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (s *subscriptions) subscribe(w http.ResponseWriter, r *http.Request, urn string) {
	if sid := r.Header.Get("SID"); sid != "" { // 续订
		s.mu.RLock()
		sub, ok := s.bySID[sid]
		s.mu.RUnlock()
		if !ok || sub.service != urn {
			logging.Warn(s.log, "gena", "订阅续订被拒",
				map[string]any{"sid": sid, "reason": "SID 不存在或服务不匹配"})
			writeUPnPError(w, 412, "Precondition Failed")
			return
		}
		sub.mu.Lock()
		sub.expires = time.Now().Add(parseTimeout(r.Header.Get("TIMEOUT")))
		sub.mu.Unlock()
		writeSubscribeOK(w, sid)
		return
	}
	if nt := strings.ToLower(r.Header.Get("NT")); nt != "" && nt != "upnp:event" {
		logging.Warn(s.log, "gena", "订阅被拒", map[string]any{"reason": "NT 不是 upnp:event", "nt": nt})
		writeUPnPError(w, 412, "Precondition Failed")
		return
	}
	callback := parseCallback(r.Header.Get("CALLBACK"))
	if callback == "" || !strings.HasPrefix(callback, "http://") {
		logging.Warn(s.log, "gena", "订阅被拒",
			map[string]any{"reason": "缺少有效 CALLBACK", "callback": r.Header.Get("CALLBACK")})
		writeUPnPError(w, 412, "Invalid Callback")
		return
	}
	sub := &subscription{
		sid:      newSID(),
		service:  urn,
		callback: callback,
		expires:  time.Now().Add(parseTimeout(r.Header.Get("TIMEOUT"))),
	}
	s.mu.Lock()
	s.bySID[sub.sid] = sub
	s.mu.Unlock()
	writeSubscribeOK(w, sub.sid)
	logging.Info(s.log, "gena", "新建订阅成功",
		map[string]any{"sid": sub.sid, "callback": callback, "service": urn,
			"timeout_seconds": GENASubscriptionSeconds})
	go s.push(sub, s.parent.eventBody(urn, true))
}

func (s *subscriptions) unsubscribe(w http.ResponseWriter, r *http.Request) {
	sid := r.Header.Get("SID")
	if sid == "" {
		logging.Warn(s.log, "gena", "取消订阅被拒", map[string]any{"reason": "Missing SID"})
		writeUPnPError(w, 412, "Missing SID")
		return
	}
	s.mu.Lock()
	delete(s.bySID, sid)
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func writeSubscribeOK(w http.ResponseWriter, sid string) {
	w.Header().Set("SID", sid)
	w.Header().Set("TIMEOUT", "Second-"+strconv.Itoa(GENASubscriptionSeconds))
	w.Header().Set("SERVER", serverToken)
	w.Header().Set("EXT", "")
	w.Header().Set("CONTENT-LENGTH", "0")
	w.Header().Set("CONNECTION", "close")
	w.WriteHeader(http.StatusOK)
}

func writeUPnPError(w http.ResponseWriter, code int, desc string) {
	body := fmt.Sprintf(`<?xml version="1.0"?><UPnPError xmlns="%s"><errorCode>%d</errorCode><errorDescription>%s</errorDescription></UPnPError>`,
		UPnPControlNS, code, xmlEscape(desc))
	writeXMLBody(w, code, body)
}

// notifyService 把状态变化推给订阅该服务的所有控制点。
func (s *subscriptions) notifyService(urn string, initial bool) {
	body := s.parent.eventBody(urn, initial)
	if body == "" {
		return
	}
	s.mu.RLock()
	list := make([]*subscription, 0, len(s.bySID))
	for _, sub := range s.bySID {
		if sub.service == urn {
			list = append(list, sub)
		}
	}
	s.mu.RUnlock()
	for _, sub := range list {
		go s.push(sub, body)
	}
}

func (s *subscriptions) push(sub *subscription, body string) {
	sub.mu.Lock()
	expired := time.Now().After(sub.expires)
	sub.mu.Unlock()
	if expired {
		s.mu.Lock()
		delete(s.bySID, sub.sid)
		s.mu.Unlock()
		return
	}
	req, err := http.NewRequest("NOTIFY", sub.callback, bytes.NewReader([]byte(body)))
	if err != nil {
		return
	}
	req.Header.Set("CONTENT-TYPE", `text/xml; charset="utf-8"`)
	req.Header.Set("NT", "upnp:event")
	req.Header.Set("NTS", "upnp:propchange")
	req.Header.Set("SID", sub.sid)
	req.Header.Set("SEQ", strconv.FormatUint(uint64(sub.nextSeq()), 10))
	resp, err := s.client.Do(req)
	if err != nil {
		logging.Debug(s.log, "gena", "事件推送失败", map[string]any{"callback": sub.callback, "error": err.Error()})
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func (s *subscriptions) reapLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			for sid, sub := range s.bySID {
				sub.mu.Lock()
				expired := now.After(sub.expires)
				sub.mu.Unlock()
				if expired {
					delete(s.bySID, sid)
				}
			}
			s.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

// propertySet 构造 GENA 的 propertyset 报文。
func propertySet(properties map[string]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<e:propertyset xmlns:e="urn:schemas-upnp-org:event-1-0">`)
	for name, value := range properties {
		b.WriteString("<e:property><" + name + ">" + xmlEscape(value) + "</" + name + "></e:property>")
	}
	b.WriteString("</e:propertyset>")
	return b.String()
}
