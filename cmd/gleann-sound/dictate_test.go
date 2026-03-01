package main

import (
	"testing"

	"github.com/tevfik/gleann-sound/internal/hotkey"
)

// ---------------------------------------------------------------------------
// parseHotkey tests — comprehensive coverage of the hotkey string parser
// ---------------------------------------------------------------------------

func TestParseHotkey_CtrlAltSpace(t *testing.T) {
	mods, key, err := parseHotkey("ctrl+alt+space")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mods) != 2 {
		t.Fatalf("expected 2 modifiers, got %d", len(mods))
	}
	if mods[0] != hotkey.ModCtrl {
		t.Errorf("modifier 0: want ModCtrl, got %v", mods[0])
	}
	if mods[1] != hotkey.ModAlt {
		t.Errorf("modifier 1: want ModAlt, got %v", mods[1])
	}
	if key != hotkey.KeySpace {
		t.Errorf("key: want KeySpace, got %v", key)
	}
}

func TestParseHotkey_CtrlShiftD(t *testing.T) {
	mods, key, err := parseHotkey("ctrl+shift+d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mods) != 2 {
		t.Fatalf("expected 2 modifiers, got %d", len(mods))
	}
	if mods[0] != hotkey.ModCtrl {
		t.Errorf("modifier 0: want ModCtrl, got %v", mods[0])
	}
	if mods[1] != hotkey.ModShift {
		t.Errorf("modifier 1: want ModShift, got %v", mods[1])
	}
	if key != hotkey.KeyD {
		t.Errorf("key: want KeyD, got %v", key)
	}
}

func TestParseHotkey_SingleKey(t *testing.T) {
	mods, key, err := parseHotkey("f1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mods) != 0 {
		t.Errorf("expected 0 modifiers, got %d", len(mods))
	}
	if key != hotkey.KeyF1 {
		t.Errorf("key: want KeyF1, got %v", key)
	}
}

func TestParseHotkey_AllModifiers(t *testing.T) {
	mods, key, err := parseHotkey("ctrl+alt+shift+super+space")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mods) != 4 {
		t.Fatalf("expected 4 modifiers, got %d", len(mods))
	}
	if key != hotkey.KeySpace {
		t.Errorf("key: want KeySpace, got %v", key)
	}
}

func TestParseHotkey_ModifierAliases(t *testing.T) {
	tests := []struct {
		input   string
		wantMod hotkey.Modifier
	}{
		{"control+a", hotkey.ModCtrl},
		{"option+a", hotkey.ModAlt},
		{"win+a", hotkey.ModSuper},
		{"cmd+a", hotkey.ModSuper},
		{"meta+a", hotkey.ModSuper},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			mods, _, err := parseHotkey(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(mods) != 1 || mods[0] != tt.wantMod {
				t.Errorf("modifier: want %v, got %v", tt.wantMod, mods)
			}
		})
	}
}

func TestParseHotkey_SpecialKeys(t *testing.T) {
	tests := []struct {
		input   string
		wantKey hotkey.Key
	}{
		{"space", hotkey.KeySpace},
		{"return", hotkey.KeyReturn},
		{"enter", hotkey.KeyReturn},
		{"escape", hotkey.KeyEscape},
		{"esc", hotkey.KeyEscape},
		{"tab", hotkey.KeyTab},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, key, err := parseHotkey(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if key != tt.wantKey {
				t.Errorf("key: want %v, got %v", tt.wantKey, key)
			}
		})
	}
}

func TestParseHotkey_FunctionKeys(t *testing.T) {
	fkeys := map[string]hotkey.Key{
		"f1": hotkey.KeyF1, "f2": hotkey.KeyF2, "f3": hotkey.KeyF3,
		"f4": hotkey.KeyF4, "f5": hotkey.KeyF5, "f6": hotkey.KeyF6,
		"f7": hotkey.KeyF7, "f8": hotkey.KeyF8, "f9": hotkey.KeyF9,
		"f10": hotkey.KeyF10, "f11": hotkey.KeyF11, "f12": hotkey.KeyF12,
	}

	for name, wantKey := range fkeys {
		t.Run(name, func(t *testing.T) {
			_, key, err := parseHotkey("ctrl+" + name)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if key != wantKey {
				t.Errorf("key: want %v, got %v", wantKey, key)
			}
		})
	}
}

func TestParseHotkey_Letters(t *testing.T) {
	for c := 'a'; c <= 'z'; c++ {
		t.Run(string(c), func(t *testing.T) {
			_, key, err := parseHotkey("ctrl+" + string(c))
			if err != nil {
				t.Fatalf("unexpected error for key %q: %v", string(c), err)
			}
			expected, ok := letterKeys[c]
			if !ok {
				t.Fatalf("missing letter key mapping for %q", string(c))
			}
			if key != expected {
				t.Errorf("key %q: want %v, got %v", string(c), expected, key)
			}
		})
	}
}

func TestParseHotkey_Digits(t *testing.T) {
	for c := '0'; c <= '9'; c++ {
		t.Run(string(c), func(t *testing.T) {
			_, key, err := parseHotkey("alt+" + string(c))
			if err != nil {
				t.Fatalf("unexpected error for key %q: %v", string(c), err)
			}
			expected, ok := digitKeys[c]
			if !ok {
				t.Fatalf("missing digit key mapping for %q", string(c))
			}
			if key != expected {
				t.Errorf("key %q: want %v, got %v", string(c), expected, key)
			}
		})
	}
}

func TestParseHotkey_CaseInsensitive(t *testing.T) {
	mods1, key1, err1 := parseHotkey("CTRL+ALT+SPACE")
	mods2, key2, err2 := parseHotkey("ctrl+alt+space")
	if err1 != nil || err2 != nil {
		t.Fatalf("errors: %v / %v", err1, err2)
	}
	if len(mods1) != len(mods2) {
		t.Error("modifiers should be same regardless of case")
	}
	if key1 != key2 {
		t.Error("keys should be same regardless of case")
	}
}

func TestParseHotkey_WhitespaceHandling(t *testing.T) {
	mods, key, err := parseHotkey("  ctrl + alt + space  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mods) != 2 {
		t.Errorf("expected 2 modifiers, got %d", len(mods))
	}
	if key != hotkey.KeySpace {
		t.Errorf("key: want KeySpace, got %v", key)
	}
}

// ---------------------------------------------------------------------------
// Error cases
// ---------------------------------------------------------------------------

func TestParseHotkey_NoKey(t *testing.T) {
	_, _, err := parseHotkey("ctrl+alt")
	if err == nil {
		t.Error("expected error for hotkey string without a key")
	}
}

func TestParseHotkey_UnsupportedKey(t *testing.T) {
	_, _, err := parseHotkey("ctrl+!")
	if err == nil {
		t.Error("expected error for unsupported key '!'")
	}
}

func TestParseHotkey_UnsupportedComponent(t *testing.T) {
	_, _, err := parseHotkey("ctrl+backspace")
	if err == nil {
		t.Error("expected error for unsupported key 'backspace'")
	}
}

func TestParseHotkey_EmptyString(t *testing.T) {
	_, _, err := parseHotkey("")
	if err == nil {
		t.Error("expected error for empty hotkey string")
	}
}
