package httpserver

import (
	"context"
	"net"
	"testing"
	"time"
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
