//go:build !windows

package tcasr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A refiner whose sidecar dies mid-request answers the next requests from a
// restarted sidecar. Only the public API is used, with a shell stand-in for
// the helper: the first process dies on its first request, the second answers.
func TestRefinerSurvivesSidecarDeath(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "started-once")
	script := "#!/bin/sh\necho '{\"ready\":true}'\n" +
		"if [ ! -e " + marker + " ]; then : > " + marker + "; head -c 1 >/dev/null; exit 1; fi\n" +
		"while :; do n=$(head -c 3205 | wc -c); [ \"$n\" -eq 3205 ] || exit 0; echo '{\"text\":\"ok\"}'; done\n"
	p := filepath.Join(dir, "gleann-asr-helper")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(HelperEnv, p)
	model := filepath.Join(dir, "m.gguf")
	if err := os.WriteFile(model, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := NewRefiner(model, "en")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// The first request kills the first process; whether it is retried on the
	// replacement or refused, a request shortly after must succeed.
	first, ferr := r.Refine(context.Background(), make([]int16, 1600))
	if ferr == nil && first != "ok" {
		t.Fatalf("the call that hit the death returned %q with no error: a lost answer must be an error", first)
	}
	deadline := time.Now().Add(10 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if got, err = r.Refine(context.Background(), make([]int16, 1600)); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || got != "ok" {
		t.Fatalf("refiner never recovered from the sidecar's death: %q, %v", got, err)
	}
}
