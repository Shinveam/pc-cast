package dlna

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

type descriptionData struct {
	URLBase      string
	FriendlyName string
	UDN          string
	Services     []serviceInfo
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return s
	}
	return b.String()
}

// buildDescription 生成 UPnP 设备描述文档。
//
// 服务 URL 全部写成完整绝对 URL（以 URLBase 为前缀），而不是相对路径：
// 部分 Android 投屏 SDK 用 <URLBase> 解析，另一些忽略 URLBase 直接用
// SSDP LOCATION 解析，还有少数自研 HTTP 客户端会把 Host 头里的地址当
// base。绝对 URL 在三种解析方式下都指向同一个实际监听端口，避免控制
// 点把 SOAP / GENA 请求发到错误端口（表现为「搜得到、点不动」）。
func buildDescription(d descriptionData) string {
	base := ""
	if d.URLBase != "" {
		base = strings.TrimSuffix(d.URLBase, "/") + "/"
	}
	serviceURL := func(ref string) string {
		if base == "" || strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
			return ref
		}
		return base + strings.TrimPrefix(ref, "/")
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<root xmlns="` + DeviceNS + `" xmlns:dlna="urn:schemas-dlna-org:device-1-0">`)
	b.WriteString(`<specVersion><major>1</major><minor>0</minor></specVersion>`)
	b.WriteString("<URLBase>" + xmlEscape(d.URLBase) + "</URLBase>")
	b.WriteString("<device>")
	b.WriteString("<deviceType>" + DeviceTypeMediaRenderer + "</deviceType>")
	b.WriteString("<friendlyName>" + xmlEscape(d.FriendlyName) + "</friendlyName>")
	b.WriteString("<manufacturer>PC-Cast</manufacturer>")
	// UPnP 规范中这两个 URL 字段可选，但部分投屏 SDK 会把它们当作
	// 「完整设备描述」的必要条件，缺失时静默放弃设备；这里指向接收端
	// 自身地址，不引用任何第三方项目地址。
	selfURL := base
	if selfURL == "" {
		selfURL = "http://localhost/"
	}
	b.WriteString("<manufacturerURL>" + xmlEscape(selfURL) + "</manufacturerURL>")
	b.WriteString("<modelDescription>AVTransport Media Renderer</modelDescription>")
	b.WriteString("<modelName>PC-Cast</modelName>")
	b.WriteString("<modelNumber>1</modelNumber>")
	b.WriteString("<modelURL>" + xmlEscape(selfURL) + "</modelURL>")
	b.WriteString("<serialNumber>1</serialNumber>")
	b.WriteString("<UDN>" + xmlEscape(d.UDN) + "</UDN>")
	b.WriteString(`<dlna:X_DLNADOC>DMR-1.50</dlna:X_DLNADOC>`)
	b.WriteString("<serviceList>")
	for _, s := range d.Services {
		b.WriteString("<service>")
		b.WriteString("<serviceType>" + s.URN + "</serviceType>")
		b.WriteString("<serviceId>" + s.ID + "</serviceId>")
		b.WriteString("<SCPDURL>" + xmlEscape(serviceURL(s.SCPDURL)) + "</SCPDURL>")
		b.WriteString("<controlURL>" + xmlEscape(serviceURL(s.ControlURL)) + "</controlURL>")
		b.WriteString("<eventSubURL>" + xmlEscape(serviceURL(s.EventURL)) + "</eventSubURL>")
		b.WriteString("</service>")
	}
	b.WriteString("</serviceList>")
	b.WriteString("</device></root>")
	return b.String()
}

type actionDef struct {
	Name string
	In   []string
	Out  []string
}

type variableDef struct {
	Name          string
	Type          string
	AllowedValues []string
	SendEvents    string // "yes" / "no"
}

var scpdDefinitions = map[string]struct {
	Actions   []actionDef
	Variables []variableDef
}{
	ServiceAVTransport: {
		Actions: []actionDef{
			{"SetAVTransportURI", []string{"InstanceID", "CurrentURI", "CurrentURIMetaData"}, nil},
			{"SetNextAVTransportURI", []string{"InstanceID", "NextURI", "NextURIMetaData"}, nil},
			{"GetMediaInfo", []string{"InstanceID"}, []string{"NrTracks", "MediaDuration", "CurrentURI", "CurrentURIMetaData", "NextURI", "NextURIMetaData", "PlayMedium", "RecordMedium", "WriteStatus"}},
			{"GetTransportInfo", []string{"InstanceID"}, []string{"CurrentTransportState", "CurrentTransportStatus", "CurrentSpeed"}},
			{"GetPositionInfo", []string{"InstanceID"}, []string{"Track", "TrackDuration", "TrackMetaData", "TrackURI", "RelTime", "AbsTime", "RelCount", "AbsCount"}},
			{"GetDeviceCapabilities", []string{"InstanceID"}, []string{"PlayMedia", "RecMedia", "RecQualityModes"}},
			{"GetTransportSettings", []string{"InstanceID"}, []string{"PlayMode", "RecQualityMode"}},
			{"Stop", []string{"InstanceID"}, nil},
			{"Play", []string{"InstanceID", "Speed"}, nil},
			{"Pause", []string{"InstanceID"}, nil},
			{"Seek", []string{"InstanceID", "Unit", "Target"}, nil},
			{"Next", []string{"InstanceID"}, nil},
			{"Previous", []string{"InstanceID"}, nil},
			{"SetPlayMode", []string{"InstanceID", "NewPlayMode"}, nil},
			{"GetCurrentTransportActions", []string{"InstanceID"}, []string{"Actions"}},
		},
		Variables: []variableDef{
			{"TransportState", "string", []string{"STOPPED", "PLAYING", "TRANSITIONING", "PAUSED_PLAYBACK", "NO_MEDIA_PRESENT"}, "yes"},
			{"TransportStatus", "string", []string{"OK", "ERROR_OCCURRED"}, "yes"},
			{"CurrentSpeed", "string", nil, "yes"},
			{"CurrentURI", "string", nil, "yes"},
			{"CurrentURIMetaData", "string", nil, "yes"},
			{"NumberOfTracks", "ui4", nil, "yes"},
			{"CurrentTrack", "ui4", nil, "yes"},
			{"CurrentTrackDuration", "string", nil, "yes"},
			{"CurrentTrackMetaData", "string", nil, "yes"},
			{"CurrentTrackURI", "string", nil, "yes"},
			{"RelativeTimePosition", "string", nil, "yes"},
			{"AbsoluteTimePosition", "string", nil, "yes"},
			{"PlaybackStorageMedium", "string", nil, "yes"},
			{"CurrentPlayMode", "string", nil, "no"},
			{"TransportPlaySpeed", "string", nil, "no"},
			{"LastChange", "string", nil, "yes"},
			// UPnP AVTransport:1 模板要求的 A_ARG_TYPE_*、计数与录制类变量。
			// 每个 <argument> 的 <relatedStateVariable> 都必须指向这里声明的变量：
			// 悬空引用会让 Cling / CyberGarage 等严格 DLNA SDK 判定服务描述无效，
			// 表现为「拉完 SCPD 就再也不发 SOAP 请求」。
			{"A_ARG_TYPE_InstanceID", "ui4", nil, "no"},
			{"A_ARG_TYPE_SeekMode", "string", []string{"TRACK_NR", "ABS_TIME", "REL_TIME", "ABS_COUNT", "REL_COUNT", "CHANNEL_FREQ", "TAPE-INDEX", "FRAME"}, "no"},
			{"A_ARG_TYPE_SeekTarget", "string", nil, "no"},
			{"A_ARG_TYPE_TransportURI", "string", nil, "no"},
			{"A_ARG_TYPE_TransportURIMetaData", "string", nil, "no"},
			{"A_ARG_TYPE_NextTransportURI", "string", nil, "no"},
			{"A_ARG_TYPE_NextTransportURIMetaData", "string", nil, "no"},
			{"AVTransportURI", "string", nil, "yes"},
			{"AVTransportURIMetaData", "string", nil, "yes"},
			{"NextAVTransportURI", "string", nil, "yes"},
			{"NextAVTransportURIMetaData", "string", nil, "yes"},
			{"CurrentMediaDuration", "string", nil, "yes"},
			{"RelativeCounterPosition", "i4", nil, "yes"},
			{"AbsoluteCounterPosition", "i4", nil, "yes"},
			{"CurrentTransportActions", "string", nil, "yes"},
			{"RecordStorageMedium", "string", nil, "yes"},
			{"PossibleRecordStorageMedia", "string", nil, "no"},
			{"PossiblePlaybackStorageMedia", "string", nil, "no"},
			{"RecordMediumWriteStatus", "string", []string{"WRITABLE", "PROTECTED", "NOT_WRITABLE", "UNKNOWN", "NOT_IMPLEMENTED"}, "yes"},
			{"CurrentRecordQualityMode", "string", nil, "no"},
			{"PossibleRecordQualityModes", "string", nil, "no"},
		},
	},
	ServiceRenderingControl: {
		Actions: []actionDef{
			{"ListPresets", []string{"InstanceID"}, []string{"CurrentPresetNameList"}},
			{"SelectPreset", []string{"InstanceID", "PresetName"}, nil},
			{"GetVolume", []string{"InstanceID", "Channel"}, []string{"CurrentVolume"}},
			{"SetVolume", []string{"InstanceID", "Channel", "DesiredVolume"}, nil},
			{"GetMute", []string{"InstanceID", "Channel"}, []string{"CurrentMute"}},
			{"SetMute", []string{"InstanceID", "Channel", "DesiredMute"}, nil},
		},
		Variables: []variableDef{
			{"Volume", "ui2", nil, "yes"},
			{"Mute", "boolean", nil, "yes"},
			{"LastChange", "string", nil, "yes"},
			{"PresetNameList", "string", nil, "no"},
			{"A_ARG_TYPE_InstanceID", "ui4", nil, "no"},
			{"A_ARG_TYPE_Channel", "string", []string{"Master", "LF", "RF", "CF", "LFE", "LS", "RS", "LFC", "RFC", "SD", "SL", "SR", "W", "T", "B"}, "no"},
			{"A_ARG_TYPE_PresetName", "string", []string{"FactoryDefaults"}, "no"},
			{"VolumeDB", "i2", nil, "yes"},
		},
	},
	ServiceConnectionManager: {
		Actions: []actionDef{
			{"GetProtocolInfo", nil, []string{"Source", "Sink"}},
			{"GetCurrentConnectionIDs", nil, []string{"ConnectionIDs"}},
			{"GetCurrentConnectionInfo", []string{"ConnectionID"}, []string{"RcsID", "AVTransportID", "ProtocolInfo", "PeerConnectionManager", "PeerConnectionID", "Direction", "Status"}},
			{"ConnectionComplete", []string{"ConnectionID"}, nil},
		},
		Variables: []variableDef{
			{"SourceProtocolInfo", "string", nil, "yes"},
			{"SinkProtocolInfo", "string", nil, "yes"},
			{"CurrentConnectionIDs", "string", nil, "yes"},
			{"A_ARG_TYPE_ConnectionStatus", "string", []string{"OK", "ContentFormatMismatch", "InsufficientBandwidth", "UnreliableChannel", "Unknown"}, "no"},
			{"A_ARG_TYPE_ConnectionManager", "string", nil, "no"},
			{"A_ARG_TYPE_Direction", "string", []string{"Input", "Output"}, "no"},
			{"A_ARG_TYPE_ConnectionID", "i4", nil, "no"},
			{"A_ARG_TYPE_RcsID", "i4", nil, "no"},
			{"A_ARG_TYPE_AVTransportID", "i4", nil, "no"},
			{"A_ARG_TYPE_ProtocolInfo", "string", nil, "no"},
		},
	},
}

// buildSCPD 生成服务描述（SCPD）文档。
func buildSCPD(urn string) string {
	// 优先返回 UPnP 官方静态模板；静态原文字段完整、格式稳定，可避免
	// 动态拼装的精简描述被严格 DLNA SDK 判定为无效服务。
	if body := officialSCPD(urn); body != "" {
		return body
	}
	def, ok := scpdDefinitions[urn]
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<scpd xmlns="` + ServiceNS + `"><specVersion><major>1</major><minor>0</minor></specVersion>`)
	b.WriteString("<actionList>")
	for _, a := range def.Actions {
		b.WriteString("<action><name>" + a.Name + "</name><argumentList>")
		for _, arg := range a.In {
			b.WriteString(fmt.Sprintf(`<argument><name>%s</name><direction>in</direction><relatedStateVariable>%s</relatedStateVariable></argument>`, arg, relatedVariable(arg)))
		}
		for _, arg := range a.Out {
			b.WriteString(fmt.Sprintf(`<argument><name>%s</name><direction>out</direction><relatedStateVariable>%s</relatedStateVariable></argument>`, arg, relatedVariable(arg)))
		}
		b.WriteString("</argumentList></action>")
	}
	b.WriteString("</actionList><serviceStateTable>")
	for _, v := range def.Variables {
		b.WriteString(`<stateVariable sendEvents="` + v.SendEvents + `"><name>` + v.Name + `</name><dataType>` + v.Type + `</dataType>`)
		if len(v.AllowedValues) > 0 {
			b.WriteString("<allowedValueList>")
			for _, av := range v.AllowedValues {
				b.WriteString("<allowedValue>" + av + "</allowedValue>")
			}
			b.WriteString("</allowedValueList>")
		}
		b.WriteString("</stateVariable>")
	}
	b.WriteString("</serviceStateTable></scpd>")
	return b.String()
}

// relatedVariable 把 SOAP 参数映射到 SCPD serviceStateTable 中真实存在的变量。
// 这里只允许返回 scpdDefinitions 里声明过的名字；scpd_test.go 会逐个校验，
// 任何悬空引用都会导致严格 DLNA SDK 拒绝整个服务描述。
func relatedVariable(arg string) string {
	known := map[string]string{
		"InstanceID": "A_ARG_TYPE_InstanceID", "CurrentURI": "AVTransportURI", "CurrentURIMetaData": "AVTransportURIMetaData",
		"NextURI": "NextAVTransportURI", "NextURIMetaData": "NextAVTransportURIMetaData", "Speed": "TransportPlaySpeed",
		"Unit": "A_ARG_TYPE_SeekMode", "Target": "A_ARG_TYPE_SeekTarget", "NewPlayMode": "CurrentPlayMode",
		"CurrentTransportState": "TransportState", "CurrentTransportStatus": "TransportStatus", "CurrentSpeed": "CurrentSpeed",
		"NrTracks": "NumberOfTracks", "MediaDuration": "CurrentMediaDuration", "PlayMedium": "PlaybackStorageMedium",
		"RecordMedium": "RecordStorageMedium", "WriteStatus": "RecordMediumWriteStatus",
		"Track": "CurrentTrack", "TrackDuration": "CurrentTrackDuration", "TrackMetaData": "CurrentTrackMetaData",
		"TrackURI": "CurrentTrackURI", "RelTime": "RelativeTimePosition", "AbsTime": "AbsoluteTimePosition",
		"RelCount": "RelativeCounterPosition", "AbsCount": "AbsoluteCounterPosition",
		"PlayMode": "CurrentPlayMode", "RecQualityMode": "CurrentRecordQualityMode", "Actions": "CurrentTransportActions",
		"PlayMedia": "PossiblePlaybackStorageMedia", "RecMedia": "PossibleRecordStorageMedia", "RecQualityModes": "PossibleRecordQualityModes",
		"CurrentPresetNameList": "PresetNameList", "PresetName": "A_ARG_TYPE_PresetName",
		"Channel": "A_ARG_TYPE_Channel", "DesiredVolume": "Volume", "CurrentVolume": "Volume",
		"DesiredMute": "Mute", "CurrentMute": "Mute",
		"Source": "SourceProtocolInfo", "Sink": "SinkProtocolInfo", "ConnectionIDs": "CurrentConnectionIDs",
		"ConnectionID": "A_ARG_TYPE_ConnectionID", "RcsID": "A_ARG_TYPE_RcsID", "AVTransportID": "A_ARG_TYPE_AVTransportID",
		"ProtocolInfo": "A_ARG_TYPE_ProtocolInfo", "PeerConnectionManager": "A_ARG_TYPE_ConnectionManager",
		"PeerConnectionID": "A_ARG_TYPE_ConnectionID", "Direction": "A_ARG_TYPE_Direction", "Status": "A_ARG_TYPE_ConnectionStatus",
	}
	if v, ok := known[arg]; ok {
		return v
	}
	return "A_ARG_TYPE_" + arg
}
