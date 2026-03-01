// Package hotkey provides cross-platform global hotkey detection.
//
// On Linux, hotkeys are detected via evdev (/dev/input), which works on
// both X11 and Wayland without any display-server dependency.  The user
// must have read access to /dev/input/event* devices (typically by being
// a member of the "input" group).
//
// On macOS and Windows, hotkeys are handled via golang.design/x/hotkey.
package hotkey

// Modifier represents a modifier key (Ctrl, Shift, Alt, Super).
// Values are abstract and mapped to platform-specific codes internally.
type Modifier uint32

const (
	ModCtrl  Modifier = 1 << iota // Ctrl / Control
	ModShift                      // Shift
	ModAlt                        // Alt / Option
	ModSuper                      // Super / Win / Cmd
)

// Key represents a keyboard key.
// Values are abstract and mapped to platform-specific scancodes internally.
type Key uint32

const (
	KeySpace  Key = iota + 1
	KeyReturn     // Enter / Return
	KeyEscape
	KeyTab

	KeyA
	KeyB
	KeyC
	KeyD
	KeyE
	KeyF
	KeyG
	KeyH
	KeyI
	KeyJ
	KeyK
	KeyL
	KeyM
	KeyN
	KeyO
	KeyP
	KeyQ
	KeyR
	KeyS
	KeyT
	KeyU
	KeyV
	KeyW
	KeyX
	KeyY
	KeyZ

	Key0
	Key1
	Key2
	Key3
	Key4
	Key5
	Key6
	Key7
	Key8
	Key9

	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
)

// Hotkey manages a global hotkey registration.
type Hotkey struct {
	mods []Modifier
	key  Key

	keydownCh chan struct{}
	keyupCh   chan struct{}
	stopCh    chan struct{}
	done      chan struct{}

	Verbose bool // enable debug logging

	// Platform-specific data set by Register().
	platform interface{}
}

// New creates a new Hotkey with the given modifiers and key.
// Call Register() to start listening for the hotkey.
func New(mods []Modifier, key Key) *Hotkey {
	return &Hotkey{
		mods:      mods,
		key:       key,
		keydownCh: make(chan struct{}, 1),
		keyupCh:   make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Keydown returns a channel that receives when the hotkey is pressed.
func (hk *Hotkey) Keydown() <-chan struct{} {
	return hk.keydownCh
}

// Keyup returns a channel that receives when the hotkey is released.
func (hk *Hotkey) Keyup() <-chan struct{} {
	return hk.keyupCh
}
