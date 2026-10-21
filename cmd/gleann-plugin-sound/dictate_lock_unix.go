//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireDictateLock makes sure only one dictation process runs per user:
// two of them would both hear the hotkey and type every word twice. The
// returned function releases the lock; the kernel also drops it if the
// process dies.
func acquireDictateLock() (func(), error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, fmt.Sprintf("gleann-plugin-sound-dictate-%d.lock", os.Getuid()))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, nil // cannot lock: do not block dictation over it
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another dictation process is already running (lock %s); "+
			"stop it first, e.g. 'gleann-plugin-sound daemon remove' or 'systemctl --user stop gleann-plugin-sound-dictate'", path)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
