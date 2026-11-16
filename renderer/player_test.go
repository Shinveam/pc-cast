package renderer

import (
	"testing"
	"time"
)

func TestParseRelTime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		bad  bool
	}{
		{"00:00:00", 0, false},
		{"00:01:30", 90 * time.Second, false},
		{"01:02:03.500", time.Hour + 2*time.Minute + 3500*time.Millisecond, false},
		{"99:59:59", 99*time.Hour + 59*time.Minute + 59*time.Second, false},
		{"", 0, true},
		{"00:60:00", 0, true},
		{"abc", 0, true},
		{"00:00", 0, true},
		{"NOT_IMPLEMENTED", 0, true},
	}
	for _, c := range cases {
		got, err := ParseRelTime(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("ParseRelTime(%q) 应返回错误", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRelTime(%q) 意外错误: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseRelTime(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFormatRelTime(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "00:00:00"},
		{90 * time.Second, "00:01:30"},
		{time.Hour + 2*time.Minute + 3*time.Second, "01:02:03"},
		{-time.Second, "00:00:00"},
	}
	for _, c := range cases {
		if got := FormatRelTime(c.in); got != c.want {
			t.Errorf("FormatRelTime(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	got := FormatDuration(time.Hour + 2*time.Minute + 3*time.Second + 456*time.Millisecond)
	if got != "01:02:03.456" {
		t.Fatalf("FormatDuration = %q", got)
	}
}

func TestRelTimeRoundTrip(t *testing.T) {
	want := 2*time.Hour + 34*time.Minute + 56*time.Second
	got, err := ParseRelTime(FormatRelTime(want))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip = %v, want %v", got, want)
	}
}
