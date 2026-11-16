package trayui

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"pc-cast/config"
	"pc-cast/dlna"
	"pc-cast/renderer"
)

// 临时冒烟：PC_CAST_QUIT_SMOKE=1 时验证「设置窗口存在 → 托盘退出」链路。
func TestMain(m *testing.M) {
	if os.Getenv("PC_CAST_QUIT_SMOKE") == "" {
		os.Exit(m.Run())
	}
	runtime.LockOSThread()
	fmt.Println("SMOKE_BEGIN")
	st := config.NewStore(config.Default())
	u := New(Deps{
		Config:     st,
		Server:     func() *dlna.Server { return nil },
		Player:     func() renderer.Player { return nil },
		PlayerDesc: func() renderer.Descriptor { return renderer.Descriptor{} },
		InfoLines:  func() [][2]string { return nil },
		InfoText:   func() string { return "" },
		Quit: func() {
			fmt.Println("SMOKE_QUIT_HANDLER")
			go func() {
				time.Sleep(200 * time.Millisecond)
				mygo.App.Quit()
			}()
		},
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { u.BeginQuit() })
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { fmt.Println("SMOKE_WILL_QUIT") })
	mygo.App.OnQuitCanceled(func() { fmt.Println("SMOKE_QUIT_CANCELED") })
	mygo.App.WhenReady(func() {
		if err := u.Ready(); err != nil {
			fmt.Println("SMOKE_READY_ERR", err)
		}
		u.openSettings()
		if u.settings.win != nil {
			u.settings.win.Hide()
		}
		fmt.Println("SMOKE_WINDOW_HIDDEN")
		time.AfterFunc(600*time.Millisecond, func() { u.requestQuit() })
	})
	err := mygo.App.Run()
	fmt.Println("SMOKE_RUN_RETURNED", err)
	os.Exit(0)
}
