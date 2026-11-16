package dlna

import "strings"

const (
	DeviceTypeMediaRenderer  = "urn:schemas-upnp-org:device:MediaRenderer:1"
	ServiceAVTransport       = "urn:schemas-upnp-org:service:AVTransport:1"
	ServiceRenderingControl  = "urn:schemas-upnp-org:service:RenderingControl:1"
	ServiceConnectionManager = "urn:schemas-upnp-org:service:ConnectionManager:1"

	ServiceIDAVTransport       = "urn:upnp-org:serviceId:AVTransport"
	ServiceIDRenderingControl  = "urn:upnp-org:serviceId:RenderingControl"
	ServiceIDConnectionManager = "urn:upnp-org:serviceId:ConnectionManager"

	SOAPEnvelopeNS = "http://schemas.xmlsoap.org/soap/envelope/"
	SOAPEncoding   = "http://schemas.xmlsoap.org/soap/encoding/"
	UPnPControlNS  = "urn:schemas-upnp-org:control-1-0"
	DeviceNS       = "urn:schemas-upnp-org:device-1-0"
	ServiceNS      = "urn:schemas-upnp-org:service-1-0"
	DIDLNamespace  = "urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/"

	EventNSAVTransport       = "urn:schemas-upnp-org:metadata-1-0/AVTransport/"
	EventNSRenderingControl  = "urn:schemas-upnp-org:metadata-1-0/RenderingControl/"
	EventNSConnectionManager = "urn:schemas-upnp-org:metadata-1-0/ConnectionManager/"

	SSDPMulticastAddr = "239.255.255.250"
	SSDPMulticastPort = 1900
	// SSDPMaxAge 压到 2 分钟：很多国内投屏 APP 只在 alive/max-age 窗口内
	// 认为设备在线，30 分钟的 max-age 会让列表里的设备「看得到、点不动」。
	SSDPMaxAge = 120

	// GENASubscriptionSeconds 必须独立于 SSDP max-age：alive 周期压到 30 秒后，
	// 事件订阅超时不应跟着缩短，否则控制点要频繁续订甚至判定设备不稳定。
	GENASubscriptionSeconds = 1800

	// PreferredPort 是 port=0 时优先使用的固定端口。手机投屏 APP 会把
	// SSDP LOCATION 里的 IP:PORT 缓存下来；若每次启动随机换端口，列表里
	// 依旧显示设备名，但点击时连的是旧端口，连一条 HTTP 请求都不会到。
	// 因此固定端口优先、占用时递增，保证每次启动端口稳定，且不改写用户配置。
	PreferredPort  = 8768
	PortProbeCount = 12
)

// sinkFormats 是 GetProtocolInfo 与 GENA SourceProtocolInfo 事件共用的 Sink 声明。
//
// 两条实测约束决定了这里的写法：
//  1. 绝不能声明 AVC_MP4_MP_SD_* 之类的标清档 DLNA PN。控制点会据此把设备
//     判定为「只支持标清」，直接推低码率流（实测 B 站投屏 bw≈317kbps / 360P）。
//     改为声明 HD 档 + 通配，让控制点自行选择可用高清流。
//  2. 必须显式列出直播常用 MIME。很多控制点（尤其国内视频 APP）在推流前
//     逐条匹配 GetProtocolInfo 的 Sink，找不到 HLS / MPEG-TS 就放弃投屏，
//     根本不会发 SetAVTransportURI；末尾的 http-get:*:*:* 兜底并不总被采纳。
var sinkFormats = []string{
	// 高清档 profile + 通用 MP4，OP=01 表示支持 time-seek。
	"http-get:*:video/mp4:DLNA.ORG_PN=AVC_MP4_MP_HD_720p_AAC;DLNA.ORG_OP=01",
	"http-get:*:video/mp4:DLNA.ORG_PN=AVC_MP4_MP_HD_1080i_AAC;DLNA.ORG_OP=01",
	"http-get:*:video/mp4:*",
	// 直播：HLS / MPEG-TS / FLV / 未知二进制流。
	// 逐条匹配 Sink 的国内视频 APP 常用 video/m3u8、video/flv 等非正式 MIME；
	// 缺少这些条目时控制点会直接放弃投屏，连 SetAVTransportURI 都不发。
	// 末尾的通配兜底并不总被采纳，因此这里显式列出，但不引入任何标清档 DLNA PN。
	"http-get:*:application/vnd.apple.mpegurl:*",
	"http-get:*:application/x-mpegURL:*",
	"http-get:*:application/octet-stream:*",
	"http-get:*:video/mp2t:*",
	"http-get:*:video/MP2T:*",
	"http-get:*:video/x-flv:*",
	"http-get:*:video/m3u8:*",
	"http-get:*:video/flv:*",
	"http-get:*:video/f4v:*",
	"http-get:*:video/vnd.dlna.mpeg-tts:*",
	// 点播常见容器与音频、图片。
	"http-get:*:video/x-matroska:*",
	"http-get:*:video/avi:*",
	"http-get:*:video/x-msvideo:*",
	"http-get:*:video/quicktime:*",
	"http-get:*:video/x-ms-wmv:*",
	"http-get:*:video/webm:*",
	"http-get:*:video/3gpp:*",
	"http-get:*:audio/mpeg:*",
	"http-get:*:audio/mp4:*",
	"http-get:*:audio/flac:*",
	"http-get:*:audio/wav:*",
	"http-get:*:image/jpeg:*",
	"http-get:*:image/png:*",
	// 通配兜底：覆盖控制点使用未列出的 content type 的场景。
	"http-get:*:*:*",
}

// ProtocolInfoSink 是逗号分隔的 UPnP Sink 协议列表。
var ProtocolInfoSink = strings.Join(sinkFormats, ",")

// 服务路径与 URN 的映射，供 HTTP 路由与 SSDP 宣告复用。
type serviceInfo struct {
	URN        string
	ID         string
	Path       string
	EventNS    string
	SCPDURL    string
	ControlURL string
	EventURL   string
}

func serviceList() []serviceInfo {
	return []serviceInfo{
		{
			URN: ServiceAVTransport, ID: ServiceIDAVTransport, Path: "AVTransport",
			EventNS: EventNSAVTransport,
			SCPDURL: "/AVTransport/scpd.xml", ControlURL: "/AVTransport/control", EventURL: "/AVTransport/event",
		},
		{
			URN: ServiceRenderingControl, ID: ServiceIDRenderingControl, Path: "RenderingControl",
			EventNS: EventNSRenderingControl,
			SCPDURL: "/RenderingControl/scpd.xml", ControlURL: "/RenderingControl/control", EventURL: "/RenderingControl/event",
		},
		{
			URN: ServiceConnectionManager, ID: ServiceIDConnectionManager, Path: "ConnectionManager",
			EventNS: EventNSConnectionManager,
			SCPDURL: "/ConnectionManager/scpd.xml", ControlURL: "/ConnectionManager/control", EventURL: "/ConnectionManager/event",
		},
	}
}
