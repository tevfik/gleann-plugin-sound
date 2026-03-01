package audio

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// MalgoCapturer unit tests
// ---------------------------------------------------------------------------
//
// NOTE: These tests exercise the API contract and error paths of MalgoCapturer.
// Tests that actually open audio devices require hardware access and are skipped
// in headless CI environments (no PulseAudio / PipeWire).

func TestNewMalgoCapturer(t *testing.T) {
	c := NewMalgoCapturer()
	if c == nil {
		t.Fatal("NewMalgoCapturer returned nil")
	}
	if c.running {
		t.Error("new capturer should not be running")
	}
}

func TestMalgoCapturer_StopWhenNotRunning(t *testing.T) {
	c := NewMalgoCapturer()
	// Stop on an uninitialised capturer should be a no-op.
	if err := c.Stop(); err != nil {
		t.Errorf("Stop on idle capturer should return nil, got: %v", err)
	}
}

func TestMalgoCapturer_StartRequiresHardware(t *testing.T) {
	// Attempt to start capture — this will fail in environments without
	// audio hardware (CI, containers) but should return a clean error.
	c := NewMalgoCapturer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.Start(ctx, func(pcm []int16) {})
	if err != nil {
		// Expected in headless environments — just log it.
		t.Logf("Start returned expected error (no audio device): %v", err)
		return
	}

	// If it succeeded, we're on a real machine — clean up.
	defer c.Stop()

	// Should not be able to start twice.
	err = c.Start(ctx, func(pcm []int16) {})
	if err == nil {
		t.Error("starting an already-running capturer should return an error")
	}
}

func TestMalgoCapturer_ContextCancelsCapture(t *testing.T) {
	c := NewMalgoCapturer()
	ctx, cancel := context.WithCancel(context.Background())

	err := c.Start(ctx, func(pcm []int16) {})
	if err != nil {
		t.Skipf("skipping context cancellation test — no audio device: %v", err)
	}

	// Cancel context — should stop the capturer asynchronously.
	cancel()
	time.Sleep(200 * time.Millisecond)

	// After context cancel, the capturer should have stopped.
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if running {
		t.Error("capturer should have stopped after context cancellation")
		c.Stop()
	}
}

// TestWhisperConstants verifies the exported audio format constants.
func TestWhisperConstants(t *testing.T) {
	if WhisperSampleRate != 16000 {
		t.Errorf("WhisperSampleRate: want 16000, got %d", WhisperSampleRate)
	}
	if WhisperChannels != 1 {
		t.Errorf("WhisperChannels: want 1, got %d", WhisperChannels)
	}
	if WhisperBitsPerSample != 16 {
		t.Errorf("WhisperBitsPerSample: want 16, got %d", WhisperBitsPerSample)
	}
}
