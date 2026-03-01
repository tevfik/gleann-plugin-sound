package plugin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tevfik/gleann-sound/internal/core"
)

// ---------------------------------------------------------------------------
// Mock implementations for testing the plugin server
// ---------------------------------------------------------------------------

type mockCapturer struct {
	started  bool
	stopped  bool
	onData   func(pcmData []int16)
	startErr error
}

func (m *mockCapturer) Start(_ context.Context, onData func(pcmData []int16)) error {
	if m.startErr != nil {
		return m.startErr
	}
	m.started = true
	m.onData = onData
	return nil
}

func (m *mockCapturer) Stop() error {
	m.stopped = true
	return nil
}

type mockTranscriber struct {
	segments []core.Segment
	err      error
}

func (m *mockTranscriber) TranscribeStream(_ context.Context, pcmData []int16) (string, error) {
	return "mock text", m.err
}

func (m *mockTranscriber) TranscribeStreamSegments(_ context.Context, pcmData []int16) ([]core.Segment, error) {
	return m.segments, m.err
}

func (m *mockTranscriber) TranscribeFile(_ context.Context, filepath string) ([]core.Segment, error) {
	return m.segments, m.err
}

func (m *mockTranscriber) Close() error {
	return nil
}

func (m *mockTranscriber) SetLanguage(_ string) {}

// ---------------------------------------------------------------------------
// GRPCServer tests
// ---------------------------------------------------------------------------

func TestNewGRPCServer(t *testing.T) {
	cap := &mockCapturer{}
	tr := &mockTranscriber{}
	handler := func(event core.TranscriptionEvent) {}

	srv := NewGRPCServer(cap, tr, handler)
	if srv == nil {
		t.Fatal("NewGRPCServer returned nil")
	}
	if srv.capturer == nil {
		t.Error("capturer not set")
	}
	if srv.transcriber == nil {
		t.Error("transcriber not set")
	}
}

func TestGRPCServer_StopWithoutServe(t *testing.T) {
	srv := NewGRPCServer(&mockCapturer{}, &mockTranscriber{}, nil)
	// Stop should be safe even without Serve having been called.
	srv.Stop()
}

func TestGRPCServer_StartTranscription(t *testing.T) {
	cap := &mockCapturer{}
	tr := &mockTranscriber{
		segments: []core.Segment{
			{Start: 0, End: time.Second, Text: "test transcription"},
		},
	}

	var (
		mu             sync.Mutex
		receivedEvents []core.TranscriptionEvent
	)
	handler := func(event core.TranscriptionEvent) {
		mu.Lock()
		receivedEvents = append(receivedEvents, event)
		mu.Unlock()
	}

	srv := NewGRPCServer(cap, tr, handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := srv.StartTranscription(ctx)
	if err != nil {
		t.Fatalf("StartTranscription error: %v", err)
	}

	if !cap.started {
		t.Error("capturer should have been started")
	}

	// Simulate audio data arriving.
	if cap.onData != nil {
		cap.onData([]int16{100, 200, 300, 400, 500})
	}

	// Wait for the 2-second flush timer to trigger.
	// In tests, we wait a bit longer to account for scheduling.
	time.Sleep(3 * time.Second)

	cancel() // Stop the transcription loop.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	n := len(receivedEvents)
	mu.Unlock()
	if n == 0 {
		t.Error("expected at least one transcription event")
	}
}

func TestGRPCServer_StartTranscription_CaptureError(t *testing.T) {
	cap := &mockCapturer{startErr: context.DeadlineExceeded}
	tr := &mockTranscriber{}
	srv := NewGRPCServer(cap, tr, nil)

	err := srv.StartTranscription(context.Background())
	if err == nil {
		t.Error("expected error when capturer fails to start")
	}
}

func TestGRPCServer_ServeInvalidAddr(t *testing.T) {
	srv := NewGRPCServer(&mockCapturer{}, &mockTranscriber{}, nil)
	// Try to listen on an invalid address.
	err := srv.Serve("invalid-address-no-port")
	if err == nil {
		t.Error("expected error for invalid address")
		srv.Stop()
	}
}

func TestGRPCServer_ServeAndStop(t *testing.T) {
	srv := NewGRPCServer(&mockCapturer{}, &mockTranscriber{}, nil)

	// Serve on a random available port.
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve("localhost:0")
	}()

	// Give the server time to start.
	time.Sleep(200 * time.Millisecond)

	// Stop should cleanly shut it down.
	srv.Stop()

	select {
	case err := <-errCh:
		// grpc.Server.Serve returns nil on GracefulStop.
		if err != nil {
			t.Logf("Serve returned: %v (may be expected)", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Serve did not return after Stop")
	}
}

// ---------------------------------------------------------------------------
// SegmentToProto tests
// ---------------------------------------------------------------------------

func TestSegmentToProto(t *testing.T) {
	seg := core.Segment{
		Start: 1500 * time.Millisecond,
		End:   3200 * time.Millisecond,
		Text:  "hello world",
	}

	proto := SegmentToProto(seg)

	if proto.StartMs != 1500 {
		t.Errorf("StartMs: want 1500, got %d", proto.StartMs)
	}
	if proto.EndMs != 3200 {
		t.Errorf("EndMs: want 3200, got %d", proto.EndMs)
	}
	if proto.Text != "hello world" {
		t.Errorf("Text: want %q, got %q", "hello world", proto.Text)
	}
}

func TestSegmentToProto_Zero(t *testing.T) {
	seg := core.Segment{}
	proto := SegmentToProto(seg)

	if proto.StartMs != 0 || proto.EndMs != 0 || proto.Text != "" {
		t.Errorf("zero segment should produce zero proto, got: %+v", proto)
	}
}
