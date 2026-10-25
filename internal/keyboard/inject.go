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

	"github.com/go-vgo/robotgo"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
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

	// KeepControlKeys types newline and tab as Enter and Tab. Off by default:
	// dictated text must never press Enter in the focused terminal.
	KeepControlKeys bool
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

	clean := SanitizeText(text)
	if !k.KeepControlKeys {
		clean = FlattenWhitespace(clean)
	}
	if dropped := len([]rune(text)) - len([]rune(clean)); dropped > 0 {
		log.Printf("[keyboard] sanitised input: %d character(s) removed or replaced", dropped)
	}
	if clean == "" {
		return nil
	}

	// Pre-flight check: verify X11 display is reachable before calling
	// robotgo's C code, which will SIGSEGV if the display can't be opened.
	if err := checkDisplay(); err != nil {
		return fmt.Errorf("keyboard: %w", err)
	}

	log.Printf("[keyboard] injecting %d chars", len([]rune(clean)))

	// The push-to-talk hotkey is usually still held while words are typed.
	// Typed with Ctrl+Shift down, every letter becomes a terminal shortcut
	// (Ctrl+Shift+W closes the tab, Ctrl+Shift+Q the window), so release the
	// modifiers first. The physical release later is harmless.
	releaseModifiers()

	// Type each word in a single robotgo call: typing rune by rune with a
	// 5 ms gap raced with X11 keymap remapping for characters (ç ğ ı ş …)
	// that have no key of their own, producing wrong or missing letters.
	for _, run := range splitRuns(clean) {
		var err error

		switch run {
		case "\n":
			err = robotgo.KeyTap("enter")
		case "\t":
			err = robotgo.KeyTap("tab")
		case " ":
			err = robotgo.KeyTap("space")
		default:
			robotgo.TypeStr(run)
			if !isASCII(run) {
				// let the target application finish the keymap remap
				time.Sleep(10 * time.Millisecond)
			}
		}

		if err != nil {
			return fmt.Errorf("keyboard: failed to inject %q: %w", run, err)
		}

		if k.CharDelay > 0 {
			time.Sleep(k.CharDelay)
		}
	}

	return nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
