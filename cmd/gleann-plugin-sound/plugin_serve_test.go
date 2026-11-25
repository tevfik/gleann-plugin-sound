package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseMaxDurationNamesItsSource(t *testing.T) {
	if _, err := parseMaxDuration("abc", "GLEANN_SOUND_MAX_DURATION"); err == nil || !strings.Contains(err.Error(), "GLEANN_SOUND_MAX_DURATION") {
		t.Errorf("env error = %v", err)
	}
	if _, err := parseMaxDuration("-5m", "--max-duration"); err == nil || !strings.Contains(err.Error(), "--max-duration") {
		t.Errorf("flag error = %v", err)
	}
	if d, err := parseMaxDuration("90m", "--max-duration"); err != nil || d != 90*time.Minute {
		t.Errorf("90m = %v %v", d, err)
	}
}

// S3 shutdown parity: the drain must fit inside the stop grace gleann gives
// the plugin (SIGTERM, kill after GLEANN_PLUGIN_STOP_GRACE), not the old
// 20 s drain / 25 s hard-exit that outlived the host's 10 s patience.
func TestStopGraceDefaultsToHostValue(t *testing.T) {
	if defaultStopGrace != 10*time.Second {
		t.Errorf("defaultStopGrace = %v, want 10s (gleann plugin.go kill grace)", defaultStopGrace)
	}
}

func TestShutdownDeadlinesTrackGrace(t *testing.T) {
	for _, c := range []struct {
		grace           time.Duration
		drain, hardExit time.Duration
	}{
		{10 * time.Second, 9 * time.Second, 10 * time.Second}, // host default
		{30 * time.Second, 29 * time.Second, 30 * time.Second},
		{2 * time.Second, 1 * time.Second, 2 * time.Second},
		{1 * time.Second, 500 * time.Millisecond, 1 * time.Second}, // short grace: drain is half of it
		{500 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond},
		{0, 9 * time.Second, 10 * time.Second}, // unset/zero → host default
	} {
		drain, hard := shutdownDeadlines(c.grace)
		if drain != c.drain || hard != c.hardExit {
			t.Errorf("grace %v: drain=%v hard=%v, want %v/%v", c.grace, drain, hard, c.drain, c.hardExit)
		}
	}
}

func TestStopGraceEnv(t *testing.T) {
	for _, c := range []struct {
		env  string
		want time.Duration
	}{
		{"", 10 * time.Second},
		{"10s", 10 * time.Second},
		{"30s", 30 * time.Second},
		{"45", 10 * time.Second},          // no unit: the host ignores it, so do we
		{"500ms", 500 * time.Millisecond}, // any positive duration, like the host
		{"1ms", time.Millisecond},
		{" 30s", 10 * time.Second}, // the host does not trim
		{"0", 10 * time.Second},    // non-positive falls back
		{"-5s", 10 * time.Second},  // negative falls back
		{"nonsense", 10 * time.Second},
	} {
		if got := stopGraceFromEnv(c.env); got != c.want {
			t.Errorf("GLEANN_PLUGIN_STOP_GRACE=%q → %v, want %v", c.env, got, c.want)
		}
	}
}
