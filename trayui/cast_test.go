package trayui

import (
	"io"
	"log/slog"
	"testing"

	"pc-cast/config"
	"pc-cast/logging"
)

func castTestUI(t *testing.T) *UI {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	st := config.NewStore(config.Default())
	return New(Deps{
		Log:    logging.New(slog.LevelError, io.Discard),
		Config: st,
	})
}

func TestToggleCastEnabled(t *testing.T) {
	u := castTestUI(t)
	st := u.d.Config
	if !st.Get().CastEnabled {
		t.Fatal("投屏开关应默认开启")
	}
	u.toggle("cast", false)
	if st.Get().CastEnabled {
		t.Fatal("关闭投屏后配置未生效")
	}
	u.toggle("cast", true)
	if !st.Get().CastEnabled {
		t.Fatal("重新开启投屏后配置未生效")
	}
}

// 总开关关闭时播放器/DLNA 服务已被释放，refreshStatus 必须提前返回，
// 不得再调用 Player getter（此处故意留空，调用会 panic）。
func TestRefreshStatusWhenCastDisabled(t *testing.T) {
	u := castTestUI(t)
	if err := u.d.Config.Update(func(c *config.Config) { c.CastEnabled = false }); err != nil {
		t.Fatal(err)
	}
	u.refreshStatus()
}
