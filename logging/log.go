// Package logging 提供 pc-cast 统一的 slog JSON 日志。
// 顶级字段固定为 time / level / service / tag / msg，附加字段统一放入 field。
package logging

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	Service    = "pc-cast"
	TimeLayout = "2006-01-02 15:04:05.000"
)

type entry struct {
	Time    string         `json:"time"`
	Level   string         `json:"level"`
	Service string         `json:"service"`
	Tag     string         `json:"tag"`
	Msg     string         `json:"msg"`
	Field   map[string]any `json:"field,omitempty"`
}

// Handler 把 slog.Record 序列化为单行 JSON。
type Handler struct {
	mu    sync.Mutex
	out   io.Writer
	level slog.Level
	attrs []slog.Attr
	tag   string
}

// New 创建 logger。writers 为空时丢弃输出。
func New(level slog.Level, writers ...io.Writer) *slog.Logger {
	var w io.Writer = io.Discard
	if len(writers) == 1 {
		w = writers[0]
	} else if len(writers) > 1 {
		ws := make([]io.Writer, len(writers))
		copy(ws, writers)
		w = io.MultiWriter(ws...)
	}
	return slog.New(&Handler{out: w, level: level})
}

// ParseLevel 解析配置中的日志级别，未知值回退到 INFO。
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func (h *Handler) clone() *Handler {
	c := &Handler{out: h.out, level: h.level, tag: h.tag}
	c.attrs = append([]slog.Attr(nil), h.attrs...)
	return c
}

func (h *Handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := h.clone()
	for _, a := range attrs {
		if a.Key == "tag" {
			c.tag = a.Value.String()
			continue
		}
		c.attrs = append(c.attrs, a)
	}
	return c
}

// WithGroup 保持日志结构扁平，group 名不参与输出。
func (h *Handler) WithGroup(_ string) slog.Handler { return h.clone() }

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	e := entry{
		Time:    r.Time.Format(TimeLayout),
		Level:   r.Level.String(),
		Service: Service,
		Tag:     h.tag,
		Msg:     r.Message,
	}
	if e.Time == "" {
		e.Time = time.Now().Format(TimeLayout)
	}
	fields := make(map[string]any)
	collect := func(a slog.Attr) {
		switch a.Key {
		case "tag":
			e.Tag = a.Value.String()
		case "field":
			if m, ok := a.Value.Any().(map[string]any); ok {
				for k, v := range m {
					fields[k] = v
				}
			}
		default:
			fields[a.Key] = a.Value.Any()
		}
	}
	for _, a := range h.attrs {
		collect(a)
	}
	r.Attrs(func(a slog.Attr) bool { collect(a); return true })
	if len(fields) > 0 {
		e.Field = fields
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err = h.out.Write(append(line, '\n'))
	return err
}

// WithTag 返回带固定 tag 的 logger。
func WithTag(l *slog.Logger, tag string) *slog.Logger {
	return l.With(slog.String("tag", tag))
}

func Debug(l *slog.Logger, tag, msg string, field map[string]any) {
	write(l, slog.LevelDebug, tag, msg, field)
}
func Info(l *slog.Logger, tag, msg string, field map[string]any) {
	write(l, slog.LevelInfo, tag, msg, field)
}
func Warn(l *slog.Logger, tag, msg string, field map[string]any) {
	write(l, slog.LevelWarn, tag, msg, field)
}
func Error(l *slog.Logger, tag, msg string, field map[string]any) {
	write(l, slog.LevelError, tag, msg, field)
}

func write(l *slog.Logger, level slog.Level, tag, msg string, field map[string]any) {
	if l == nil || !l.Enabled(context.Background(), level) {
		return
	}
	attrs := make([]slog.Attr, 0, 2)
	if tag != "" {
		attrs = append(attrs, slog.String("tag", tag))
	}
	if field != nil {
		attrs = append(attrs, slog.Any("field", field))
	}
	l.LogAttrs(context.Background(), level, msg, attrs...)
}
