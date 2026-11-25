package tcasr

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// Supervision of the offline ASR helper (S2).
//
// The helper is a long-lived process holding a loaded model. When it dies -
// OOM-killed while transcribing, segfault, a bad state that only a restart
// clears - nothing else in the plugin notices, and every call after that
// fails against a dead pipe. This file owns one helper process and its
// restarts:
//
//   - The process is reaped by exactly one goroutine (helper.reap), which
//     fires a death hook back into the supervisor before any waiter can be
//     released, so "is it up?" never answers about a dead helper.
//   - Callers observe a down helper through ErrRefinerUnavailable (the
//     core-identical sentinel the HTTP layer maps to 503 + Retry-After)
//     rather than a dead-pipe error, and the supervisor's state feeds
//     /health.
//   - A call that actually ran into the death waits out a bounded window
//     for the automatic restart and retries the operation once on the
//     replacement; arrivals that find the replacement already failing - or
//     that cannot afford to wait, as the streaming batch pass does - get the
//     sentinel immediately, so one dying helper cannot stall the request
//     loop.
//   - A helper that dies while idle after a healthy period (up for
//     healthyFor, or one that has answered a call) did not fail any request:
//     it restarts with back-off and does not count toward "down". A crash
//     loop is still bounded: more than idleMax such deaths inside an hour
//     end in "down". Deaths during a call, or before the helper has proven
//     itself, count as failed starts as below.
//   - Repeatedly failing restarts back off exponentially and end in the
//     terminal "down" state: /health then reports the refiner down with a
//     long Retry-After, and the model is not loaded over and over on a
//     machine that cannot hold it.
//
// The supervisor owns only helper processes: mode/model/language are fixed
// at construction, because spawning happens without the lock held and
// startHelper reads those fields.

// ErrRefinerUnavailable is core's sentinel, kept under this package's name so
// the supervision can report the same error the HTTP layer maps to
// 503 + Retry-After.
var ErrRefinerUnavailable = core.ErrRefinerUnavailable

// Observable states of a supervised helper.
const (
	stateOff       = "off"
	stateUp        = "up"
	stateRestart   = "restarting"
	stateDown      = "down"
	defaultRetry   = 2 * time.Second // how long one call waits for a restart
	defaultBackoff = time.Minute     // ceiling of the restart back-off
	defaultMaxDown = 3               // failed restarts before the terminal state
	// downRetryAfter is the Retry-After (seconds) sent in the terminal state.
	downRetryAfter = 3600
	// defaultHealthy is how long a helper must stay up, idle, before its
	// death no longer counts as a failed start.
	defaultHealthy = 30 * time.Second
	// defaultIdleMax idle deaths per idleWindow are restarted; one more
	// goes down, so a crash loop is bounded.
	defaultIdleMax = 6
	idleWindow     = time.Hour
)

// How a caller that found no live helper should treat the automatic restart.
const (
	// waitAlways: this call ran into the death itself, so it waits its
	// window and retries on the replacement.
	waitAlways = iota
	// waitIfPromising: nothing has failed yet, so the replacement is worth
	// waiting for; once a restart attempt has failed the caller is turned
	// away instead, which keeps request loops from piling up behind a
	// dying helper.
	waitIfPromising
	// noWait: never wait. The batch pass of a streaming session runs
	// several operations per chunk on a latency budget; one of them
	// waiting would be felt as stutter.
	noWait
)

// supervisor keeps one helper process alive and accountable.
type supervisor struct {
	mu sync.Mutex

	mode  string // helper mode: "offline", "stream" or "assisted"
	model string
	lang  string
	// attRight is the attachment window asked of the helper (-1: offline).
	// Written before the first spawn; SetLanguage updates it only while a
	// language change is allowed, that is, with no helper running.
	attRight int

	retryWindow time.Duration             // how long a caller waits for a restart
	backoffMax  time.Duration             // ceiling of the restart back-off
	sleepFn     func(time.Duration) error // test seam for the back-off sleep

	state string
	h     *helper
	last  error // what the last failure was
	// fails counts consecutive failures: restart spawns that errored, and
	// helpers that died before answering a single call (a helper that prints
	// ready and then crashes is not a successful restart). It is reset only
	// when a helper has answered one call (proven), never by a spawn alone.
	fails    int
	proven   bool // the installed helper has answered a call
	upSince  time.Time
	inflight int     // calls running on the installed helper
	callDied *helper // helper whose pipe broke under a call
	// Idle deaths (no call running, helper up for healthyFor or proven) do
	// not count as failures; they are rate limited instead. idleDeaths holds
	// the times inside idleWindow, idleMax is how many are tolerated.
	idleDeaths []time.Time
	idleBack   int // extra back-off steps from recent idle deaths
	healthyFor time.Duration
	idleMax    int
	nowFn      func() time.Time // test seam for the clock
	maxDown    int              // fails at which the helper goes terminal
	starting   bool             // a cold start is spawning outside the lock
	loop       bool             // a restart pass is running
	closed     bool
	ready      chan struct{} // closed and replaced on every state/h change
	stop       chan struct{} // closed to interrupt the back-off sleep (lazily)
	stopClosed bool          // stop has been closed
	// failNext / failAll make the next (or every) restart fail: the tests
	// use them to keep a restart pending or to force the terminal state.
	failNext  bool
	failAll   bool
	spawnHook func(*supervisor) (*helper, error) // test seam for spawning
}

// window/maxDown/backoffOf read tunables that a zero-value supervisor leaves
// unset.
func (s *supervisor) window() time.Duration {
	if s.retryWindow > 0 {
		return s.retryWindow
	}
	return defaultRetry
}

func (s *supervisor) downLimit() int {
	if s.maxDown > 0 {
		return s.maxDown
	}
	return defaultMaxDown
}

func (s *supervisor) healthy() time.Duration {
	if s.healthyFor > 0 {
		return s.healthyFor
	}
	return defaultHealthy
}

func (s *supervisor) idleLimit() int {
	if s.idleMax > 0 {
		return s.idleMax
	}
	return defaultIdleMax
}

func (s *supervisor) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

func (s *supervisor) backoffOf(fails int) time.Duration {
	ceiling := s.backoffMax
	if ceiling <= 0 {
		ceiling = defaultBackoff
	}
	return backoffDelay(fails, ceiling)
}

// fire releases anyone waiting on a state change. Callers must not hold the
// lock while watching the channel, and must re-check the state after it.
func (s *supervisor) fireLocked() {
	if s.ready == nil {
		s.ready = make(chan struct{})
	}
	close(s.ready)
	s.ready = make(chan struct{})
}

// backoffDelay is the restart schedule: 1s, 2s, 4s, ... capped at max. It is
// a free function so tests can check the sequence on a zero-value supervisor.
func backoffDelay(attempt int, max time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := time.Second
	for i := 0; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		return max
	}
	return d
}

func (s *supervisor) backoff(attempt int) time.Duration {
	return backoffDelay(attempt, defaultBackoff)
}

// status is what /health reports for this helper: "off", "up", "restarting"
// or "down". A supervisor that was never started is off; one whose helper
// died and whose restarts keep failing is down until the process goes away.
func (s *supervisor) status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case "", stateOff:
		return stateOff
	case stateUp:
		if s.h == nil {
			return stateRestart
		}
		return stateUp
	default:
		return s.state
	}
}

// retryAfter is the Retry-After (seconds) that goes with the unavailability:
// 5 while a restart is pending, and the back-off itself once the helper is
// terminally down. Zero means "not unavailable".
func (s *supervisor) retryAfter() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case stateRestart:
		return core.RetryAfter
	case stateDown:
		// Terminal: nothing in this process brings the helper back, only a
		// restart of the plugin does, so clients are not invited to retry
		// soon.
		return downRetryAfter
	default:
		return 0
	}
}

// unavailableLocked explains why there is no helper. The sentinel is what the
// HTTP layer maps to 503 + Retry-After, so callers never see a dead-pipe
// message they cannot act on.
func (s *supervisor) unavailableLocked() error {
	// The text is state-specific: a restart is under way, or it is not and
	// only a restart of the plugin helps.
	if s.state == stateDown {
		return fmt.Errorf("%w: the refiner failed repeatedly, restart the plugin", ErrRefinerUnavailable)
	}
	if s.last != nil {
		return fmt.Errorf("%w: its process died and is being restarted (%v)", ErrRefinerUnavailable, s.last)
	}
	return fmt.Errorf("%w: its process died and is being restarted", ErrRefinerUnavailable)
}

// spawn starts a helper. The decision to fail it (test seams) and the hook
// are read under the lock; the process is started without it held, because
// loading a model takes seconds.
func (s *supervisor) spawn() (*helper, error) {
	s.mu.Lock()
	fail, hook := s.failAll || s.failNext, s.spawnHook
	if s.failNext {
		s.failNext = false
	}
	mode, model, lang, att := s.mode, s.model, s.lang, s.attRight
	s.mu.Unlock()
	if fail {
		return nil, fmt.Errorf("asr helper restart suppressed (GLEANN test seam)")
	}
	if hook != nil {
		return hook(s)
	}
	return startHelper(mode, model, lang, att)
}

// adopt finishes a start: the "a start is in progress" flag is cleared and
// the helper installed, or the failure recorded, in one critical section, so
// nobody else decides to spawn while this one is still adopting.
func (s *supervisor) adopt(h *helper, spawnErr error) error {
	s.mu.Lock()
	s.starting = false
	if spawnErr != nil {
		s.last = spawnErr
		s.fireLocked()
		s.mu.Unlock()
		return spawnErr
	}
	armed, adopted := s.installLocked(h)
	live := s.h != nil && !s.closed
	s.mu.Unlock()
	if !adopted {
		// Closed down, or someone else's helper is in place. Collecting this
		// child happens outside the supervisor lock: the reaper's hook takes it.
		_ = h.close()
		if live {
			return nil
		}
		return s.statusErr()
	}
	if !armed {
		// The process was gone before its death hook could be armed.
		s.died(h)
	}
	return nil
}

// installLocked puts a fresh helper in place and arms its death hook.
// Arming happens under the supervisor lock, which is what closes the window
// where a process could exit between being started and being watched: the
// reaper records the exit under the helper lock before it reads the hook, and
// an armed hook therefore always runs.
func (s *supervisor) installLocked(h *helper) (armed, adopted bool) {
	if s.closed || s.h != nil || s.state == stateDown {
		return false, false
	}
	// fails is deliberately kept: it is reset by markProven, once the helper
	// has answered a call.
	s.h, s.state, s.last, s.proven = h, stateUp, nil, false
	s.upSince, s.inflight, s.callDied = s.now(), 0, nil
	armed = h.setOnExit(s.died)
	s.fireLocked()
	return armed, true
}

// statusErr explains the current state to a caller.
func (s *supervisor) statusErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unavailableLocked()
}

// died is the reaper's exit hook. It runs on the reaper goroutine, with the
// helper's lock released, and returns before the helper's done channel
// closes: the detach and the state change are therefore visible to anyone
// waiting on that channel, while the slow part (starting a replacement) runs
// on its own goroutine.
func (s *supervisor) died(dead *helper) {
	s.mu.Lock()
	if s.h != dead {
		s.mu.Unlock()
		return // already detached by close() or replaced
	}
	s.h = nil
	duringCall := s.inflight > 0 || s.callDied == dead
	s.inflight, s.callDied = 0, nil
	now := s.now()
	idle := !duringCall && (s.proven || now.Sub(s.upSince) >= s.healthy())
	switch {
	case s.closed:
	case idle:
		// Nothing failed: the process was lost between requests after a
		// healthy period (an OOM kill, an operator). Restart with back-off,
		// but bound a crash loop by a per-hour budget instead.
		recent := s.idleDeaths[:0]
		for _, at := range s.idleDeaths {
			if now.Sub(at) < idleWindow {
				recent = append(recent, at)
			}
		}
		s.idleDeaths = append(recent, now)
		s.idleBack = len(s.idleDeaths) - 1
		if len(s.idleDeaths) > s.idleLimit() {
			s.state = stateDown
		}
	default:
		// It died during a call or before proving itself: a failed start,
		// however well it loaded, so the back-off escalates and the count
		// moves toward the terminal state.
		s.fails++
		if s.fails >= s.downLimit() {
			s.state = stateDown
		}
	}
	if s.closed {
		s.state = stateOff
		s.fireLocked()
		s.mu.Unlock()
		return
	}
	if s.state == stateDown {
		s.fireLocked()
		s.mu.Unlock()
		log.Printf("[tc] %s helper gone, not restarting (down after %d failed restarts)", s.mode, s.fails)
		return
	}
	s.state = stateRestart
	if s.last == nil {
		s.last = errors.New("helper process exited")
	}
	launch := !s.loop
	s.loop = true
	s.fireLocked()
	s.mu.Unlock()
	log.Printf("[tc] %s helper gone, restarting", s.mode)
	if launch {
		go s.restartLoop()
	}
}

// stopLocked hands the restart loop the channel that shutdown closes.
func (s *supervisor) stopLocked() chan struct{} {
	if s.stop == nil {
		s.stop = make(chan struct{})
	}
	return s.stop
}

// restartLoop is the single restart pass: exponential back-off, one spawn at
// a time, and an exit as soon as a helper is in place, the supervisor is
// closed, or maxDown spawns have failed.
func (s *supervisor) restartLoop() {
	s.mu.Lock()
	stop := s.stopLocked()
	s.mu.Unlock()
	for {
		s.mu.Lock()
		if s.closed || s.h != nil || s.state == stateDown {
			// Down is terminal: reloading a model this machine cannot hold,
			// over and over, is how a machine gets brought to its knees.
			s.loop = false
			s.mu.Unlock()
			return
		}
		d, sleep := s.backoffOf(s.fails+s.idleBack), s.sleepFn
		s.mu.Unlock()

		if sleep != nil {
			_ = sleep(d)
		} else if d > 0 {
			select {
			case <-time.After(d):
			case <-stop:
			}
		}

		s.mu.Lock()
		if s.closed || s.h != nil || s.state == stateDown {
			s.loop = false
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		h, err := s.spawn()
		s.mu.Lock()
		if s.closed || s.h != nil {
			s.loop = false
			s.mu.Unlock()
			if h != nil {
				_ = h.close()
			}
			return
		}
		if err != nil {
			s.fails++
			s.last = err
			if s.fails >= s.downLimit() {
				s.state = stateDown
				log.Printf("[tc] %s helper: %d restarts failed, giving up: %v", s.mode, s.fails, err)
			}
			s.fireLocked()
			s.mu.Unlock()
			continue
		}
		tried := s.fails
		armed, adopted := s.installLocked(h)
		s.loop = false
		live := s.h != nil && !s.closed
		s.mu.Unlock()
		if !adopted {
			_ = h.close()
			if !live {
				log.Printf("[tc] %s helper: not adopting a replacement (%s)", s.mode, s.status())
			}
			return
		}
		log.Printf("[tc] %s helper restarted after %d failed attempt(s)", s.mode, tried)
		if !armed {
			s.died(h)
		}
		return
	}
}

// start puts a helper in place and waits for it. Used at construction, where
// a model that cannot be loaded must be reported to whoever asked for it.
func (s *supervisor) start() error {
	s.mu.Lock()
	if s.closed {
		err := s.unavailableLocked()
		s.mu.Unlock()
		return err
	}
	if s.h != nil || s.starting || s.loop {
		s.mu.Unlock()
		return nil
	}
	s.starting = true
	s.mu.Unlock()

	h, err := s.spawn()
	return s.adopt(h, err)
}

// ensure makes sure a helper is usable, or says why it is not. policy tells
// it what the caller can afford: a call that ran into the death waits and
// gets retried, an arrival while nothing has failed yet may wait, and a
// latency-bound pass does not wait at all.
func (s *supervisor) ensure(ctx context.Context, policy int) error {
	for {
		s.mu.Lock()
		if s.closed {
			err := s.unavailableLocked()
			s.mu.Unlock()
			return err
		}
		if s.h != nil {
			s.mu.Unlock()
			return nil
		}
		if s.state == stateDown {
			// Terminal: the model does not fit or the helper cannot start.
			// Reloading it on every request would only repeat the failure.
			err := s.unavailableLocked()
			s.mu.Unlock()
			return err
		}
		if !s.loop && !s.starting {
			// Nobody is starting one: do it here, outside the lock, so
			// health checks and other calls are not blocked by the load.
			s.starting = true
			s.mu.Unlock()
			h, err := s.spawn()
			if aerr := s.adopt(h, err); aerr != nil {
				return aerr
			}
			continue
		}
		if policy != waitAlways && !(policy == waitIfPromising && s.fails == 0) {
			err := s.unavailableLocked()
			s.mu.Unlock()
			return err
		}
		ready, win := s.ready, s.window()
		if ready == nil {
			s.ready = make(chan struct{})
			ready = s.ready
		}
		s.mu.Unlock()
		return s.awaitHelper(ctx, ready, win)
	}
}

// awaitHelper watches for a replacement inside the retry window. The lock is
// not held while waiting.
func (s *supervisor) awaitHelper(ctx context.Context, ready chan struct{}, win time.Duration) error {
	deadline := time.NewTimer(win)
	defer deadline.Stop()
	for {
		select {
		case <-ready:
			s.mu.Lock()
			if s.h != nil {
				s.mu.Unlock()
				return nil
			}
			if s.closed || s.state == stateDown {
				err := s.unavailableLocked()
				s.mu.Unlock()
				return err
			}
			if s.ready == nil {
				s.ready = make(chan struct{})
			}
			ready = s.ready
			s.mu.Unlock()
		case <-deadline.C:
			s.mu.Lock()
			if s.h != nil { // woken between the timer and the lock
				s.mu.Unlock()
				return nil
			}
			err := s.unavailableLocked()
			s.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// call runs one operation on the helper, starting or replacing it as needed.
// It is the policy for calls that can afford to wait out a restart (an
// utterance refinement at the end of a dictation).
func (s *supervisor) call(ctx context.Context, op byte, pcm []int16) (reply, error) {
	return s.run(ctx, waitIfPromising, op, pcm)
}

// callNow is call without waiting for a restart. The batch pass of a
// streaming session runs 'R', 'A' and 'F' back to back and cannot restart in
// the middle of itself, so it hands the whole pass back to the caller with
// the sentinel and lets the next one run on the replacement.
func (s *supervisor) callNow(ctx context.Context, op byte, pcm []int16) (reply, error) {
	return s.run(ctx, noWait, op, pcm)
}

func (s *supervisor) run(ctx context.Context, policy int, op byte, pcm []int16) (reply, error) {
	if err := ctx.Err(); err != nil {
		return reply{}, err
	}
	if err := s.ensure(ctx, policy); err != nil {
		return reply{}, err
	}
	hp, err := s.forCall()
	if err != nil {
		return reply{}, err
	}
	s.mu.Lock()
	if s.h == hp {
		s.inflight++
	}
	s.mu.Unlock()
	rep, err := hp.call(op, pcm)
	if err == nil {
		s.endCall(hp, false)
		s.markProven(hp)
		return rep, nil
	}
	// Did the pipe die, or did the operation itself fail? A helper that
	// answers with an error field is still a helper: the caller's error is
	// the honest one and no restart is warranted.
	if hp.deadErr() == nil {
		if gone, _ := hp.died(); !gone {
			s.endCall(hp, false)
			return rep, err
		}
	}
	s.endCall(hp, true)
	// The pipe broke under this call, but the reaper may not have collected
	// the process yet: until it has, the supervisor still counts it as the
	// live helper. Wait for the reaper (killing a process that closed its
	// pipes yet lingers), so the death is accounted for before looking for a
	// replacement, and the old process is never left behind.
	if !hp.reaped(ctx, s.window()) {
		return reply{}, ctx.Err()
	}
	if policy == noWait {
		// A pass of several dependent operations ('R', 'A'..., 'F') cannot
		// continue on a fresh helper that never saw the earlier ones: the
		// whole pass fails, and a partial result is never a success.
		return reply{}, s.statusErr()
	}
	// This call - and only this call - now waits for the automatic restart
	// and retries once on the replacement.
	if uerr := s.ensure(ctx, waitAlways); uerr != nil {
		return reply{}, uerr
	}
	hp2, ferr := s.forCall()
	if ferr != nil {
		return reply{}, ferr
	}
	if hp2 == hp {
		return rep, err // no replacement: report the original failure
	}
	return hp2.call(op, pcm)
}

// endCall closes the in-flight accounting of one call; broke records that the
// helper's pipe died under it, which the reaper's hook may see only later.
func (s *supervisor) endCall(hp *helper, broke bool) {
	s.mu.Lock()
	if s.h == hp {
		if broke {
			s.callDied = hp
		}
		if s.inflight > 0 {
			s.inflight--
		}
	}
	s.mu.Unlock()
}

// markProven records that hp answered a call: from then on a death of it
// starts the failure count over.
func (s *supervisor) markProven(hp *helper) {
	s.mu.Lock()
	if s.h == hp && !s.proven {
		s.proven, s.fails = true, 0
	}
	s.mu.Unlock()
}

// forCall hands out the current helper.
func (s *supervisor) forCall() (*helper, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.h != nil {
		return s.h, nil
	}
	return nil, s.unavailableLocked()
}

// close stops supervising: no more restarts, and the helper is asked to quit.
// Safe to call more than once and from any goroutine; it never returns an
// error worth acting on, because there is nothing left to do about it.
func (s *supervisor) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.state = stateOff
	dead := s.h
	s.h = nil
	if s.stop != nil && !s.stopClosed {
		close(s.stop)
		s.stopClosed = true
	}
	s.fireLocked()
	s.mu.Unlock()
	if dead != nil {
		_ = dead.close()
	}
	return nil
}
