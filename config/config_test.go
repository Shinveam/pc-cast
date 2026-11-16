package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
}

func TestDefaultAndValidate(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.DeviceUUID == "" {
		t.Fatal("DeviceUUID 为空")
	}
	c.Port = 70000
	if err := c.Validate(); err == nil {
		t.Error("越界端口应校验失败")
	}
	c = Default()
	c.LogLevel = "verbose"
	if err := c.Validate(); err == nil {
		t.Error("未知日志级别应校验失败")
	}
}

func TestSaveLoad(t *testing.T) {
	isolateConfig(t)
	c := Default()
	c.FriendlyName = "测试设备"
	c.Port = 8899
	c.Fullscreen = true
	c.DeviceUUID = "fixed-uuid"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.FriendlyName != "测试设备" || got.Port != 8899 || !got.Fullscreen || got.DeviceUUID != "fixed-uuid" {
		t.Fatalf("读回的配置不一致: %#v", got)
	}
}

func TestLoadCreatesDefaultWhenMissing(t *testing.T) {
	isolateConfig(t)
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceUUID == "" || got.FriendlyName == "" {
		t.Fatalf("默认配置不完整: %#v", got)
	}
}

func TestStoreUpdateNotifies(t *testing.T) {
	isolateConfig(t)
	st := NewStore(Default())
	called := false
	st.OnChange(func(old, next Config) {
		called = old.Port != next.Port && next.Port == 1234
	})
	if err := st.Update(func(c *Config) { c.Port = 1234 }); err != nil {
		t.Fatal(err)
	}
	if !called || st.Get().Port != 1234 {
		t.Fatalf("变更通知或内存值不正确: %#v", st.Get())
	}
	if err := st.Update(func(c *Config) { c.Port = -1 }); err == nil {
		t.Error("非法值应被拒绝且不写入")
	}
	if st.Get().Port != 1234 {
		t.Fatal("非法变更污染了内存配置")
	}
}

func TestPlayerSelectionDefaults(t *testing.T) {
	c := Default()
	if c.PlayerMode != "auto" || c.PlayerPath != "" || c.MPVPath != "" {
		t.Fatalf("默认应使用自动选择: %#v", c)
	}
}

func TestLegacyMPVPathMigration(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"friendly_name":"旧设备","port":8899,"mpv_path":"D:/tools/mpv.exe","auto_play":true,"log_level":"info","device_uuid":"legacy-uuid"}`
	if err := os.WriteFile(p, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.PlayerMode != "mpv" || got.PlayerPath != "D:/tools/mpv.exe" || got.MPVPath != "" {
		t.Fatalf("旧配置迁移不正确: %#v", got)
	}
	if got.FriendlyName != "旧设备" || got.DeviceUUID != "legacy-uuid" {
		t.Fatalf("迁移不应丢失其它字段: %#v", got)
	}
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if again.PlayerMode != "mpv" || again.PlayerPath != "D:/tools/mpv.exe" {
		t.Fatalf("迁移应幂等: %#v", again)
	}
}

func TestWindowSizeDefaultsAndParsing(t *testing.T) {
	c := Default()
	if c.WindowSize != "1080p" {
		t.Fatalf("播放窗口默认应为 1080p，实际 %q", c.WindowSize)
	}
	if opts := WindowSizeOptions(); opts[0] != "1080p" {
		t.Fatalf("默认项应排在下拉首位: %v", opts)
	}
	cases := map[string][2]int{
		"720p":  {1280, 720},
		"1080p": {1920, 1080},
		"1440p": {2560, 1440},
		"4k":    {3840, 2160},
		"auto":  {0, 0},
	}
	for preset, want := range cases {
		w, h := ParseWindowSize(preset)
		if w != want[0] || h != want[1] {
			t.Errorf("ParseWindowSize(%q) = %dx%d, want %dx%d", preset, w, h, want[0], want[1])
		}
	}
}

func TestWindowSizeValidation(t *testing.T) {
	c := Default()
	c.WindowSize = "8k"
	if err := c.Validate(); err == nil {
		t.Error("未知窗口预设应校验失败")
	}
	c.WindowSize = ""
	if err := c.Validate(); err != nil {
		t.Errorf("空窗口尺寸应视为默认 1080p: %v", err)
	}
}

func TestLegacyConfigGetsWindowSize(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"friendly_name":"旧设备","port":8899,"player_mode":"auto","auto_play":true,"log_level":"info","device_uuid":"legacy-uuid"}`
	if err := os.WriteFile(p, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.WindowSize != "1080p" {
		t.Fatalf("旧配置应获得默认 1080p，实际 %q", got.WindowSize)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"window_size"`) {
		t.Fatalf("旧配置未回写 window_size 字段: %s", b)
	}
}

func TestCastEnabledDefaultAndLegacy(t *testing.T) {
	if !Default().CastEnabled {
		t.Fatal("投屏总开关应默认开启")
	}
	isolateConfig(t)
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"friendly_name":"旧设备","port":8899,"player_mode":"auto","auto_play":true,"log_level":"info","device_uuid":"legacy-uuid"}`
	if err := os.WriteFile(p, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.CastEnabled {
		t.Fatal("旧配置缺少 cast_enabled 时应默认开启投屏")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"cast_enabled": true`) {
		t.Fatalf("旧配置未回写 cast_enabled 字段: %s", b)
	}

	off := `{"friendly_name":"旧设备","port":8899,"player_mode":"auto","auto_play":true,"log_level":"info","device_uuid":"legacy-uuid","cast_enabled":false}`
	if err := os.WriteFile(p, []byte(off), 0o644); err != nil {
		t.Fatal(err)
	}
	disabled, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if disabled.CastEnabled {
		t.Fatal("显式关闭的投屏开关不应被默认值覆盖")
	}
}
