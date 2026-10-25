package keyboard

import (
	"os"
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
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

func TestSanitizeText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "merhaba dünya", "merhaba dünya"},
		{"turkish", "çğıöşü ÇĞİÖŞÜ", "çğıöşü ÇĞİÖŞÜ"},
		{"nbsp becomes space", "bir\u00a0iki", "bir iki"},
		{"narrow nbsp", "10\u202f%", "10 %"},
		{"thin space", "a\u2009b", "a b"},
		{"zero width removed", "a\u200bb\u200dc\ufeffd", "abcd"},
		{"soft hyphen and bidi", "a\u00adb\u200ec", "abc"},
		{"invalid utf8", "a\xffb\xc3", "ab"},
		{"replacement char", "a�b", "ab"},
		{"control chars", "a\x00b\x07c\x1bd", "abcd"},
		{"newline and tab kept", "a\n\tb", "a\n\tb"},
		{"crlf", "a\r\nb", "a\nb"},
		{"lone cr", "a\rb", "a\nb"},
		{"curly quotes kept", "“x” — y…", "“x” — y…"},
	}
	for _, c := range cases {
		if got := SanitizeText(c.in); got != c.want {
			t.Errorf("%s: SanitizeText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestSplitRuns(t *testing.T) {
	got := splitRuns("çok iyi\ngün")
	want := []string{"çok", " ", "iyi", "\n", "gün"}
	if len(got) != len(want) {
		t.Fatalf("splitRuns = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitRuns = %q, want %q", got, want)
		}
	}
	if r := splitRuns(""); len(r) != 0 {
		t.Errorf("splitRuns(\"\") = %q, want empty", r)
	}
}

func TestRobotGoInjector_OnlyDroppedChars(t *testing.T) {
	// Text that sanitises to nothing must return before touching the display.
	if err := NewRobotGoInjector().TypeText("\u200b\ufeff\x00"); err != nil {
		t.Errorf("TypeText of only-removable text should be a no-op, got: %v", err)
	}
}

func TestFlattenWhitespace(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\nb", "a b"},
		{"a \n b", "a  b"}, // real spaces are kept; only the control adds none
		{"a\t\tb", "a b"},
		{"a\r\nb", "a b"},
		{" lead", " lead"},
		{"x\n", "x "},
		{"no controls", "no controls"},
	}
	for _, c := range cases {
		if got := FlattenWhitespace(c.in); got != c.want {
			t.Errorf("FlattenWhitespace(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNewRobotGoInjector_NoControlKeysByDefault(t *testing.T) {
	if NewRobotGoInjector().KeepControlKeys {
		t.Error("dictation must not type Enter/Tab by default")
	}
}

// Sending key-up for the modifiers must not kill the process on any keyboard
// layout (a keycode-0 key-up used to abort with an XTEST BadValue error).
func TestReleaseModifiersSurvives(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("needs an X display")
	}
	for i := 0; i < 3; i++ {
		releaseModifiers()
	}
}
