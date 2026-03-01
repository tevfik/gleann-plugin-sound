//go:build linux

package hotkey

import (
	"testing"
	"time"
)

func TestKeyToEvdevMapping(t *testing.T) {
	// Every Key constant must have an evdev mapping.
	keys := []Key{
		KeySpace, KeyReturn, KeyEscape, KeyTab,
		KeyA, KeyB, KeyC, KeyD, KeyE, KeyF, KeyG, KeyH, KeyI, KeyJ, KeyK, KeyL, KeyM,
		KeyN, KeyO, KeyP, KeyQ, KeyR, KeyS, KeyT, KeyU, KeyV, KeyW, KeyX, KeyY, KeyZ,
		Key0, Key1, Key2, Key3, Key4, Key5, Key6, Key7, Key8, Key9,
		KeyF1, KeyF2, KeyF3, KeyF4, KeyF5, KeyF6, KeyF7, KeyF8, KeyF9, KeyF10, KeyF11, KeyF12,
	}
	for _, k := range keys {
		if _, ok := keyToEvdev[k]; !ok {
			t.Errorf("Key %d has no evdev mapping", k)
		}
	}
}

func TestModEvdevCodes(t *testing.T) {
	mods := []Modifier{ModCtrl, ModShift, ModAlt, ModSuper}
	for _, m := range mods {
		codes, ok := modEvdevCodes[m]
		if !ok {
			t.Errorf("Modifier %d has no evdev codes", m)
			continue
		}
		if codes[0] == 0 || codes[1] == 0 {
			t.Errorf("Modifier %d has zero evdev codes: %v", m, codes)
		}
		if codes[0] == codes[1] {
			t.Errorf("Modifier %d has identical left/right codes: %v", m, codes)
		}
	}
}

func TestFindKeyboards(t *testing.T) {
	// This may fail in CI without /dev/input, but should work on real hardware.
	paths := findKeyboards()
	t.Logf("found %d keyboard device(s): %v", len(paths), paths)
	// Don't fail — CI environments may not have /dev/input.
}

func TestProcessEvents(t *testing.T) {
	hk := New([]Modifier{ModCtrl, ModShift}, KeySpace)

	evdevCode := keyToEvdev[KeySpace]
	eventCh := make(chan evdevEvent, 64)

	go func() {
		defer close(hk.done)
		hk.processEvents(eventCh, evdevCode)
	}()

	// Simulate: press LeftCtrl, press LeftShift, press Space
	eventCh <- evdevEvent{code: evKeyLeftCtrl, value: keyPress}
	eventCh <- evdevEvent{code: evKeyLeftShift, value: keyPress}
	eventCh <- evdevEvent{code: evKeySpace, value: keyPress}

	// Should get keydown.
	select {
	case <-hk.Keydown():
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("expected keydown event")
	}

	// Simulate: release Space
	eventCh <- evdevEvent{code: evKeySpace, value: keyRelease}

	// Should get keyup.
	select {
	case <-hk.Keyup():
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("expected keyup event")
	}

	// Shutdown.
	close(hk.stopCh)
	<-hk.done
}

func TestProcessEventsModRelease(t *testing.T) {
	hk := New([]Modifier{ModCtrl}, KeyA)

	evdevCode := keyToEvdev[KeyA]
	eventCh := make(chan evdevEvent, 64)

	go func() {
		defer close(hk.done)
		hk.processEvents(eventCh, evdevCode)
	}()

	// Press Ctrl+A → keydown.
	eventCh <- evdevEvent{code: evKeyLeftCtrl, value: keyPress}
	eventCh <- evdevEvent{code: evKeyA, value: keyPress}

	select {
	case <-hk.Keydown():
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("expected keydown event")
	}

	// Release Ctrl while A is still held → should fire keyup.
	eventCh <- evdevEvent{code: evKeyLeftCtrl, value: keyRelease}

	select {
	case <-hk.Keyup():
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("expected keyup when modifier released")
	}

	close(hk.stopCh)
	<-hk.done
}

func TestProcessEventsNoModifier(t *testing.T) {
	hk := New([]Modifier{ModCtrl}, KeySpace)

	evdevCode := keyToEvdev[KeySpace]
	eventCh := make(chan evdevEvent, 64)

	go func() {
		defer close(hk.done)
		hk.processEvents(eventCh, evdevCode)
	}()

	// Press Space without Ctrl → should NOT fire keydown.
	eventCh <- evdevEvent{code: evKeySpace, value: keyPress}

	// Give processor time to handle the event.
	time.Sleep(50 * time.Millisecond)

	select {
	case <-hk.Keydown():
		t.Fatal("should not fire keydown without modifier")
	default:
		// OK — no event expected.
	}

	eventCh <- evdevEvent{code: evKeySpace, value: keyRelease}

	close(hk.stopCh)
	<-hk.done
}

func TestProcessEventsRepeatIgnored(t *testing.T) {
	hk := New([]Modifier{ModCtrl}, KeyA)

	evdevCode := keyToEvdev[KeyA]
	eventCh := make(chan evdevEvent, 64)

	go func() {
		defer close(hk.done)
		hk.processEvents(eventCh, evdevCode)
	}()

	// Press Ctrl+A → keydown.
	eventCh <- evdevEvent{code: evKeyLeftCtrl, value: keyPress}
	eventCh <- evdevEvent{code: evKeyA, value: keyPress}

	select {
	case <-hk.Keydown():
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("expected keydown")
	}

	// Send repeat events (value=2) — should NOT fire additional keydown.
	eventCh <- evdevEvent{code: evKeyA, value: 2} // repeat

	// Give processor time to handle the repeat.
	time.Sleep(50 * time.Millisecond)

	select {
	case <-hk.Keydown():
		t.Fatal("repeat should not fire additional keydown")
	default:
		// OK
	}

	// Release.
	eventCh <- evdevEvent{code: evKeyA, value: keyRelease}

	select {
	case <-hk.Keyup():
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("expected keyup")
	}

	close(hk.stopCh)
	<-hk.done
}
