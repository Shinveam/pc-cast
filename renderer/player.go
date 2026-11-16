// Package renderer 定义 DLNA 层使用的播放器抽象，并按运行环境自动选择可用后端。
package renderer

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ipcSeq 让每次启动/重启 mpv 都使用新的 IPC 端点名，
// 避免服务重启时旧命名管道或 socket 文件尚未释放导致新实例连接失败。
var ipcSeq atomic.Uint64

func nextIPCToken() uint64 { return ipcSeq.Add(1) }

type State string

const (
	StateStopped       State = "STOPPED"
	StatePlaying       State = "PLAYING"
	StatePaused        State = "PAUSED_PLAYBACK"
	StateTransitioning State = "TRANSITIONING"
	StateNoMedia       State = "NO_MEDIA_PRESENT"
)

// Metadata 是从 DIDL-Lite / 请求参数中得到的媒体信息。
type Metadata struct {
	URI          string
	Title        string
	ProtocolInfo string
	RawXML       string
	Duration     time.Duration
	// Live 表示直播流：没有可定位的时长，进度查询回 00:00:00、
	// Seek 作为 no-op 成功返回（避免控制点收到错误后断开投屏）。
	Live bool
}

// liveProtocolMarks 是 protocolInfo 里的直播强证据：只要声明了这些 MIME，
// 即使 URI 长得像普通文件，也按直播处理。
var liveProtocolMarks = []string{
	"mpegurl", "m3u8", "video/mp2t", "mpeg-tts",
	"x-flv", "video/flv", "video/f4v",
}

// vodExtensions 是明确的点播文件后缀；命中即视为点播反证。
var vodExtensions = []string{
	".mp4", ".m4v", ".mkv", ".avi", ".mov", ".wmv", ".webm",
	".mp3", ".flac", ".m4a", ".aac", ".jpg", ".jpeg", ".png",
}

// liveURIMarks 覆盖国内直播 CDN 的常见路径/参数写法。关键字可能出现在
// path，也可能藏在 query 里的 url= 参数中，因此同时检查完整 URL 与 path。
var liveURIMarks = []string{
	"/live/", "/live-", "/live.", "live-bvc", "live_", "bvc=live",
	"live=1", "is_live", "stream_type=live", "hlslive", "pull-flv",
	"/hls/", "rtmp://", "rtsp://", "rtspt://",
}

// IsLive 综合 URI 与 DIDL 元数据判断是否为直播。
//
// 判定顺序「强证据 → 点播反证 → URI 特征」：meta.Live / protocolInfo 由
// DIDL 解析给出，最可信；其次用明确的点播后缀与 VOD 参数排除误判（B 站
// 点播 URL 带大量签名参数，宽松的关键字匹配很容易误伤）；最后识别 HLS、
// HTTP-FLV、MPEG-TS、DASH 及 live-bvc/live_ 等直播流特征。
func IsLive(uri string, meta Metadata) bool {
	if meta.Live {
		return true
	}
	p := strings.ToLower(meta.ProtocolInfo)
	for _, mark := range liveProtocolMarks {
		if strings.Contains(p, mark) {
			return true
		}
	}
	lower := strings.ToLower(strings.TrimSpace(uri))
	path := lower
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if isVODURI(lower, path, meta) {
		return false
	}
	if hasLiveURIMark(lower, path) {
		return true
	}
	// 未知容器但 DIDL 明确声明不可 seek、且没有时长：按直播处理。
	return meta.Duration == 0 && strings.Contains(p, "dlna.org_op=00")
}

func isVODURI(lower, path string, meta Metadata) bool {
	for _, ext := range vodExtensions {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return strings.Contains(lower, "bvc=vod") ||
		strings.Contains(path, "/vod/") || meta.Duration > 0
}

func hasLiveURIMark(lower, path string) bool {
	for _, ext := range []string{".m3u8", ".m3u", ".mpd", ".flv", ".f4v"} {
		if strings.Contains(lower, ext) {
			return true
		}
	}
	if strings.HasSuffix(path, ".ts") {
		return true
	}
	for _, mark := range liveURIMarks {
		if strings.Contains(lower, mark) {
			return true
		}
	}
	return false
}

// WindowSizer 是可选接口，用于运行期调整播放窗口尺寸。
// 刻意不并入 Player 接口：dlna 层与既有测试 mock 无需为此改动。
type WindowSizer interface {
	ApplyWindowSize(width, height int)
}

// Player 由 dlna 包消费；renderer 包负责实现，避免反向依赖。
type Player interface {
	Start() error
	Close() error
	Path() string
	Version() string

	SetURI(uri string, meta Metadata) error
	Play(speed string) error
	Pause() error
	Stop() error
	Seek(unit, target string) error
	SetVolume(volume int) error
	Volume() int
	SetMute(on bool) error
	Mute() bool

	State() State
	Position() (current, duration time.Duration)
	CurrentURI() string
	CurrentMetadata() Metadata

	// SetChangeHook 注册播放状态变化回调，用于触发 GENA 事件推送。
	SetChangeHook(hook func())
	// ApplyRuntimeOptions 让设置窗口的修改立即生效（路径变更需由上层重启）。
	ApplyRuntimeOptions(fullscreen, autoPlay bool)
}

// ParseRelTime 解析 DLNA 的 REL_TIME / ABS_TIME（HH:MM:SS[.fff]）。
func ParseRelTime(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "NOT_IMPLEMENTED" {
		return 0, fmt.Errorf("无效的时间值 %q", s)
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("无效的时间格式 %q", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("无效的小时 %q: %w", parts[0], err)
	}
	min, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("无效的分钟 %q: %w", parts[1], err)
	}
	sec, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return 0, fmt.Errorf("无效的秒 %q: %w", parts[2], err)
	}
	if h < 0 || min < 0 || min > 59 || sec < 0 || sec >= 60 {
		return 0, fmt.Errorf("时间越界 %q", s)
	}
	return time.Duration((float64(h)*3600 + float64(min)*60 + sec) * float64(time.Second)), nil
}

// FormatRelTime 把时长格式化为 UPnP 使用的 HH:MM:SS。
func FormatRelTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int64(d / time.Second)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

// FormatDuration 输出 mpv 事件需要的带毫秒时间串。
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := d / time.Millisecond
	h := total / 3600000
	m := (total % 3600000) / 60000
	s := (total % 60000) / 1000
	ms := total % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}
