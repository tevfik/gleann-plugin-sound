//go:build !windows

package tcasr

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// fakeHelper writes a stand-in sidecar that reports ready and then idles.
func fakeHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gleann-asr-helper")
	script := "#!/bin/sh\necho '{\"ready\":true}'\nexec sleep 60\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(HelperEnv, p)
	return p
}

// A sidecar that dies while idle must flip the lock-free alive flag without
// any call in flight, so /health reports "down" instead of a stale "up".
func TestIdleSidecarDeathClearsAlive(t *testing.T) {
	fakeHelper(t)
	model := filepath.Join(t.TempDir(), "m.gguf")
	if err := os.WriteFile(model, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := NewRefiner(model, "en")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Alive() {
		t.Fatal("fresh helper reported dead")
	}
	if err := r.h.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for r.Alive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r.Alive() {
		t.Error("Alive() still true after the idle sidecar was killed")
	}
	if err := r.Close(); err != nil { // must not hang or double-Wait
		t.Fatal(err)
	}
}
