//go:build darwin

package hotkey

import (
	"fmt"
	"log"

	exthotkey "golang.design/x/hotkey"
)

type darwinPlatform struct {
	extHk *exthotkey.Hotkey
}

// Register registers the hotkey on macOS via Carbon RegisterEventHotKey
// (through the golang.design/x/hotkey library).
func (hk *Hotkey) Register() error {
	extMods, err := modsToExt(hk.mods)
	if err != nil {
		return err
	}
	extKey, err := keyToExt(hk.key)
	if err != nil {
		return err
	}

	extHk := exthotkey.New(extMods, extKey)
	if err := extHk.Register(); err != nil {
		return fmt.Errorf("hotkey: %w", err)
	}

	hk.platform = &darwinPlatform{extHk: extHk}
	log.Println("[hotkey] registered via Carbon (macOS)")

	go func() {
		defer close(hk.done)
		for {
			select {
			case <-hk.stopCh:
				return
			case <-extHk.Keydown():
				select {
				case hk.keydownCh <- struct{}{}:
				default:
				}
			case <-extHk.Keyup():
				select {
				case hk.keyupCh <- struct{}{}:
				default:
				}
			}
		}
	}()

	return nil
}

// Unregister unregisters the hotkey on macOS.
func (hk *Hotkey) Unregister() error {
	select {
	case <-hk.stopCh:
		return nil
	default:
	}

	close(hk.stopCh)
	<-hk.done

	if p, ok := hk.platform.(*darwinPlatform); ok {
		p.extHk.Unregister()
	}
	log.Println("[hotkey] unregistered")
	return nil
}

// ---------------------------------------------------------------------------
// Type mapping — our abstract types → golang.design/x/hotkey types
// ---------------------------------------------------------------------------

func modsToExt(mods []Modifier) ([]exthotkey.Modifier, error) {
	var out []exthotkey.Modifier
	for _, m := range mods {
		switch m {
		case ModCtrl:
			out = append(out, exthotkey.ModCtrl)
		case ModShift:
			out = append(out, exthotkey.ModShift)
		case ModAlt:
			out = append(out, exthotkey.ModOption)
		case ModSuper:
			out = append(out, exthotkey.ModCmd)
		default:
			return nil, fmt.Errorf("hotkey: unsupported modifier %d", m)
		}
	}
	return out, nil
}

func keyToExt(k Key) (exthotkey.Key, error) {
	m := map[Key]exthotkey.Key{
		KeySpace: exthotkey.KeySpace, KeyReturn: exthotkey.KeyReturn,
		KeyEscape: exthotkey.KeyEscape, KeyTab: exthotkey.KeyTab,
		KeyA: exthotkey.KeyA, KeyB: exthotkey.KeyB, KeyC: exthotkey.KeyC,
		KeyD: exthotkey.KeyD, KeyE: exthotkey.KeyE, KeyF: exthotkey.KeyF,
		KeyG: exthotkey.KeyG, KeyH: exthotkey.KeyH, KeyI: exthotkey.KeyI,
		KeyJ: exthotkey.KeyJ, KeyK: exthotkey.KeyK, KeyL: exthotkey.KeyL,
		KeyM: exthotkey.KeyM, KeyN: exthotkey.KeyN, KeyO: exthotkey.KeyO,
		KeyP: exthotkey.KeyP, KeyQ: exthotkey.KeyQ, KeyR: exthotkey.KeyR,
		KeyS: exthotkey.KeyS, KeyT: exthotkey.KeyT, KeyU: exthotkey.KeyU,
		KeyV: exthotkey.KeyV, KeyW: exthotkey.KeyW, KeyX: exthotkey.KeyX,
		KeyY: exthotkey.KeyY, KeyZ: exthotkey.KeyZ,
		Key0: exthotkey.Key0, Key1: exthotkey.Key1, Key2: exthotkey.Key2,
		Key3: exthotkey.Key3, Key4: exthotkey.Key4, Key5: exthotkey.Key5,
		Key6: exthotkey.Key6, Key7: exthotkey.Key7, Key8: exthotkey.Key8,
		Key9: exthotkey.Key9,
		KeyF1: exthotkey.KeyF1, KeyF2: exthotkey.KeyF2, KeyF3: exthotkey.KeyF3,
		KeyF4: exthotkey.KeyF4, KeyF5: exthotkey.KeyF5, KeyF6: exthotkey.KeyF6,
		KeyF7: exthotkey.KeyF7, KeyF8: exthotkey.KeyF8, KeyF9: exthotkey.KeyF9,
		KeyF10: exthotkey.KeyF10, KeyF11: exthotkey.KeyF11, KeyF12: exthotkey.KeyF12,
	}
	if v, ok := m[k]; ok {
		return v, nil
	}
	return 0, fmt.Errorf("hotkey: unsupported key %d", k)
}
