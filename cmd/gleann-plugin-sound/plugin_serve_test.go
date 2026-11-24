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
