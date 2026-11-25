package tcasr

// Second half of the S2 supervision tests: arrivals during restart, the
// single retry after a fast restart, backoff sequence, supervised engine
// batch, /health-visible status values and the -race parallel refine test.

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func TestRefinerArrivalsDuringRestartGetSentinel(t *testing.T) {
	// Only the call that hit the death waits in the retry window; fresh
	// arrivals during a pending restart get the sentinel immediately.
	helperFake(t)
	r, err := NewRefiner(helperModel(t), "en")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fastSup(r.sup)
	r.mu.Lock()
	r.retryWindow = 150 * time.Millisecond
	r.failAll = true // keep the restart pending for the whole test
	r.mu.Unlock()
	killRefiner(t, r, false)

	if _, err := r.Refine(context.Background(), pcm3()); !errors.Is(err, ErrRefinerUnavailable) {
		t.Fatalf("death call: %v, want ErrRefinerUnavailable", err)
	}
	r.mu.Lock()
	running := r.state == "restarting" || r.state == "down"
	r.mu.Unlock()
	if !running {
		t.Fatal("restart loop is not running")
	}
	start := time.Now()
	if _, err := r.Refine(context.Background(), pcm3()); !errors.Is(err, ErrRefinerUnavailable) {
		t.Fatalf("arrival: %v, want ErrRefinerUnavailable", err)
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("arrival waited %v; want the sentinel without waiting", d)
	}
}

func TestSupervisorRetriesFailingCallOnceAfterFastRestart(t *testing.T) {
	// The call that hit the death is retried once when the restart lands
	// inside the window; the retried answer comes from the replacement,
	// chosen deterministically through the spawn hook.
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
	killRefiner(t, r, false)
	r.mu.Lock()
	r.spawnHook = func(s *supervisor) (*helper, error) {
		// The restart loop runs on its own goroutine; t.Setenv is bound to
		// the test goroutine, so this one spawn gets its per-request text
		// through a plain Setenv/Unsetenv around the start.
		_ = os.Setenv("GLEANN_TEST_HELPER_TEXT", `{"text":"late"}`+"\n")
		defer os.Unsetenv("GLEANN_TEST_HELPER_TEXT")
		return startHelper(s.mode, s.model, s.lang, s.attRight)
	}
	r.mu.Unlock()

	got, err := r.Refine(context.Background(), pcm3())
	if err != nil {
		t.Fatalf("death call: %v, want the once-retry to succeed", err)
	}
	if got != "late" {
		t.Fatalf("retry text = %q, want the replacement's frame", got)
	}
}

func TestSupervisorBackoffSequence(t *testing.T) {
	s := &supervisor{}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60}
	for i, w := range want {
		if got := s.backoff(i); got != w*time.Second {
			t.Errorf("backoff(%d) = %v, want %v", i, got, w*time.Second)
		}
	}
}

func TestEngineBatchSupervised(t *testing.T) {
	// The engine's batch helper is supervised too: after a death the pass
	// returns the sentinel (R/A/F must restart as a whole, no per-op
	// retry), the dead process is reaped, and a later pass runs on the
	// restarted helper.
	helperFake(t)
	e, err := NewEngine(helperModel(t))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	pcm := make([]int16, 4000)
	segs, err := e.TranscribeStreamSegments(context.Background(), pcm)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].Text != "hello" {
		t.Fatalf("segments = %+v", segs)
	}

	e.mu.Lock()
	sup := e.batch
	h := sup.h
	e.mu.Unlock()
	if h == nil {
		t.Fatal("batch helper not running after the first pass")
	}
	fastSup(sup)
	e.mu.Lock()
	sup.failNext = true
	e.mu.Unlock()
	killHelperSync(h)

	if _, err := e.TranscribeStreamSegments(context.Background(), pcm3()); !errors.Is(err, ErrRefinerUnavailable) {
		t.Fatalf("pass after death: %v, want ErrRefinerUnavailable", err)
	}
	if h.cmd.ProcessState == nil {
		t.Fatal("dead batch helper was not reaped")
	}
	waitFor(t, 5*time.Second, func() bool { return sup.status() == "up" }, "batch supervisor did not restart")
	e.mu.Lock()
	replaced := e.batch == sup && sup.h != nil && sup.h != h
	e.mu.Unlock()
	if !replaced {
		t.Fatal("dead batch helper was not replaced")
	}
	segs, err = e.TranscribeStreamSegments(context.Background(), pcm)
	if err != nil {
		t.Fatalf("pass after restart: %v", err)
	}
	if len(segs) != 1 || segs[0].Text != "hello" {
		t.Fatalf("segments after restart = %+v", segs)
	}
}

func TestRefinerStatusValues(t *testing.T) {
	// Values exposed to /health: off before start, up after, restarting or
	// down after a death, off after Close.
	s := &supervisor{mode: "offline", model: helperModel(t)}
	if st := s.status(); st != "off" {
		t.Errorf("fresh supervisor: status = %q, want off", st)
	}
	helperFake(t)
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	if st := s.status(); st != "up" {
		t.Errorf("after start: status = %q, want up", st)
	}
	fastSup(s)
	s.mu.Lock()
	s.failAll = true
	h := s.h
	s.mu.Unlock()
	killHelperSync(h)
	if st := s.status(); st == "up" || st == "off" {
		t.Errorf("after death: status = %q, want restarting or down", st)
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	if st := s.status(); st != "off" {
		t.Errorf("after close: status = %q, want off", st)
	}
}

func TestParallelRefinesOnLiveHelper(t *testing.T) {
	// Four (plus) concurrent Refine calls on one supervised helper: they
	// queue on the helper lock and all pass; clean under -race.
	helperFake(t)
	r, err := NewRefiner(helperModel(t), "en")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fastSup(r.sup)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := r.Refine(context.Background(), pcm3())
			if err != nil || got != "ok" {
				t.Errorf("parallel Refine %d: %q, %v", i, got, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestConcurrentRefinesAcrossDeath(t *testing.T) {
	// The helper dies while four Refines are in flight or arriving: every
	// one finishes with the sentinel or succeeds on a replacement — never
	// a raw death error, no deadlock, clean under -race.
	helperFake(t)
	r, err := NewRefiner(helperModel(t), "en")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fastSup(r.sup)
	r.mu.Lock()
	r.retryWindow = 300 * time.Millisecond
	r.failAll = true
	r.mu.Unlock()
	killRefiner(t, r, false)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = r.Refine(context.Background(), pcm3())
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil && !errors.Is(e, ErrRefinerUnavailable) {
			t.Errorf("Refine %d: %v, want nil or ErrRefinerUnavailable", i, e)
		}
	}
}
