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

// ── AudioSource parsing ────────────────────────────────────────

func TestParseAudioSource(t *testing.T) {
	tests := []struct {
		input   string
		want    AudioSource
		wantErr bool
	}{
		{"mic", SourceMic, false},
		{"microphone", SourceMic, false},
		{"speaker", SourceSpeaker, false},
		{"loopback", SourceSpeaker, false},
		{"desktop", SourceSpeaker, false},
		{"both", SourceBoth, false},
		{"all", SourceBoth, false},
		{"invalid", SourceMic, true},
		{"", SourceMic, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseAudioSource(tt.input)
			if tt.wantErr && err == nil {
				t.Errorf("ParseAudioSource(%q): expected error", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ParseAudioSource(%q): unexpected error: %v", tt.input, err)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseAudioSource(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestAudioSourceString(t *testing.T) {
	tests := []struct {
		src  AudioSource
		want string
	}{
		{SourceMic, "mic"},
		{SourceSpeaker, "speaker"},
		{SourceBoth, "both"},
	}
	for _, tt := range tests {
		if got := tt.src.String(); got != tt.want {
			t.Errorf("AudioSource(%d).String() = %q, want %q", tt.src, got, tt.want)
		}
	}
}

// ── Constructor tests ──────────────────────────────────────────

func TestNewMalgoCapturer(t *testing.T) {
	c := NewMalgoCapturer()
	if c == nil {
		t.Fatal("NewMalgoCapturer returned nil")
	}
	if c.running {
		t.Error("new capturer should not be running")
	}
	if c.source != SourceMic {
		t.Errorf("default source should be SourceMic, got %v", c.source)
	}
}

func TestNewMalgoCapturerWithSource(t *testing.T) {
	for _, src := range []AudioSource{SourceMic, SourceSpeaker, SourceBoth} {
		c := NewMalgoCapturerWithSource(src)
		if c == nil {
			t.Fatalf("NewMalgoCapturerWithSource(%v) returned nil", src)
		}
		if c.source != src {
			t.Errorf("source = %v, want %v", c.source, src)
		}
	}
}

// ── Lifecycle tests ────────────────────────────────────────────

func TestMalgoCapturer_StopWhenNotRunning(t *testing.T) {
	c := NewMalgoCapturer()
	// Stop on an uninitialised capturer should be a no-op.
	if err := c.Stop(); err != nil {
		t.Errorf("Stop on idle capturer should return nil, got: %v", err)
	}
}

func TestMalgoCapturer_StartMic(t *testing.T) {
	c := NewMalgoCapturerWithSource(SourceMic)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.Start(ctx, func(pcm []int16) {})
	if err != nil {
		t.Logf("Start(mic) returned expected error (no audio device): %v", err)
		return
	}
	defer c.Stop()

	// Should not be able to start twice.
	err = c.Start(ctx, func(pcm []int16) {})
	if err == nil {
		t.Error("starting an already-running capturer should return an error")
	}
}

func TestMalgoCapturer_StartSpeaker(t *testing.T) {
	c := NewMalgoCapturerWithSource(SourceSpeaker)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.Start(ctx, func(pcm []int16) {})
	if err != nil {
		// Loopback may not be supported on all platforms.
		t.Logf("Start(speaker) returned expected error: %v", err)
		return
	}
	defer c.Stop()
}

func TestMalgoCapturer_StartBoth(t *testing.T) {
	c := NewMalgoCapturerWithSource(SourceBoth)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.Start(ctx, func(pcm []int16) {})
	if err != nil {
		t.Logf("Start(both) returned expected error: %v", err)
		return
	}
	defer c.Stop()
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

// ── Constants ──────────────────────────────────────────────────

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
