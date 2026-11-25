package tcasr

// Review fixes for S2: the flapping helper must reach "down" (B1), a pass cut
// short by a death must fail whole (B2), and "down" must not invite retries.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// B1: a helper that prints ready and then crashes is not a successful
// restart. Default-like limits (maxDown 3, 1 s doubling back-off); only the
// sleep is shortened, through the sleepFn seam, and it records the schedule.
func TestFlappingHelperReachesDownWithCappedAttempts(t *testing.T) {
	helperFake(t)
	t.Setenv("GLEANN_TEST_HELPER_EXIT_AFTER_READY", "1")
	var spawns atomic.Int64
	var slept []time.Duration
	s := &supervisor{mode: "offline", model: helperModel(t), lang: "en", attRight: -1}
	s.sleepFn = func(d time.Duration) error { s.mu.Lock(); slept = append(slept, d); s.mu.Unlock(); return nil }
	s.spawnHook = func(s *supervisor) (*helper, error) {
		spawns.Add(1)
		return startHelper(s.mode, s.model, s.lang, s.attRight)
	}
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	defer s.close()
	waitFor(t, 10*time.Second, func() bool { return s.status() == "down" }, "a helper that crashes after ready never reached down")
	time.Sleep(200 * time.Millisecond)
	if n := spawns.Load(); n != int64(defaultMaxDown) {
		t.Errorf("spawns = %d, want exactly %d before going down", n, defaultMaxDown)
	}
	s.mu.Lock()
	got := append([]time.Duration(nil), slept...)
	s.mu.Unlock()
	if len(got) < 2 || got[0] != 2*time.Second || got[1] != 4*time.Second {
		t.Errorf("back-off schedule = %v, want it to keep escalating (2s, 4s, ...)", got)
	}
}

// A helper that has answered a call has proved itself: its later death starts
// the schedule over instead of counting toward down.
func TestProvenHelperDeathResetsFailureCount(t *testing.T) {
	helperFake(t)
	r, err := NewRefiner(helperModel(t), "en")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fastSup(r.sup)
	for i := 0; i < 5; i++ { // more deaths than maxDown, each after a good call
		if _, err := r.Refine(context.Background(), pcm3()); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		killRefiner(t, r, false)
		waitFor(t, 5*time.Second, func() bool { return r.Status() == "up" }, "no restart after a proven helper died")
	}
}

// B2: the helper dies in the middle of a batch pass. The pass must fail with
// the sentinel; the rest of it must never run on a fresh helper and come back
// as a (truncated) success.
func TestBatchDeathMidPassFailsWhole(t *testing.T) {
	helperFake(t)
	t.Setenv("GLEANN_TEST_HELPER_DIE_AT_A", "3")
	e, err := NewEngine(helperModel(t))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	pcm := make([]int16, 16000*6) // six 1 s chunks
	segs, err := e.TranscribeStreamSegments(context.Background(), pcm)
	if !errors.Is(err, ErrRefinerUnavailable) {
		t.Fatalf("segments = %+v err = %v, want ErrRefinerUnavailable and no partial result", segs, err)
	}
	if len(segs) != 0 {
		t.Errorf("a partial result came back with the error: %+v", segs)
	}
}

// M2: down is terminal (only a plugin restart recovers), so it must not tell
// clients to come back in a few seconds.
func TestDownRetryAfterIsLong(t *testing.T) {
	s := &supervisor{state: stateDown, fails: 3}
	if got := s.retryAfter(); got < 600 {
		t.Errorf("Retry-After in terminal down = %d s, want a long value", got)
	}
}

// The error text names the state: restarting while a restart is under way,
// "restart the plugin" once down.
func TestUnavailableTextIsStateSpecific(t *testing.T) {
	s := &supervisor{state: stateRestart}
	s.mu.Lock()
	restarting := s.unavailableLocked()
	s.state = stateDown
	down := s.unavailableLocked()
	s.mu.Unlock()
	if !errors.Is(restarting, ErrRefinerUnavailable) || !errors.Is(down, ErrRefinerUnavailable) {
		t.Fatal("sentinel lost")
	}
	if got := restarting.Error(); !strings.Contains(got, "being restarted") {
		t.Errorf("restarting text = %q", got)
	}
	if got := down.Error(); strings.Contains(got, "being restarted") || !strings.Contains(got, "restart the plugin") {
		t.Errorf("down text = %q", got)
	}
}

// killIdle kills the refiner's helper while no call is in progress and waits
// for the supervisor to settle: "up" again, or "down".
func killIdle(t *testing.T, r *Refiner) string {
	t.Helper()
	killRefiner(t, r, false)
	var st string
	waitFor(t, 5*time.Second, func() bool { st = r.Status(); return st == "up" || st == "down" }, "no settle after an idle kill")
	return st
}

// Idle deaths spaced past the healthy window never count as failed starts: no
// request failed, so the helper must not go terminal however often the
// process is killed (below the hourly idle budget).
func TestIdleDeathsAfterHealthyPeriodDoNotGoDown(t *testing.T) {
	helperFake(t)
	r, err := NewRefiner(helperModel(t), "en")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fastSup(r.sup)
	r.sup.mu.Lock()
	r.sup.healthyFor, r.sup.idleMax = 40*time.Millisecond, 100
	r.sup.mu.Unlock()
	for i := 0; i < defaultMaxDown*2; i++ { // twice the failed-start budget
		time.Sleep(80 * time.Millisecond) // healthy: past the window, no call made
		if st := killIdle(t, r); st != "up" {
			t.Fatalf("idle kill %d left the helper %q", i+1, st)
		}
	}
	if _, err := r.Refine(context.Background(), pcm3()); err != nil {
		t.Fatalf("refiner unusable after idle deaths: %v", err)
	}
}

// A rapid idle crash loop is still bounded: at most idleMax idle restarts in
// the window, then down.
func TestRapidIdleCrashLoopStillGoesDown(t *testing.T) {
	helperFake(t)
	r, err := NewRefiner(helperModel(t), "en")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fastSup(r.sup)
	const budget = 5
	r.sup.mu.Lock()
	r.sup.healthyFor, r.sup.idleMax = 10*time.Millisecond, budget
	r.sup.mu.Unlock()
	survived := 0
	for i := 0; i < budget*3; i++ {
		time.Sleep(30 * time.Millisecond)
		if killIdle(t, r) == "down" {
			break
		}
		survived++
	}
	if r.Status() != "down" {
		t.Fatal("an idle crash loop never went down")
	}
	if survived != budget {
		t.Errorf("idle restarts before down = %d, want exactly %d", survived, budget)
	}
}

// Deaths early in a helper's life still count, idle or not (covered by the
// flapping test); a death during a call counts too.
func TestDeathDuringCallCountsAsFailure(t *testing.T) {
	s := &supervisor{mode: "offline", state: stateUp, maxDown: 3, healthyFor: time.Millisecond}
	h := &helper{}
	s.h, s.proven, s.upSince, s.inflight = h, true, time.Now().Add(-time.Hour), 1
	hold := make(chan struct{})
	defer close(hold)
	s.sleepFn = func(time.Duration) error { <-hold; return nil } // keep the restart pending
	s.spawnHook = func(*supervisor) (*helper, error) { return nil, errors.New("no") }
	s.died(h)
	defer s.close()
	s.mu.Lock()
	f := s.fails
	s.mu.Unlock()
	if f != 1 {
		t.Errorf("fails after a death during a call = %d, want 1", f)
	}
}
