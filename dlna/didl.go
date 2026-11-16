package dlna

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"pc-cast/renderer"
)

// didlRes 是 <res> 元素的一条候选。控制点可能给出多个 res（不同清晰度/格式），
// 旧实现会被最后一个覆盖，导致 protocolInfo / duration 取自最差的那条。
type didlRes struct {
	uri      string
	protocol string
	duration time.Duration
	score    int64
}

// ParseDIDL 从 SetAVTransportURI 的 CurrentURIMetaData 中提取标题、时长、协议与直播标记。
// DIDL-Lite 常带各种命名空间前缀，因此只按 LocalName 匹配。
func ParseDIDL(raw string, fallbackURI string) renderer.Metadata {
	m := renderer.Metadata{URI: fallbackURI, RawXML: raw}
	if strings.TrimSpace(raw) == "" {
		return m
	}
	dec := xml.NewDecoder(strings.NewReader(raw))
	var title strings.Builder
	var res []didlRes
	inTitle := false
	cur := -1 // 当前 <res> 在 res 中的下标；用下标而非指针，避免 append 后指针失效
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return renderer.Metadata{URI: fallbackURI, RawXML: raw}
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case "title":
				inTitle = true
			case "res":
				res = append(res, didlRes{})
				cur = len(res) - 1
				for _, a := range t.Attr {
					switch strings.ToLower(a.Name.Local) {
					case "protocolinfo":
						res[cur].protocol = a.Value
					case "duration":
						if d, err := parseClockDuration(a.Value); err == nil {
							res[cur].duration = d
						}
					case "bitrate":
						if n, err := strconv.ParseInt(strings.TrimSpace(a.Value), 10, 64); err == nil {
							res[cur].score += n / 1000 // 归一化到与像素面积相近的量级
						}
					case "resolution":
						if w, h, ok := parseResolution(a.Value); ok {
							res[cur].score += w * h
						}
					}
				}
			}
		case xml.CharData:
			if inTitle {
				title.Write(t)
			}
			if cur >= 0 {
				res[cur].uri += string(t)
			}
		case xml.EndElement:
			switch strings.ToLower(t.Name.Local) {
			case "title":
				inTitle = false
			case "res":
				cur = -1
			}
		}
	}

	if s := strings.TrimSpace(title.String()); s != "" {
		m.Title = s
	}
	// 直播的强证据：DIDL 声明为 videoBroadcast，或任一 res 是 HLS / MPEG-TS，
	// 或 res 地址是 m3u8。DLNA.ORG_OP=00（不可 seek）且无时长时也算直播。
	lowerRaw := strings.ToLower(raw)
	if strings.Contains(lowerRaw, "videobroadcast") {
		m.Live = true
	}
	best := -1
	for i, r := range res {
		p := strings.ToLower(r.protocol)
		if strings.Contains(p, "mpegurl") || strings.Contains(p, "video/mp2t") {
			m.Live = true
		}
		if strings.Contains(p, "dlna.org_op=00") && r.duration == 0 {
			m.Live = true
		}
		if renderer.IsLive(r.uri, renderer.Metadata{}) {
			m.Live = true
		}
		if best < 0 || r.score > res[best].score {
			best = i
		}
	}
	if best >= 0 {
		m.ProtocolInfo = res[best].protocol
		m.Duration = res[best].duration
		// 沿用 ParseDIDL 的既有语义：DIDL 里的 res 优先，参数只是兜底。
		// 控制点最终选定的 CurrentURI 由 actions.go 再覆盖一次。
		if u := strings.TrimSpace(res[best].uri); u != "" {
			m.URI = u
		}
	}
	if m.URI == "" {
		m.URI = fallbackURI
	}
	return m
}

// parseResolution 解析形如 1920x1080 的 DIDL resolution 属性。
func parseResolution(s string) (int64, int64, bool) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(s)), "x", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || w <= 0 {
		return 0, 0, false
	}
	h, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// MakeDIDL 生成回传给控制点的 TrackMetaData / CurrentURIMetaData。
func MakeDIDL(meta renderer.Metadata) string {
	if meta.RawXML != "" {
		return meta.RawXML
	}
	uri := meta.URI
	protocol := meta.ProtocolInfo
	if protocol == "" {
		protocol = "http-get:*:*:*"
		if meta.Live {
			protocol = "http-get:*:application/vnd.apple.mpegurl:*"
		}
	}
	title := meta.Title
	if title == "" {
		title = basename(uri)
	}
	class := "object.item.videoItem"
	if meta.Live {
		class = "object.item.videoItem.videoBroadcast"
	}
	duration := ""
	if meta.Duration > 0 {
		duration = ` duration="` + renderer.FormatDuration(meta.Duration) + `"`
	}
	return fmt.Sprintf(`<DIDL-Lite xmlns="%s" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">`+
		`<item id="0" parentID="-1" restricted="1"><dc:title>%s</dc:title><upnp:class>%s</upnp:class>`+
		`<res protocolInfo="%s"%s>%s</res></item></DIDL-Lite>`,
		DIDLNamespace, xmlEscape(title), class, xmlEscape(protocol), duration, xmlEscape(uri))
}

func basename(uri string) string {
	if i := strings.LastIndex(uri, "/"); i >= 0 && i < len(uri)-1 {
		return uri[i+1:]
	}
	if uri == "" {
		return "未知媒体"
	}
	return uri
}

// parseClockDuration 解析 DIDL 中形如 00:12:34.567 的 duration 属性。
func parseClockDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("空的 duration")
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("无效的 duration %q", s)
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil {
		return 0, err
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil {
		return 0, err
	}
	var sec float64
	if _, err := fmt.Sscanf(parts[2], "%f", &sec); err != nil {
		return 0, err
	}
	return time.Duration((float64(h)*3600 + float64(m)*60 + sec) * float64(time.Second)), nil
}
