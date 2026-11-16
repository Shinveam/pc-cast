package assets

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"testing"
)

var pngSig = []byte("\x89PNG\r\n\x1a\n")

func decodeSize(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PNG 解码失败: %v", err)
	}
	if format != "png" {
		t.Fatalf("格式 %q，期望 png", format)
	}
	return cfg.Width, cfg.Height
}

// countColorPixels 统计满足谓词的不透明像素数，用于校验状态语义色。
// RGBA() 返回 16bit/通道，阈值按 0..65535 计。
func countColorPixels(t *testing.T, data []byte, pred func(r, g, b uint32) bool) int {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PNG 解码失败: %v", err)
	}
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a > 0xc000 && pred(r, g, bl) {
				n++
			}
		}
	}
	return n
}

var (
	isGreen = func(r, g, b uint32) bool { return g > r+8000 && g > b+3000 }
	isAmber = func(r, g, b uint32) bool { return r > b+20000 && g > b+10000 }
	// isBlue 覆盖蓝色主底板与「投屏已关闭」的灰蓝底板。
	isBlue = func(r, g, b uint32) bool { return b > r+4000 && b > g+2000 }
	// isChroma 是高饱和判据，用于非关闭态（关闭态按规范为灰阶）。
	isChroma = func(r, g, b uint32) bool {
		lo, hi := r, r
		for _, v := range []uint32{g, b} {
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		return hi-lo > 0x2800
	}
)

// countOpaque 统计不透明像素，用来区分「带底板的彩色图标」与
// 「透明底单色模板」——后者只有符号笔画是不透明的。
func countOpaque(t *testing.T, data []byte) int {
	t.Helper()
	return countColorPixels(t, data, func(uint32, uint32, uint32) bool { return true })
}

func TestValidate(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusImage(t *testing.T) {
	all := map[Status][]byte{
		StatusIdle:     StatusImage(StatusIdle),
		StatusActive:   StatusImage(StatusActive),
		StatusLimited:  StatusImage(StatusLimited),
		StatusDisabled: StatusImage(StatusDisabled),
	}
	seen := map[string]Status{}
	for s, b := range all {
		if w, h := decodeSize(t, b); w != 64 || h != 64 {
			t.Fatalf("状态 %d 图像规格 %dx%d", s, w, h)
		}
		if prev, ok := seen[string(b)]; ok {
			t.Fatalf("状态 %d 与 %d 使用了同一张图", s, prev)
		}
		seen[string(b)] = s
	}
	if !bytes.Equal(StatusImage(Status(200)), all[StatusIdle]) {
		t.Fatal("未知状态应回退到待机图标")
	}
	if countColorPixels(t, all[StatusActive], isGreen) < 20 {
		t.Fatal("投屏中变体缺少绿色角标")
	}
	if countColorPixels(t, all[StatusLimited], isAmber) < 20 {
		t.Fatal("能力受限变体缺少琥珀色角标")
	}
}

// TestTrayStatusIcon 覆盖托盘用 32px 彩色状态变体：规格、四态互异、
// 语义色角标、未知状态回退，以及「必须彩色」这一硬约束（防止再退回
// 透明底单色模板）。
func TestTrayStatusIcon(t *testing.T) {
	seen := map[string]Status{}
	for _, s := range []Status{StatusIdle, StatusActive, StatusLimited, StatusDisabled} {
		b := TrayStatusIcon(s)
		if w, h := decodeSize(t, b); w != 32 || h != 32 {
			t.Fatalf("状态 %d 托盘图规格 %dx%d，期望 32x32", s, w, h)
		}
		if prev, ok := seen[string(b)]; ok {
			t.Fatalf("状态 %d 与 %d 使用了同一张托盘图", s, prev)
		}
		seen[string(b)] = s
		if countOpaque(t, b) < 800 {
			t.Fatalf("状态 %d 托盘图缺少不透明底板，疑似退回单色模板", s)
		}
		if s == StatusDisabled {
			// 规范中「投屏已关闭」是灰阶变体，只要求保留灰蓝底板色调。
			if countColorPixels(t, b, isBlue) < 100 {
				t.Fatalf("状态 %d 托盘图缺少灰蓝底板", s)
			}
		} else if countColorPixels(t, b, isChroma) < 100 {
			t.Fatalf("状态 %d 托盘图不是彩色图标", s)
		}
	}
	active := TrayStatusIcon(StatusActive)
	limited := TrayStatusIcon(StatusLimited)
	if countColorPixels(t, active, isGreen) < 5 {
		t.Fatal("托盘投屏中缺少绿色角标")
	}
	if countColorPixels(t, limited, isAmber) < 5 {
		t.Fatal("托盘能力受限缺少琥珀色角标")
	}
	if !bytes.Equal(TrayStatusIcon(Status(200)), TrayStatusIcon(StatusIdle)) {
		t.Fatal("未知状态应回退到待机托盘图标")
	}
	first := TrayStatusIcon(StatusActive)
	first[0] = 0
	if bytes.Equal(first, TrayStatusIcon(StatusActive)) {
		t.Fatal("返回值必须是副本")
	}
}

func TestAppAndNotifyIcons(t *testing.T) {
	if w, h := decodeSize(t, AppIcon()); w != 256 || h != 256 {
		t.Fatalf("主图标规格 %dx%d", w, h)
	}
	if w, h := decodeSize(t, NotifyImage()); w != 48 || h != 48 {
		t.Fatalf("通知图标规格 %dx%d", w, h)
	}
}

func TestICOFrames(t *testing.T) {
	b := ICO()
	if len(b) < 6 || binary.LittleEndian.Uint16(b[2:4]) != 1 {
		t.Fatal("ICO 头部非法")
	}
	n := int(binary.LittleEndian.Uint16(b[4:6]))
	if n != 6 {
		t.Fatalf("ICO 帧数 %d，期望 6", n)
	}
	want := []int{16, 32, 48, 64, 128, 0} // 0 表示 256
	for i := 0; i < n; i++ {
		p := 6 + 16*i
		if b[p] != byte(want[i]) {
			t.Fatalf("第 %d 帧宽度 %d，期望 %d", i+1, b[p], want[i])
		}
		size := binary.LittleEndian.Uint32(b[p+8 : p+12])
		off := binary.LittleEndian.Uint32(b[p+12 : p+16])
		if off > uint32(len(b)) || size > uint32(len(b))-off {
			t.Fatalf("第 %d 帧越界", i+1)
		}
		if !bytes.HasPrefix(b[off:off+size], pngSig) {
			t.Fatalf("第 %d 帧不是 PNG", i+1)
		}
	}
}

func TestReturnsIndependentCopies(t *testing.T) {
	first := AppIcon()
	first[0] = 0
	if bytes.Equal(first, AppIcon()) {
		t.Fatal("返回值必须是副本，不能暴露内嵌数据本身")
	}
}
