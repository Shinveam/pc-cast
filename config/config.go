// Package config 负责 pc-cast 配置的持久化与运行期热更新。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const AppName = "pc-cast"

// Config 的所有字段都会持久化到用户配置目录。
type Config struct {
	FriendlyName string `json:"friendly_name"`
	Port         int    `json:"port"`
	BindIP       string `json:"bind_ip"`
	// PlayerMode: auto / 后端 ID（mpv、vlc、system…）/ custom。
	PlayerMode string `json:"player_mode"`
	// PlayerPath 仅在 PlayerMode == custom 时生效。
	PlayerPath string `json:"player_path"`
	// Deprecated: 旧版 mpv 专属路径，Load 时自动迁移到 PlayerMode/PlayerPath。
	MPVPath    string `json:"mpv_path,omitempty"`
	Fullscreen bool   `json:"fullscreen"`
	AutoPlay   bool   `json:"auto_play"`
	// CastEnabled 是投屏总开关：关闭后不启动 DLNA/SSDP 服务，手机无法发现设备。
	// 默认开启；旧版配置缺少该字段时 Load 会按默认值 true 回写。
	CastEnabled bool `json:"cast_enabled"`
	// WindowSize 是播放窗口预设：auto / 720p / 1080p / 1440p / 4k，默认 1080p。
	WindowSize  string `json:"window_size"`
	OpenAtLogin bool   `json:"open_at_login"`
	LogLevel    string `json:"log_level"`
	DeviceUUID  string `json:"device_uuid"`
}

// WindowSizeOptions 是设置页「播放窗口」下拉的候选项，默认项排在首位。
func WindowSizeOptions() []string {
	return []string{"1080p", "720p", "1440p", "4k", "auto"}
}

// ParseWindowSize 把预设换成像素宽高；(0,0) 表示跟随视频原始尺寸。
func ParseWindowSize(s string) (int, int) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "720p":
		return 1280, 720
	case "1080p":
		return 1920, 1080
	case "1440p":
		return 2560, 1440
	case "4k":
		return 3840, 2160
	default:
		return 0, 0
	}
}

func validWindowSize(s string) bool {
	for _, o := range WindowSizeOptions() {
		if strings.EqualFold(o, strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func Default() Config {
	name := "PC-Cast"
	if host, err := os.Hostname(); err == nil && host != "" {
		name = "PC-Cast (" + host + ")"
	}
	return Config{
		FriendlyName: name,
		Port:         0, // 0 表示由系统分配空闲端口
		BindIP:       "",
		PlayerMode:   "auto", // 按运行环境自动选择播放后端
		PlayerPath:   "",
		Fullscreen:   false,
		AutoPlay:     true,
		CastEnabled:  true,    // 默认开启投屏，右键托盘可随时关闭
		WindowSize:   "1080p", // 默认 1920x1080，避免小窗口缩放导致画面发糊
		OpenAtLogin:  false,
		LogLevel:     "info",
		DeviceUUID:   newUUID(),
	}
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "pc-cast-" + hex.EncodeToString([]byte(fmt.Sprint(os.Getpid())))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[0:8], s[8:12], s[12:16], s[16:20], s[20:32])
}

// Dir 返回配置目录（Windows 为 %AppData%/pc-cast）。
func Dir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, AppName), nil
}

func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

func LogsPath() string {
	d, err := Dir()
	if err != nil {
		return filepath.Join(os.TempDir(), AppName, "pc-cast.log")
	}
	return filepath.Join(d, "pc-cast.log")
}

// Load 读取配置；文件不存在时用默认值创建；内容损坏时返回错误由调用方决定降级。
func Load() (Config, error) {
	c := Default()
	p, err := Path()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		if err := c.Save(); err != nil {
			return c, err
		}
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("解析配置文件 %s 失败: %w", p, err)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	needSave := false
	if _, ok := raw["player_mode"]; !ok {
		// 旧版配置缺少播放器选择字段，回写一次补齐，便于用户直接编辑。
		needSave = true
	}
	if _, ok := raw["window_size"]; !ok {
		// 旧版配置没有窗口尺寸字段，回写默认的 1080p。
		needSave = true
	}
	if _, ok := raw["cast_enabled"]; !ok {
		// 旧版配置没有投屏总开关：c 由 Default() 初始化，值已是 true，
		// 这里只回写字段，方便用户直接查看/编辑配置文件。
		needSave = true
	}
	// 兼容旧版配置：非空 mpv_path 视为「强制使用 mpv + 指定路径」。
	if strings.TrimSpace(c.PlayerMode) == "" {
		c.PlayerMode = "auto"
		needSave = true
	}
	if c.MPVPath != "" {
		if c.PlayerPath == "" {
			c.PlayerPath = c.MPVPath
			c.PlayerMode = "mpv"
		}
		c.MPVPath = ""
		needSave = true
	}
	if strings.TrimSpace(c.WindowSize) == "" {
		c.WindowSize = "1080p"
		needSave = true
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	if c.DeviceUUID == "" {
		c.DeviceUUID = newUUID()
		needSave = true
	}
	if needSave {
		if err := c.Save(); err != nil {
			return c, err
		}
	}
	return c, nil
}

func (c Config) Validate() error {
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("port 超出范围: %d", c.Port)
	}
	if strings.TrimSpace(c.FriendlyName) == "" {
		return errors.New("friendly_name 不能为空")
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("未知的 log_level: %q", c.LogLevel)
	}
	if strings.TrimSpace(c.WindowSize) == "" {
		c.WindowSize = "1080p"
	}
	if !validWindowSize(c.WindowSize) {
		return fmt.Errorf("未知的 window_size: %q", c.WindowSize)
	}
	return nil
}

// Save 原子写入配置文件。
func (c Config) Save() error {
	if err := c.Validate(); err != nil {
		return err
	}
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Store 提供并发安全的运行期配置与变更通知。
type Store struct {
	mu       sync.RWMutex
	cur      Config
	onChange []func(old, next Config)
}

func NewStore(c Config) *Store { return &Store{cur: c} }

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// OnChange 注册变更回调，返回注销函数。
func (s *Store) OnChange(fn func(old, next Config)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = append(s.onChange, fn)
	i := len(s.onChange) - 1
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if i < len(s.onChange) {
			s.onChange[i] = nil
		}
	}
}

// Update 修改配置、持久化并同步通知观察者。持久化失败时不更新内存值。
func (s *Store) Update(mutate func(*Config)) error {
	s.mu.Lock()
	old := s.cur
	next := old
	mutate(&next)
	if err := next.Validate(); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := next.Save(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.cur = next
	listeners := make([]func(old, next Config), len(s.onChange))
	copy(listeners, s.onChange)
	s.mu.Unlock()
	for _, fn := range listeners {
		if fn != nil {
			fn(old, next)
		}
	}
	return nil
}
