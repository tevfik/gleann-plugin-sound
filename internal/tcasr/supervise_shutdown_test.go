package tcasr

// S2 supervision versus shutdown: a restart must never outlive Close, the
// back-off must not become a restart storm, and no process may leak.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newCountingRefiner is a refiner whose restarts go through a counting hook
// that spawns the real fake helper (or fails, when failSpawn is set).
func newCountingRefiner(t *testing.T, failSpawn bool) (*Refiner, *atomic.Int64) {
	t.Helper()
	helperFake(t)
	r, err := NewRefiner(helperModel(t), "en")
	if err != nil {
		t.Fatal(err)
	}
	var spawns atomic.Int64
	r.mu.Lock()
	r.spawnHook = func(s *supervisor) (*helper, error) {
		spawns.Add(1)
		if failSpawn {
			return nil, errTestSpawn
		}
		return startHelper(s.mode, s.model, s.lang, s.attRight)
	}
	r.mu.Unlock()
	return r, &spawns
}

type testSpawnError struct{}

func (testSpawnError) Error() string { return "spawn refused by the test" }

var errTestSpawn = testSpawnError{}

func loopRunning(r *Refiner) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loop
}

// A death followed at once by Close, while the restart is still in its
// back-off sleep: the loop must wake up and exit without spawning anything.
func TestNoRestartAfterClose(t *testing.T) {
	r, spawns := newCountingRefiner(t, false)
	killRefiner(t, r, false)
	waitFor(t, 2*time.Second, func() bool { return r.Status() == "restarting" }, "death was not noticed")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// The first back-off is 1 s: the back-off goroutine must be gone long
	// before that, and nothing may have been spawned.
	waitFor(t, 500*time.Millisecond, func() bool { return !loopRunning(r) }, "back-off goroutine survived Close")
	time.Sleep(1200 * time.Millisecond) // past the back-off it was sleeping on
	if n := spawns.Load(); n != 0 {
		t.Errorf("%d restart(s) spawned after Close", n)
	}
	if st := r.Status(); st != "off" {
		t.Errorf("status after Close = %q, want off", st)
	}
	if _, err := r.Refine(context.Background(), pcm3()); !errors.Is(err, ErrRefinerUnavailable) {
		t.Errorf("Refine after Close = %v, want ErrRefinerUnavailable", err)
	}
}

// Close that lands while a replacement is being spawned: the replacement is
// reaped, not adopted and not leaked.
func TestCloseDuringRestartSpawnReapsReplacement(t *testing.T) {
	helperFake(t)
	r, err := NewRefiner(helperModel(t), "en")
	if err != nil {
		t.Fatal(err)
	}
	fastSup(r.sup)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var spawned atomic.Pointer[helper]
	r.mu.Lock()
	r.spawnHook = func(s *supervisor) (*helper, error) {
		once.Do(func() { close(entered) })
		<-release
		h, err := startHelper(s.mode, s.model, s.lang, s.attRight)
		spawned.Store(h)
		return h, err
	}
	r.mu.Unlock()
	killRefiner(t, r, false)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("restart never started")
	}
	_ = r.Close()
	close(release)
	waitFor(t, 3*time.Second, func() bool { return spawned.Load() != nil && !loopRunning(r) }, "restart pass did not finish")
	h := spawned.Load()
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement spawned during Close was leaked (not reaped)")
	}
	if h.cmd.ProcessState == nil {
		t.Error("replacement has no ProcessState: not collected")
	}
	if st := r.Status(); st != "off" {
		t.Errorf("status = %q, want off", st)
	}
}

// A helper that can never be restarted is tried maxDown times, backed off
// between tries, and then left alone: no storm, no more spawns.
func TestRestartBackoffIsBoundedNoStorm(t *testing.T) {
	r, spawns := newCountingRefiner(t, true)
	defer r.Close()
	fastSup(r.sup)
	r.mu.Lock()
	r.maxDown = 4
	r.backoffMax = 20 * time.Millisecond
	r.mu.Unlock()
	killRefiner(t, r, false)
	waitFor(t, 5*time.Second, func() bool { return r.Status() == "down" }, "never reached down")
	time.Sleep(300 * time.Millisecond) // many backoffMax periods
	// The killed helper never answered a call, so its death is failure 1;
	// the three refused restarts bring the count to maxDown.
	if n := spawns.Load(); n != 3 {
		t.Errorf("spawns = %d, want 3 (maxDown 4, minus the unproven death): the back-off must not storm", n)
	}
	waitFor(t, time.Second, func() bool { return !loopRunning(r) }, "restart pass still running while down")
	// The cap holds for any attempt number.
	for _, n := range []int{0, 1, 5, 10, 64, 1 << 20} {
		if d := backoffDelay(n, time.Minute); d > time.Minute || d < time.Second {
			t.Errorf("backoffDelay(%d) = %v, outside [1s, 1m]", n, d)
		}
	}
}

// Restarting reaps the process it replaces and leaves exactly one live one.
func TestRestartReapsOldProcess(t *testing.T) {
	r, spawns := newCountingRefiner(t, false)
	defer r.Close()
	fastSup(r.sup)
	old := killRefiner(t, r, false)
	waitFor(t, 5*time.Second, func() bool { return r.Status() == "up" && spawns.Load() == 1 }, "no replacement")
	if old.cmd.ProcessState == nil {
		t.Error("old process was not reaped")
	}
	r.mu.Lock()
	cur := r.h
	r.mu.Unlock()
	if cur == nil || cur == old {
		t.Fatal("old helper still installed after the restart")
	}
	if gone, _ := cur.died(); gone {
		t.Error("replacement is not alive")
	}
}
