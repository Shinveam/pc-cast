package dlna

import (
	"net/http"
	"strconv"
	"strings"

	"pc-cast/logging"
	"pc-cast/renderer"
)

func (s *Server) dispatchAction(w http.ResponseWriter, r *http.Request, urn, action string, args map[string]string) {
	// 写操作与能力查询提升到 INFO：控制点是否真的发起过投屏、是否在推流前
	// 查询过 Sink 能力，是排查「直播推不上 / 清晰度低」的第一手证据。
	// 高频轮询动作（GetPositionInfo、GetTransportInfo 等）仍留在 DEBUG。
	info := map[string]any{"service": urn, "action": action, "remote": r.RemoteAddr}
	switch action {
	case "SetAVTransportURI":
		uri := strings.TrimSpace(args["CurrentURI"])
		meta := ParseDIDL(args["CurrentURIMetaData"], uri)
		live := renderer.IsLive(uri, meta)
		info["uri"] = uri
		info["live"] = live
		info["protocol_info"] = meta.ProtocolInfo
		info["title"] = meta.Title
		logging.Info(s.log, "soap", "收到投屏请求", info)
	case "SetNextAVTransportURI":
		// 部分控制点会先预置下一路直播流；直播与点播一律原样交给播放器，
		// 这里只保留 live 标记用于日志取证，不做任何拦截或提示。
		next := strings.TrimSpace(args["NextURI"])
		live := next != "" && renderer.IsLive(next, renderer.Metadata{})
		info["uri"] = next
		info["live"] = live
		logging.Info(s.log, "soap", "收到控制请求", info)
	case "Play", "Pause", "Stop", "Seek", "SetVolume", "SetMute",
		"GetProtocolInfo", "GetDeviceCapabilities":
		logging.Info(s.log, "soap", "收到控制请求", info)
	default:
		logging.Debug(s.log, "soap", "处理动作", info)
	}

	switch urn {
	case ServiceAVTransport:
		s.avTransportAction(w, action, args)
	case ServiceRenderingControl:
		s.renderingControlAction(w, action, args)
	case ServiceConnectionManager:
		s.connectionManagerAction(w, action, args)
	default:
		writeSOAPFault(w, 401, "Invalid Action")
	}
}

func (s *Server) avTransportAction(w http.ResponseWriter, action string, args map[string]string) {
	if id := args["InstanceID"]; id != "" && id != "0" {
		writeSOAPFault(w, 702, "Invalid InstanceID")
		return
	}
	reply := func(pairs ...[2]string) { writeActionResponse(w, ServiceAVTransport, action, pairs) }

	switch action {
	case "SetAVTransportURI":
		uri := strings.TrimSpace(args["CurrentURI"])
		if uri == "" {
			writeSOAPFault(w, 711, "Invalid URI")
			return
		}
		meta := ParseDIDL(args["CurrentURIMetaData"], uri)
		meta.URI = uri // 控制点的 CurrentURI 优先，DIDL 仅用于补充标题等信息
		meta.Live = renderer.IsLive(uri, meta)
		if err := s.player.SetURI(uri, meta); err != nil {
			logging.Error(s.log, "soap", "播放失败", map[string]any{"uri": uri, "error": err.Error()})
			writeSOAPFault(w, 714, "Illegal MIME-Type or playback error: "+err.Error())
			return
		}
		s.subs.notifyService(ServiceAVTransport, false)
		reply()

	case "SetNextAVTransportURI":
		s.mu.Lock()
		s.nextURI = args["NextURI"]
		s.nextMeta = args["NextURIMetaData"]
		s.mu.Unlock()
		reply()

	case "GetMediaInfo":
		s.mu.RLock()
		nextURI, nextMeta := s.nextURI, s.nextMeta
		s.mu.RUnlock()
		meta := s.player.CurrentMetadata()
		_, duration := s.player.Position()
		if meta.Duration > 0 {
			duration = meta.Duration
		}
		mediaDuration := renderer.FormatRelTime(duration)
		if meta.Live {
			mediaDuration = "00:00:00"
		}
		reply(
			[2]string{"NrTracks", "1"},
			[2]string{"MediaDuration", mediaDuration},
			[2]string{"CurrentURI", s.player.CurrentURI()},
			[2]string{"CurrentURIMetaData", MakeDIDL(meta)},
			[2]string{"NextURI", nextURI},
			[2]string{"NextURIMetaData", nextMeta},
			[2]string{"PlayMedium", "NETWORK"},
			[2]string{"RecordMedium", "NOT_IMPLEMENTED"},
			[2]string{"WriteStatus", "NOT_IMPLEMENTED"},
		)

	case "GetTransportInfo":
		reply(
			[2]string{"CurrentTransportState", string(s.player.State())},
			[2]string{"CurrentTransportStatus", "OK"},
			[2]string{"CurrentSpeed", "1"},
		)

	case "GetPositionInfo":
		pos, duration := s.player.Position()
		meta := s.player.CurrentMetadata()
		if meta.Duration > 0 {
			duration = meta.Duration
		}
		// 直播没有可定位的时长：这里回 00:00:00 而不是 NOT_IMPLEMENTED，
		// 央视频等控制点据此维持投屏会话；回 NOT_IMPLEMENTED 会被当成
		// 「设备不支持」并触发主动 Stop。AbsTime 对网络流无从得知，
		// 仍按规范保留 NOT_IMPLEMENTED。
		if meta.Live {
			reply(
				[2]string{"Track", "1"},
				[2]string{"TrackDuration", "00:00:00"},
				[2]string{"TrackMetaData", MakeDIDL(meta)},
				[2]string{"TrackURI", s.player.CurrentURI()},
				[2]string{"RelTime", "00:00:00"},
				[2]string{"AbsTime", "NOT_IMPLEMENTED"},
				[2]string{"RelCount", "2147483647"},
				[2]string{"AbsCount", "2147483647"},
			)
			return
		}
		reply(
			[2]string{"Track", "1"},
			[2]string{"TrackDuration", renderer.FormatRelTime(duration)},
			[2]string{"TrackMetaData", MakeDIDL(meta)},
			[2]string{"TrackURI", s.player.CurrentURI()},
			[2]string{"RelTime", renderer.FormatRelTime(pos)},
			[2]string{"AbsTime", "NOT_IMPLEMENTED"},
			[2]string{"RelCount", "2147483647"},
			[2]string{"AbsCount", "2147483647"},
		)

	case "GetDeviceCapabilities":
		reply(
			[2]string{"PlayMedia", "NETWORK,HDD"},
			[2]string{"RecMedia", "NOT_IMPLEMENTED"},
			[2]string{"RecQualityModes", "NOT_IMPLEMENTED"},
		)

	case "GetTransportSettings":
		s.mu.RLock()
		mode := s.playMode
		s.mu.RUnlock()
		reply(
			[2]string{"PlayMode", mode},
			[2]string{"RecQualityMode", "NOT_IMPLEMENTED"},
		)

	case "Play":
		if err := s.player.Play(args["Speed"]); err != nil {
			writeSOAPFault(w, 701, err.Error())
			return
		}
		s.subs.notifyService(ServiceAVTransport, false)
		reply()

	case "Pause":
		if err := s.player.Pause(); err != nil {
			writeSOAPFault(w, 701, err.Error())
			return
		}
		s.subs.notifyService(ServiceAVTransport, false)
		reply()

	case "Stop":
		if err := s.player.Stop(); err != nil {
			writeSOAPFault(w, 701, err.Error())
			return
		}
		s.subs.notifyService(ServiceAVTransport, false)
		reply()

	case "Seek":
		// 控制点在直播起播后也会立即发 Seek（实测央视频连发两次）。以前对
		// 直播直接回 710 SOAP 错误，控制点据此判定渲染器异常并主动断开投屏，
		// 表现为「只播几秒」。这里按实测结论直接转发：CapLaunch 后端
		// 没有 seek 参数时只同步内部进度、不重启播放器，对直播无害。
		if err := s.player.Seek(args["Unit"], args["Target"]); err != nil {
			writeSOAPFault(w, 711, err.Error())
			return
		}
		s.subs.notifyService(ServiceAVTransport, false)
		reply()

	case "SetPlayMode":
		mode := args["NewPlayMode"]
		if mode == "" {
			mode = "NORMAL"
		}
		s.mu.Lock()
		s.playMode = mode
		s.mu.Unlock()
		reply()

	case "GetCurrentTransportActions":
		// 直播也保留 Seek：控制点起播后仍会发 Seek，若声明列表里没有它，
		// 部分 SDK 会把后续 Seek 当成非法操作而断开会话。
		reply([2]string{"Actions", "Play,Pause,Stop,Seek,Next,Previous,SetAVTransportURI"})

	case "Next", "Previous":
		// 单文件播放场景下无下一首，返回成功以免部分控制点反复重试。
		reply()

	case "Record", "SetRecordQualityMode":
		writeSOAPFault(w, 701, "Action not implemented")

	default:
		writeSOAPFault(w, 401, "Invalid Action: "+action)
	}
}

func (s *Server) renderingControlAction(w http.ResponseWriter, action string, args map[string]string) {
	reply := func(pairs ...[2]string) { writeActionResponse(w, ServiceRenderingControl, action, pairs) }
	switch action {
	case "GetVolume":
		reply([2]string{"CurrentVolume", strconv.Itoa(s.player.Volume())})
	case "GetVolumeDB":
		// UPnP 官方模板里该动作的输出参数名就是 CurrentVolume（不是
		// CurrentVolumeDB）。音量单位按 1/256 dB 返回一个合法区间内的值。
		reply([2]string{"CurrentVolume", strconv.Itoa((s.player.Volume() - 100) * 256)})
	case "GetVolumeDBRange":
		reply([2]string{"MinValue", "-32767"}, [2]string{"MaxValue", "32767"})
	case "SetVolume":
		v, err := strconv.Atoi(strings.TrimSpace(args["DesiredVolume"]))
		if err != nil {
			writeSOAPFault(w, 402, "Invalid Args")
			return
		}
		if err := s.player.SetVolume(v); err != nil {
			writeSOAPFault(w, 701, err.Error())
			return
		}
		s.subs.notifyService(ServiceRenderingControl, false)
		reply()
	case "GetMute":
		reply([2]string{"CurrentMute", boolTo01(s.player.Mute())})
	case "SetMute":
		v := strings.TrimSpace(strings.ToLower(args["DesiredMute"]))
		on := v == "1" || v == "true" || v == "yes"
		if err := s.player.SetMute(on); err != nil {
			writeSOAPFault(w, 701, err.Error())
			return
		}
		s.subs.notifyService(ServiceRenderingControl, false)
		reply()
	case "ListPresets":
		reply([2]string{"CurrentPresetNameList", "FactoryDefaults"})
	case "SelectPreset":
		reply()
	default:
		writeSOAPFault(w, 401, "Invalid Action: "+action)
	}
}

func (s *Server) connectionManagerAction(w http.ResponseWriter, action string, args map[string]string) {
	reply := func(pairs ...[2]string) { writeActionResponse(w, ServiceConnectionManager, action, pairs) }
	switch action {
	case "GetProtocolInfo":
		reply(
			[2]string{"Source", ""},
			[2]string{"Sink", ProtocolInfoSink},
		)
	case "GetCurrentConnectionIDs":
		reply([2]string{"ConnectionIDs", "0"})
	case "GetCurrentConnectionInfo":
		reply(
			[2]string{"RcsID", "-1"},
			[2]string{"AVTransportID", "0"},
			[2]string{"ProtocolInfo", ""},
			[2]string{"PeerConnectionManager", ""},
			[2]string{"PeerConnectionID", "-1"},
			[2]string{"Direction", "Input"},
			[2]string{"Status", "OK"},
		)
	case "ConnectionComplete":
		reply()
	default:
		writeSOAPFault(w, 401, "Invalid Action: "+action)
	}
}

func boolTo01(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
