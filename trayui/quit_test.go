package trayui

import (
	"testing"
	"time"
)

// 退出流程不能在构造 UI 时触碰任何 mygo 原生对象，以下用例可在无事件循环下运行。

func TestBeginQuitIsIdempotent(t *testing.T) {
	u := New(Deps{})
	if u.IsQuitting() {
		t.Fatal("新建 UI 不应处于退出状态")
	}
	u.BeginQuit()
	u.BeginQuit()
	if !u.IsQuitting() {
		t.Fatal("BeginQuit 未置位退出标志")
	}
}

func TestRequestQuitDelegatesAndMarksQuitting(t *testing.T) {
	calls := 0
	u := New(Deps{Quit: func() { calls++ }})
	u.requestQuit()
	if calls != 1 {
		t.Fatalf("注入的退出处理器应被调用一次，实际 %d", calls)
	}
	if !u.IsQuitting() {
		t.Fatal("退出前必须放行窗口关闭")
	}
}

func TestRefreshStatusSkipsAfterQuit(t *testing.T) {
	// Player getter 为 nil：若退出后仍继续刷新状态会直接 panic。
	u := New(Deps{})
	u.BeginQuit()
	u.refreshStatus()
}

func TestCloseNeverBlocks(t *testing.T) {
	u := New(Deps{})
	u.wg.Add(1) // 模拟状态刷新协程卡在 onMain 同步调用上
	start := time.Now()
	u.Close()
	u.Close() // 幂等：不能重复 close(stopCh)
	u.wg.Done()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Close 被后台协程拖住 %s，退出会卡死", elapsed)
	}
}
