// Package keyboard provides OS-level keystroke injection for the dictation mode.
//
// It uses robotgo on supported platforms to simulate keyboard input, injecting
// each rune of the transcribed text as a synthetic key event into the
// currently focused window.
package keyboard

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/go-vgo/robotgo"
	"github.com/tevfik/gleann-sound/internal/core"
)

// ---------------------------------------------------------------------------
// RobotGoInjector
// ---------------------------------------------------------------------------

// RobotGoInjector implements core.KeyboardInjector using the robotgo library
// for cross-platform keystroke simulation.
type RobotGoInjector struct {
	// CharDelay is the pause between each injected character.  A small delay
	// avoids overwhelming the target application's input buffer and ensures
	// that window managers / IMEs process each key event.
	CharDelay time.Duration
}

// Compile-time interface check.
var _ core.KeyboardInjector = (*RobotGoInjector)(nil)

// NewRobotGoInjector creates an injector with sensible defaults.
func NewRobotGoInjector() *RobotGoInjector {
	return &RobotGoInjector{
		CharDelay: 5 * time.Millisecond,
	}
}

// TypeText injects the given UTF-8 string into the active window one character
// at a time.  Special characters (newline, tab) are mapped to their respective
// key names.
//
// This approach is deliberately simple and synchronous — for dictation we
// rarely inject more than a few hundred characters at a time, so the total
// latency is negligible (< 1 s for a full sentence).
// checkDisplay verifies that the X11 display is accessible.
// robotgo uses X11/XTest and will SIGSEGV if XOpenDisplay fails.
func checkDisplay() error {
	if os.Getenv("DISPLAY") == "" {
		return fmt.Errorf("DISPLAY not set — keyboard injection requires an X11 display")
	}
	// Quick probe: run xdpyinfo to verify the connection works.
	out, err := exec.Command("xdpyinfo", "-queryExtensions").CombinedOutput()
	if err != nil {
		hint := ""
		if os.Getenv("XAUTHORITY") == "" {
			hint = " (hint: XAUTHORITY is not set)"
		}
		return fmt.Errorf("cannot connect to X display %q%s: %s",
			os.Getenv("DISPLAY"), hint, strings.TrimSpace(string(out)))
	}
	return nil
}

func (k *RobotGoInjector) TypeText(text string) error {
	if text == "" {
		return nil
	}

	// Pre-flight check: verify X11 display is reachable before calling
	// robotgo's C code, which will SIGSEGV if the display can't be opened.
	if err := checkDisplay(); err != nil {
		return fmt.Errorf("keyboard: %w", err)
	}

	log.Printf("[keyboard] injecting %d chars", len([]rune(text)))

	for _, r := range text {
		var err error

		switch {
		case r == '\n':
			err = robotgo.KeyTap("enter")
		case r == '\t':
			err = robotgo.KeyTap("tab")
		case r == ' ':
			err = robotgo.KeyTap("space")
		case unicode.IsPrint(r):
			// robotgo.TypeStr handles Unicode including shifted characters.
			robotgo.TypeStr(string(r))
		default:
			// Non-printable / control characters — skip silently.
			continue
		}

		if err != nil {
			return fmt.Errorf("keyboard: failed to inject rune %q: %w", r, err)
		}

		if k.CharDelay > 0 {
			time.Sleep(k.CharDelay)
		}
	}

	return nil
}
