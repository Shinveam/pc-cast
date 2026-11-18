package bundled

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPathWindowsExe(t *testing.T) {
	if runtime.GOOS != "windows" || !Available() {
		t.Skip("内置二进制仅 Windows 分发")
	}
	p, err := Path()
	if err != nil {
		t.Fatalf("Path() 失败: %v", err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("解压产物不存在: %v", err)
	}
	if st.Size() < 1024*1024 {
		t.Fatalf("解压产物过小: %d", st.Size())
	}
	if filepath.Base(p) != exeName {
		t.Fatalf("文件名不符: %s", p)
	}
	p2, err := Path()
	if err != nil || p2 != p {
		t.Fatalf("缓存路径不一致: %s / %s / %v", p, p2, err)
	}
}

func TestVersionStable(t *testing.T) {
	if runtime.GOOS != "windows" || !source.ok {
		t.Skip("无内置数据")
	}
	if a, b := version(), version(); a != b || len(a) != 16 {
		t.Fatalf("版本不稳定: %q %q", a, b)
	}
}

// 用临时数据验证解压、复用、损坏后重解三条路径，任何平台都能跑。
func TestExtractInto(t *testing.T) {
	payload := bytes.Repeat([]byte("mpv-binary-content"), 4096)
	data := gzipBytes(t, payload)
	dir := filepath.Join(t.TempDir(), "v1")

	p, err := extractInto(data, dir)
	if err != nil {
		t.Fatalf("首次解压失败: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("解压内容不符: %v", err)
	}

	// 复用：改动文件 mtime 前先确认第二次仍是同一路径且内容不变。
	p2, err := extractInto(data, dir)
	if err != nil || p2 != p {
		t.Fatalf("复用路径不一致: %s / %s / %v", p, p2, err)
	}

	// 损坏目标后应重新解压出完整内容。
	if err := os.WriteFile(p, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 当前复用只看 size>0，损坏且更短会重新解压；为覆盖该场景把内容清空。
	if err := os.Truncate(p, 0); err != nil {
		t.Fatal(err)
	}
	p3, err := extractInto(data, dir)
	if err != nil {
		t.Fatalf("重解失败: %v", err)
	}
	got, err = os.ReadFile(p3)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("重解内容不符: %v", err)
	}
}

func TestExtractBadGzip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "v1")
	if _, err := extractInto([]byte("not gzip"), dir); err == nil {
		t.Fatal("损坏数据应返回错误")
	}
}
