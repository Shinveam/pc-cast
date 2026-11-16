package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"regexp"
	"testing"
)

func TestHandlerFixedFields(t *testing.T) {
	var buf bytes.Buffer
	l := New(slog.LevelDebug, &buf)
	Info(l, "ssdp", "hello", map[string]any{"ip": "192.168.1.2"})
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"time", "level", "service", "tag", "msg", "field"} {
		if _, ok := m[k]; !ok {
			t.Errorf("缺少顶级字段 %q: %s", k, buf.String())
		}
	}
	if m["service"] != Service || m["tag"] != "ssdp" || m["msg"] != "hello" || m["level"] != "INFO" {
		t.Errorf("字段值不正确: %#v", m)
	}
	field, ok := m["field"].(map[string]any)
	if !ok || field["ip"] != "192.168.1.2" {
		t.Errorf("field 不正确: %#v", m["field"])
	}
	ts, _ := m["time"].(string)
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}$`).MatchString(ts) {
		t.Errorf("时间格式不正确: %q", ts)
	}
}

func TestOptionalFieldOmitted(t *testing.T) {
	var buf bytes.Buffer
	l := New(slog.LevelInfo, &buf)
	Warn(l, "app", "no field", nil)
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["field"]; ok {
		t.Errorf("空 field 应被省略: %s", buf.String())
	}
}

func TestLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l := New(slog.LevelWarn, &buf)
	Info(l, "x", "filtered", nil)
	if buf.Len() != 0 {
		t.Fatal("低于阈值的日志不应输出")
	}
	Error(l, "x", "kept", nil)
	if buf.Len() == 0 {
		t.Fatal("达到阈值的日志应输出")
	}
}

func TestWithTag(t *testing.T) {
	var buf bytes.Buffer
	l := WithTag(New(slog.LevelInfo, &buf), "mpv")
	Info(l, "", "implicit", nil)
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["tag"] != "mpv" {
		t.Errorf("WithTag 未生效: %#v", m)
	}
	buf.Reset()
	Info(l, "ssdp", "explicit", nil)
	m = nil
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["tag"] != "ssdp" {
		t.Errorf("显式 tag 应覆盖 WithTag: %#v", m)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo,
		"WARN": slog.LevelWarn, "error": slog.LevelError, "unknown": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
