package tcasr

// Fake-helper supervision tests (S2). The test binary re-executed through
// GLEANN_ASR_HELPER plays the sidecar (env switch GLEANN_TEST_HELPER_TAG):
// it prints the scripted handshake, then one scripted frame per request it
// receives, and never exits on its own. Tests kill the process through the
// supervisor's kill hook, which reaps synchronously (the reaper's cmd.Wait
// has completed before the death is acted on), so death timing is
// deterministic. Everything runs under t.TempDir(); the real ~/.gleann is
// never touched.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func helperModel(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-model.gguf")
	if err := os.WriteFile(p, []byte("fake gguf"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// helperFake installs the fake helper: the test binary re-executed through
// GLEANN_ASR_HELPER. It answers 'T' with "ok", 'F' with "hello", 'R'/'A'
// with ok/empty lines, and quits on 'Q'.
func helperFake(t *testing.T) {
	t.Helper()
	t.Setenv(HelperEnv, os.Args[0])
	t.Setenv("GLEANN_TEST_HELPER_TAG", filepath.Base(t.TempDir()))
}

// fakeReady is the handshake every fake emits first. Replies are JSON lines,
// exactly like the native helper's.
const fakeReady = `{"ready":true}` + "\n"

// fakeText is one successful 'T' answer ("ok").
const fakeText = `{"text":"ok"}` + "\n"

// fastSup pins the supervision timing so restarts land inside test budgets.
// All supervisor fields are guarded by its mutex; tests set them only while
// no call or restart loop is in flight.
func fastSup(s *supervisor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryWindow = 5 * time.Second
	s.backoffMax = 100 * time.Millisecond
	s.sleepFn = func(d time.Duration) error { time.Sleep(d); return nil }
}

func pcm3() []int16 { return []int16{1, 2, 3} }

// killRefiner kills the refiner's current helper through the supervision
// kill hook and returns its pointer; the process is reaped synchronously.
func killRefiner(t *testing.T, r *Refiner, failNext bool) *helper {
	t.Helper()
	r.mu.Lock()
	h := r.h
	if failNext {
		r.failNext = true
	}
	r.mu.Unlock()
	if h == nil {
		t.Fatal("refiner has no helper to kill")
	}
	killHelperSync(h)
	return h
}

// killHelperSync kills the helper process and waits until the reaper
// goroutine has completed cmd.Wait (ProcessState is then non-nil).
func killHelperSync(h *helper) {
	_ = h.cmd.Process.Kill()
	<-h.done
}

// TestMain dispatches the fake-helper mode (set by helperFake) before
// running the real tests. The re-executed child speaks the helper protocol:
// handshake, then one JSON reply per request — 'T' with "ok" (overridable
// per spawn through GLEANN_TEST_HELPER_TEXT), 'F' with "hello", 'R' and 'A'
// with ok/empty lines, and a clean exit on 'Q'. It never exits on its own;
// tests kill it through the supervision hooks.
func TestMain(m *testing.M) {
	if os.Getenv("GLEANN_TEST_HELPER_TAG") == "" {
		os.Exit(m.Run())
	}
	textReply := fakeText
	if v := os.Getenv("GLEANN_TEST_HELPER_TEXT"); v != "" {
		textReply = v
	}
	fmt.Print(fakeReady) // handshake
	if os.Getenv("GLEANN_TEST_HELPER_EXIT_AFTER_READY") != "" {
		os.Exit(1) // a helper that loads and then crashes at once
	}
	dieAfterA, _ := strconv.Atoi(os.Getenv("GLEANN_TEST_HELPER_DIE_AT_A")) // crash on the Nth 'A'
	aSeen := 0
	var op [1]byte
	for {
		if _, err := os.Stdin.Read(op[:]); err != nil {
			os.Exit(0) // stdin closed: the supervision reaper ended the pipe
		}
		if op[0] == 'Q' {
			os.Exit(0)
		}
		if op[0] == 'A' {
			aSeen++
			if dieAfterA > 0 && aSeen == dieAfterA {
				os.Exit(1) // dies in the middle of a pass, without a reply
			}
		}
		if op[0] == 'A' || op[0] == 'T' {
			var ln [4]byte
			if _, err := io.ReadFull(os.Stdin, ln[:]); err != nil {
				os.Exit(0)
			}
			if _, err := io.ReadFull(os.Stdin, make([]byte, binary.LittleEndian.Uint32(ln[:])*2)); err != nil {
				os.Exit(0)
			}
		}
		var reply string
		switch op[0] {
		case 'T':
			reply = textReply
		case 'F':
			reply = `{"text":"hello"}` + "\n"
		case 'A':
			reply = `{"committed":"","tentative":""}` + "\n"
		default:
			reply = `{"ok":true}` + "\n"
		}
		if _, err := os.Stdout.WriteString(reply); err != nil {
			os.Exit(0)
		}
	}
}

func TestRefinerRecoversAfterHelperDeath(t *testing.T) {
	// Serves 'T' requests, then the test kills it: the next Refine (red
	// today: "asr helper died" forever) must succeed on a restarted helper.
	helperFake(t)
	r, err := NewRefiner(helperModel(t), "en")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fastSup(r.sup)

	got, err := r.Refine(context.Background(), pcm3())
	if err != nil || got != "ok" {
		t.Fatalf("first Refine: %q, %v", got, err)
	}
	killRefiner(t, r, false)
	if st := r.Status(); st == "up" || st == "off" {
		t.Fatalf("status after killing the helper: %q", st)
	}
	// The death call waits its window; the restart (backoff ≤ 100 ms)
	// lands inside it and the call retries once and succeeds.
	got, err = r.Refine(context.Background(), pcm3())
	if err != nil || got != "ok" {
		t.Fatalf("Refine after death+restart: %q, %v", got, err)
	}
	if st := r.Status(); st != "up" {
		t.Fatalf("status after recovery: %q, want up", st)
	}
}

func TestRefinerReapsCrashedHelper(t *testing.T) {
	// A crashed process must be reaped (cmd.ProcessState != nil) without
	// waiting for shutdown, and the supervisor must detach from it.
	helperFake(t)
	r, err := NewRefiner(helperModel(t), "en")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fastSup(r.sup)
	if _, err := r.Refine(context.Background(), pcm3()); err != nil {
		t.Fatal(err)
	}
	dead := killRefiner(t, r, false)
	if dead.cmd.ProcessState == nil {
		t.Fatal("killed helper has no ProcessState: the reaper did not reap it")
	}
	waitFor(t, 5*time.Second, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.h == nil && r.state != "up"
	}, "supervisor kept the crashed helper")
	waitFor(t, 5*time.Second, func() bool { return r.Status() == "up" }, "restart did not replace the crashed helper")
}

func TestRefinerUnavailableWhenRestartKeepsFailing(t *testing.T) {
	// Restart attempts keep failing → ErrRefinerUnavailable now, state
	// "down" after maxDown consecutive failed restarts.
	helperFake(t)
	r, err := NewRefiner(helperModel(t), "en")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fastSup(r.sup)
	r.mu.Lock()
	r.retryWindow = 300 * time.Millisecond
	r.maxDown = 3
	r.failAll = true
	r.mu.Unlock()
	killRefiner(t, r, false)
	if _, err := r.Refine(context.Background(), pcm3()); !errors.Is(err, ErrRefinerUnavailable) {
		t.Fatalf("death call: %v, want ErrRefinerUnavailable", err)
	}
	r.mu.Lock()
	st, fails := r.state, r.fails
	r.mu.Unlock()
	if st == "up" {
		t.Fatal("helper died but the refiner still reports up")
	}
	if fails == 0 {
		t.Fatal("no restart attempt was recorded")
	}
	if st == "restarting" && fails >= 3 {
		t.Errorf("after %d failed restarts state = %q, want down", fails, st)
	}
	waitFor(t, 5*time.Second, func() bool { return r.Status() == "down" }, "state never became down")
}

func waitFor(t *testing.T, budget time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
