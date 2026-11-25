package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// P-06: a requested shutdown is not an error. Serve must return nil (the
// process then exits 0), also when Shutdown runs before Serve has started.
func TestServeReturnsNilAfterShutdown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	s := New(nil, "tr", port)
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve() }()
	for i := 0; i < 100; i++ { // wait until it listens
		if c, err := net.Dial("tcp", l.Addr().String()); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Serve returned %v after a requested shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestShutdownBeforeServe(t *testing.T) {
	s := New(nil, "tr", 0)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Serve(); err != nil {
		t.Fatalf("Serve after Shutdown: %v", err)
	}
	if err := s.Close(); err != nil { // idempotent
		t.Fatal(err)
	}
}

// blockingEngine stands in for a transcription that ignores its client and
// only ends when the server revokes the request's root context or closes the
// engine: exactly the shape of a hung ffmpeg child that must die at the drain
// deadline, not after the host's kill.
type blockingEngine struct {
	blocked chan struct{} // closed once TranscribeFile enters
}

func (e *blockingEngine) TranscribeStream(context.Context, []int16) (string, error) { return "", nil }
func (e *blockingEngine) TranscribeStreamSegments(context.Context, []int16) ([]core.Segment, error) {
	return nil, nil
}
func (e *blockingEngine) TranscribeFile(context.Context, string) ([]core.Segment, error) {
	close(e.blocked)
	select {} // parked until the engine Close unblocks the conn and the root context cancels
}
func (e *blockingEngine) SetLanguage(string) {}
func (e *blockingEngine) Close() error       { return nil }

type recordingRefiner struct {
	closed atomic.Bool
}

func (r *recordingRefiner) Refine(context.Context, []int16) (string, error) { return "x", nil }
func (r *recordingRefiner) Close() error {
	r.closed.Store(true)
	return nil
}
func (r *recordingRefiner) Alive() bool { return !r.closed.Load() }

// S3: a /live session holds a hijacked connection whose request context only
// dies with the connection itself. Shutdown must revoke the server's root
// context when the drain deadline passes, so both /convert handlers and /live
// streams stop at that moment and their upload temp files are cleaned up.
func TestShutdownCancelsRequestsAtDrainDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	oldRoot := uploadRoot
	uploadRoot = t.TempDir()
	defer func() { uploadRoot = oldRoot }()

	be := &blockingEngine{blocked: make(chan struct{})}
	s := New(func() (core.Transcriber, error) { return be, nil }, "tr", port)
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve() }()

	// Wait for the listener and start an upload that parks in TranscribeFile.
	for i := 0; i < 200; i++ {
		if c, derr := net.Dial("tcp", ln.Addr().String()); derr == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	respCh := make(chan *http.Response, 1)
	go func() {
		r, perr := postUpload(fmt.Sprintf("http://127.0.0.1:%d", port))
		if perr != nil {
			respCh <- nil // the server pulled the connection out from under us
			return
		}
		respCh <- r
	}()
	select {
	case <-be.blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("upload never reached the engine")
	}

	drain := 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	start := time.Now()
	if err := s.Shutdown(ctx); err == nil {
		t.Error("Shutdown should report the hung in-flight request (drain deadline hit)")
	}
	// Old behavior: a 20 s drain outliving the host's 10 s kill. New: the
	// drain deadline plus, in the worst case (engine wedged), closeGrace.
	if d := time.Since(start); d < drain || d > drain+closeGrace+2*time.Second {
		t.Errorf("Shutdown waited %v, want <= %v", d, drain+closeGrace+2*time.Second)
	}

	// The parked handler's root context must be cancelled by the drain
	// deadline; a response (however it ends) proves the handler returned.
	select {
	case resp := <-respCh:
		// The parked handler ends only when the connection dies: the client
		// sees EOF (nil) or an error status. Either proves the request was
		// torn down at the drain deadline instead of surviving Shutdown.
		if resp != nil {
			resp.Body.Close()
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight /convert still parked after Shutdown: request context not cancelled")
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Serve returned %v after Shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}

	// The upload temp file must not linger.
	left, _ := filepath.Glob(filepath.Join(s.uploadDir(), "*"))
	if len(left) != 0 {
		t.Errorf("upload temp files left after shutdown: %v", left)
	}
}

// S3: Shutdown releases the refiner's sidecar helper before returning, not
// only through a defer that the hard-exit path may skip.
func TestShutdownClosesRefiner(t *testing.T) {
	r := &recordingRefiner{}
	s := New(nil, "en", 0)
	s.SetRefiner(r)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.closed.Load() {
		t.Error("Shutdown must close the refiner (no helper leak on the hard-exit path)")
	}
	if err := s.Close(); err != nil { // idempotent, second Close tolerated
		t.Fatal(err)
	}
}

// A Close that never returns (wedged refiner) must not block its caller past
// closeGrace, wherever it is called from (release, RunE's deferred close).
func TestBoundedCloseAbandonsWedgedClose(t *testing.T) {
	old := closeGrace
	closeGrace = 100 * time.Millisecond
	defer func() { closeGrace = old }()
	block := make(chan struct{})
	defer close(block)
	start := time.Now()
	err := BoundedClose("refiner", func() error { <-block; return nil })
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("BoundedClose blocked %v", d)
	}
	if err != nil {
		t.Errorf("abandoned close returned %v", err)
	}
	want := errors.New("boom")
	if got := BoundedClose("x", func() error { return want }); !errors.Is(got, want) {
		t.Errorf("BoundedClose swallowed the error: %v", got)
	}
}

// After release abandoned a wedged close, the second release (Shutdown then
// the deferred Close) must not wait another closeGrace.
func TestSecondReleaseIsNoopAfterAbandon(t *testing.T) {
	old := closeGrace
	closeGrace = 150 * time.Millisecond
	defer func() { closeGrace = old }()
	block := make(chan struct{})
	defer close(block)
	s := New(nil, "en", 0)
	s.SetRefiner(&wedgedRefiner{block: block})
	_ = s.release() // abandons after closeGrace
	start := time.Now()
	_ = s.release()
	if d := time.Since(start); d > closeGrace/2 {
		t.Errorf("second release waited %v", d)
	}
}

type wedgedRefiner struct{ block chan struct{} }

func (r *wedgedRefiner) Refine(context.Context, []int16) (string, error) { return "", nil }
func (r *wedgedRefiner) Close() error                                    { <-r.block; return nil }
func (r *wedgedRefiner) Alive() bool                                     { return true }
