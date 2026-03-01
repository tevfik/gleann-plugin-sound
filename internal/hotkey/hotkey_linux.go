//go:build linux

package hotkey

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ---------------------------------------------------------------------------
// Linux evdev constants (from linux/input-event-codes.h)
// ---------------------------------------------------------------------------

const (
	evKey          = 0x01 // EV_KEY event type
	inputEventSize = 24   // sizeof(struct input_event) on 64-bit Linux
)

const (
	keyPress   int32 = 1
	keyRelease int32 = 0
	// keyRepeat  int32 = 2  // ignored
)

// Evdev key codes.
const (
	evKeyEsc        uint16 = 1
	evKey1          uint16 = 2
	evKey2          uint16 = 3
	evKey3          uint16 = 4
	evKey4          uint16 = 5
	evKey5          uint16 = 6
	evKey6          uint16 = 7
	evKey7          uint16 = 8
	evKey8          uint16 = 9
	evKey9          uint16 = 10
	evKey0          uint16 = 11
	evKeyTab        uint16 = 15
	evKeyQ          uint16 = 16
	evKeyW          uint16 = 17
	evKeyE          uint16 = 18
	evKeyR          uint16 = 19
	evKeyT          uint16 = 20
	evKeyY          uint16 = 21
	evKeyU          uint16 = 22
	evKeyI          uint16 = 23
	evKeyO          uint16 = 24
	evKeyP          uint16 = 25
	evKeyEnter      uint16 = 28
	evKeyLeftCtrl   uint16 = 29
	evKeyA          uint16 = 30
	evKeyS          uint16 = 31
	evKeyD          uint16 = 32
	evKeyF          uint16 = 33
	evKeyG          uint16 = 34
	evKeyH          uint16 = 35
	evKeyJ          uint16 = 36
	evKeyK          uint16 = 37
	evKeyL          uint16 = 38
	evKeyLeftShift  uint16 = 42
	evKeyZ          uint16 = 44
	evKeyX          uint16 = 45
	evKeyC          uint16 = 46
	evKeyV          uint16 = 47
	evKeyB          uint16 = 48
	evKeyN          uint16 = 49
	evKeyM          uint16 = 50
	evKeyLeftAlt    uint16 = 56
	evKeySpace      uint16 = 57
	evKeyRightShift uint16 = 54
	evKeyF1         uint16 = 59
	evKeyF2         uint16 = 60
	evKeyF3         uint16 = 61
	evKeyF4         uint16 = 62
	evKeyF5         uint16 = 63
	evKeyF6         uint16 = 64
	evKeyF7         uint16 = 65
	evKeyF8         uint16 = 66
	evKeyF9         uint16 = 67
	evKeyF10        uint16 = 68
	evKeyF11        uint16 = 87
	evKeyF12        uint16 = 88
	evKeyRightCtrl  uint16 = 97
	evKeyRightAlt   uint16 = 100
	evKeyLeftMeta   uint16 = 125
	evKeyRightMeta  uint16 = 126
)

// keyToEvdev maps abstract Key constants to evdev key codes.
var keyToEvdev = map[Key]uint16{
	KeySpace:  evKeySpace,
	KeyReturn: evKeyEnter,
	KeyEscape: evKeyEsc,
	KeyTab:    evKeyTab,
	KeyA:      evKeyA, KeyB: evKeyB, KeyC: evKeyC, KeyD: evKeyD,
	KeyE: evKeyE, KeyF: evKeyF, KeyG: evKeyG, KeyH: evKeyH,
	KeyI: evKeyI, KeyJ: evKeyJ, KeyK: evKeyK, KeyL: evKeyL,
	KeyM: evKeyM, KeyN: evKeyN, KeyO: evKeyO, KeyP: evKeyP,
	KeyQ: evKeyQ, KeyR: evKeyR, KeyS: evKeyS, KeyT: evKeyT,
	KeyU: evKeyU, KeyV: evKeyV, KeyW: evKeyW, KeyX: evKeyX,
	KeyY: evKeyY, KeyZ: evKeyZ,
	Key0: evKey0, Key1: evKey1, Key2: evKey2, Key3: evKey3,
	Key4: evKey4, Key5: evKey5, Key6: evKey6, Key7: evKey7,
	Key8: evKey8, Key9: evKey9,
	KeyF1: evKeyF1, KeyF2: evKeyF2, KeyF3: evKeyF3, KeyF4: evKeyF4,
	KeyF5: evKeyF5, KeyF6: evKeyF6, KeyF7: evKeyF7, KeyF8: evKeyF8,
	KeyF9: evKeyF9, KeyF10: evKeyF10, KeyF11: evKeyF11, KeyF12: evKeyF12,
}

// modEvdevCodes maps Modifier to the left+right evdev key codes.
var modEvdevCodes = map[Modifier][2]uint16{
	ModCtrl:  {evKeyLeftCtrl, evKeyRightCtrl},
	ModShift: {evKeyLeftShift, evKeyRightShift},
	ModAlt:   {evKeyLeftAlt, evKeyRightAlt},
	ModSuper: {evKeyLeftMeta, evKeyRightMeta},
}

// ---------------------------------------------------------------------------
// Platform data
// ---------------------------------------------------------------------------

type linuxPlatform struct {
	files []*os.File
}

// ---------------------------------------------------------------------------
// Register / Unregister
// ---------------------------------------------------------------------------

// Register opens keyboard device(s) via evdev and starts listening for the
// configured key combination.  Works on both X11 and Wayland (and even on
// a plain Linux console) because it reads directly from the kernel input
// subsystem.
//
// Requirements:
//   - The user must have read access to /dev/input/event* (add yourself to
//     the "input" group: sudo usermod -aG input $USER, then re-login).
func (hk *Hotkey) Register() error {
	evdevCode, ok := keyToEvdev[hk.key]
	if !ok {
		return fmt.Errorf("hotkey: unsupported key %d", hk.key)
	}

	kbds := findKeyboards()
	if len(kbds) == 0 {
		return fmt.Errorf("hotkey: no keyboard devices found in /dev/input — " +
			"make sure you are in the 'input' group (sudo usermod -aG input $USER) " +
			"and re-login")
	}

	var files []*os.File
	for _, path := range kbds {
		f, err := os.Open(path)
		if err != nil {
			log.Printf("[hotkey] skip %s: %v", path, err)
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return fmt.Errorf("hotkey: cannot open any keyboard device — " +
			"make sure you are in the 'input' group (sudo usermod -aG input $USER)")
	}

	hk.platform = &linuxPlatform{files: files}

	log.Printf("[hotkey] monitoring %d keyboard device(s) via evdev (works on X11 + Wayland)", len(files))
	for _, f := range files {
		log.Printf("[hotkey]   → %s", f.Name())
	}

	// Channel for raw evdev events from all devices.
	eventCh := make(chan evdevEvent, 128)

	// Start a reader goroutine for each keyboard device.
	var wg sync.WaitGroup
	for _, f := range files {
		wg.Add(1)
		go func(f *os.File) {
			defer wg.Done()
			readEvdevEvents(f, eventCh, hk.stopCh)
		}(f)
	}

	// Close eventCh when all readers finish.
	go func() {
		wg.Wait()
		close(eventCh)
	}()

	// Process events in the background.
	go func() {
		defer close(hk.done)
		hk.processEvents(eventCh, evdevCode)
	}()

	return nil
}

// Unregister stops the event loop and closes all device files.
func (hk *Hotkey) Unregister() error {
	select {
	case <-hk.stopCh:
		return nil // already stopped
	default:
	}

	close(hk.stopCh)

	// Close device files to unblock any blocked Read() calls.
	if p, ok := hk.platform.(*linuxPlatform); ok {
		for _, f := range p.files {
			f.Close()
		}
	}

	<-hk.done
	log.Println("[hotkey] unregistered")
	return nil
}

// ---------------------------------------------------------------------------
// evdev event reading
// ---------------------------------------------------------------------------

type evdevEvent struct {
	code  uint16
	value int32 // 0=release, 1=press, 2=repeat
}

// readEvdevEvents reads struct input_event from the device file and sends
// EV_KEY events to the channel.  Returns when stopCh is closed or the file
// is closed.
func readEvdevEvents(f *os.File, ch chan<- evdevEvent, stopCh <-chan struct{}) {
	buf := make([]byte, inputEventSize*16)
	for {
		// Use a read deadline so we can check stopCh periodically.
		_ = f.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, err := f.Read(buf)

		// Check for shutdown.
		select {
		case <-stopCh:
			return
		default:
		}

		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue // timeout → loop back and check stopCh
			}
			if err == io.EOF || isClosedErr(err) {
				return
			}
			continue // transient error
		}

		for off := 0; off+inputEventSize <= n; off += inputEventSize {
			evType := binary.LittleEndian.Uint16(buf[off+16 : off+18])
			if evType != evKey {
				continue
			}
			code := binary.LittleEndian.Uint16(buf[off+18 : off+20])
			value := int32(binary.LittleEndian.Uint32(buf[off+20 : off+24]))

			select {
			case ch <- evdevEvent{code: code, value: value}:
			case <-stopCh:
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Event processing — modifier tracking + combo detection
// ---------------------------------------------------------------------------

// processEvents tracks modifier key state and fires keydownCh/keyupCh when
// the target key combination is detected.
func (hk *Hotkey) processEvents(ch <-chan evdevEvent, targetCode uint16) {
	type modInfo struct {
		mod   Modifier
		left  uint16
		right uint16
	}
	var wantMods []modInfo
	for _, m := range hk.mods {
		codes := modEvdevCodes[m]
		wantMods = append(wantMods, modInfo{mod: m, left: codes[0], right: codes[1]})
	}

	pressed := make(map[uint16]bool) // tracks pressed state of keys
	keyHeld := false

	log.Printf("[hotkey] processEvents: targetCode=%d, wantMods=%d modifier(s)", targetCode, len(wantMods))
	for i, mi := range wantMods {
		log.Printf("[hotkey]   mod[%d]: left=%d right=%d", i, mi.left, mi.right)
	}

	for {
		select {
		case <-hk.stopCh:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}

			isPress := ev.value == keyPress
			isRel := ev.value == keyRelease
			if !isPress && !isRel {
				continue // ignore repeat events
			}

			// Update pressed state.
			if isPress {
				pressed[ev.code] = true
			} else {
				delete(pressed, ev.code)
			}

			// Check if this is our target key.
			if ev.code == targetCode {
				log.Printf("[hotkey] TARGET key code=%d press=%v release=%v keyHeld=%v pressed=%v",
					ev.code, isPress, isRel, keyHeld, pressed)
				if isPress && !keyHeld {
					// Verify all required modifiers are held.
					allMods := true
					for _, mi := range wantMods {
						if !pressed[mi.left] && !pressed[mi.right] {
							allMods = false
							break
						}
					}
					if allMods {
						keyHeld = true
						select {
						case hk.keydownCh <- struct{}{}:
						default:
						}
					}
				} else if isRel && keyHeld {
					keyHeld = false
					select {
					case hk.keyupCh <- struct{}{}:
					default:
					}
				}
			}

			// If a modifier is released while the key is held, fire keyup.
			if isRel && keyHeld {
				for _, mi := range wantMods {
					if ev.code == mi.left || ev.code == mi.right {
						if !pressed[mi.left] && !pressed[mi.right] {
							keyHeld = false
							select {
							case hk.keyupCh <- struct{}{}:
							default:
							}
							break
						}
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Keyboard device discovery
// ---------------------------------------------------------------------------

// findKeyboards discovers keyboard input devices under /dev/input.
func findKeyboards() []string {
	var paths []string

	// Method 1: by-path symlinks (created by udev, most reliable).
	if matches, _ := filepath.Glob("/dev/input/by-path/*-event-kbd"); len(matches) > 0 {
		paths = resolveAndDedup(matches)
	}

	// Method 2: by-id symlinks.
	if len(paths) == 0 {
		if matches, _ := filepath.Glob("/dev/input/by-id/*-event-kbd"); len(matches) > 0 {
			paths = resolveAndDedup(matches)
		}
	}

	// Method 3: all event devices, filtered by capability.
	if len(paths) == 0 {
		if matches, _ := filepath.Glob("/dev/input/event*"); len(matches) > 0 {
			for _, m := range matches {
				if isKeyboardDevice(m) {
					paths = append(paths, m)
				}
			}
		}
	}

	sort.Strings(paths)
	return paths
}

func resolveAndDedup(links []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, link := range links {
		target, err := filepath.EvalSymlinks(link)
		if err != nil {
			continue
		}
		if !seen[target] {
			seen[target] = true
			result = append(result, target)
		}
	}
	return result
}

// isKeyboardDevice checks if the given /dev/input/event* device is a
// keyboard by querying its evdev capability bits via ioctl.
func isKeyboardDevice(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	// EVIOCGBIT(0, 4) — get supported event types.
	evBits := make([]byte, 4)
	if ioctlGetBits(f.Fd(), 0, evBits) != nil {
		return false
	}

	// Check EV_KEY (bit 1).
	if evBits[0]&(1<<evKey) == 0 {
		return false
	}

	// EVIOCGBIT(EV_KEY, 96) — get supported key codes.
	keyBits := make([]byte, 96)
	if ioctlGetBits(f.Fd(), evKey, keyBits) != nil {
		return false
	}

	// A real keyboard should have KEY_A (30).
	return keyBits[evKeyA/8]&(1<<(evKeyA%8)) != 0
}

// ioctlGetBits issues an EVIOCGBIT(ev, len) ioctl on fd.
func ioctlGetBits(fd uintptr, ev uint16, buf []byte) error {
	// _IOC(_IOC_READ, 'E', 0x20+ev, len(buf))
	const (
		iocRead     = 2
		iocDirShift = 30
		iocTypShift = 8
		iocSzShift  = 16
	)
	req := uintptr(iocRead<<iocDirShift) |
		uintptr('E')<<iocTypShift |
		uintptr(0x20+uint32(ev)) |
		uintptr(len(buf))<<iocSzShift

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req,
		uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return errno
	}
	return nil
}

func isClosedErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "file already closed") ||
		strings.Contains(s, "bad file descriptor")
}
