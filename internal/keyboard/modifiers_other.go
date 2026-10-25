//go:build !linux

package keyboard

import "github.com/go-vgo/robotgo"

// releaseModifiers sends key-up for the modifiers so injected characters are
// not combined with the keys the user is holding for the hotkey.
func releaseModifiers() {
	for _, m := range []string{"ctrl", "shift", "alt", "cmd"} {
		_ = robotgo.KeyToggle(m, "up")
	}
}
