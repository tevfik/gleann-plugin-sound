package keyboard

import (
	"testing"
	"time"

	"github.com/tevfik/gleann-sound/internal/core"
)

// ---------------------------------------------------------------------------
// RobotGoInjector tests
// ---------------------------------------------------------------------------
//
// NOTE: Actual keystroke injection requires an active X11/Wayland/Windows
// display session.  These tests verify the API contract and skip functional
// testing when no display is available.

func TestNewRobotGoInjector(t *testing.T) {
	inj := NewRobotGoInjector()
	if inj == nil {
		t.Fatal("NewRobotGoInjector returned nil")
	}
	if inj.CharDelay != 5*time.Millisecond {
		t.Errorf("default CharDelay: want 5ms, got %v", inj.CharDelay)
	}
}

func TestRobotGoInjector_ImplementsInterface(t *testing.T) {
	var _ core.KeyboardInjector = (*RobotGoInjector)(nil)
}

func TestRobotGoInjector_EmptyString(t *testing.T) {
	inj := NewRobotGoInjector()
	err := inj.TypeText("")
	if err != nil {
		t.Errorf("TypeText with empty string should be a no-op, got: %v", err)
	}
}

func TestRobotGoInjector_CharDelayCustom(t *testing.T) {
	inj := NewRobotGoInjector()
	inj.CharDelay = 10 * time.Millisecond
	if inj.CharDelay != 10*time.Millisecond {
		t.Errorf("CharDelay not set: want 10ms, got %v", inj.CharDelay)
	}
}
