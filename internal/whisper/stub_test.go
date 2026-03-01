package whisper

import (
	"context"
	"strings"
	"testing"

	"github.com/tevfik/gleann-sound/internal/core"
)

// ---------------------------------------------------------------------------
// Stub engine tests — verify the no-op transcriber behaves correctly
// ---------------------------------------------------------------------------

func TestNewEngine(t *testing.T) {
	e, err := NewEngine("models/test-model.bin")
	if err != nil {
		t.Fatalf("NewEngine returned unexpected error: %v", err)
	}
	if e == nil {
		t.Fatal("NewEngine returned nil")
	}
	if e.model != "models/test-model.bin" {
		t.Errorf("model path: want %q, got %q", "models/test-model.bin", e.model)
	}
}

func TestEngine_ImplementsTranscriber(t *testing.T) {
	var _ core.Transcriber = (*Engine)(nil)
}

func TestEngine_TranscribeStream(t *testing.T) {
	e, _ := NewEngine("test.bin")

	// 16000 samples = 1 second at 16kHz.
	pcm := make([]int16, 16000)
	text, err := e.TranscribeStream(context.Background(), pcm)
	if err != nil {
		t.Fatalf("TranscribeStream error: %v", err)
	}

	if !strings.Contains(text, "stub") {
		t.Errorf("expected stub marker in output, got: %q", text)
	}
	if !strings.Contains(text, "1.0s") {
		t.Errorf("expected duration in output, got: %q", text)
	}
}

func TestEngine_TranscribeStream_Empty(t *testing.T) {
	e, _ := NewEngine("test.bin")

	text, err := e.TranscribeStream(context.Background(), nil)
	if err != nil {
		t.Fatalf("TranscribeStream error: %v", err)
	}
	if !strings.Contains(text, "0.0s") {
		t.Errorf("empty input should yield 0.0s duration, got: %q", text)
	}
}

func TestEngine_TranscribeStreamSegments(t *testing.T) {
	e, _ := NewEngine("test.bin")

	pcm := make([]int16, 32000) // 2 seconds
	segments, err := e.TranscribeStreamSegments(context.Background(), pcm)
	if err != nil {
		t.Fatalf("TranscribeStreamSegments error: %v", err)
	}

	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}

	seg := segments[0]
	if seg.Start != 0 {
		t.Errorf("segment Start: want 0, got %v", seg.Start)
	}
	if !strings.Contains(seg.Text, "2.0s") {
		t.Errorf("expected 2.0s in segment text, got: %q", seg.Text)
	}
}

func TestEngine_TranscribeFile(t *testing.T) {
	e, _ := NewEngine("test.bin")

	segments, err := e.TranscribeFile(context.Background(), "/tmp/test.wav")
	if err != nil {
		t.Fatalf("TranscribeFile error: %v", err)
	}

	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}

	if !strings.Contains(segments[0].Text, "/tmp/test.wav") {
		t.Errorf("expected file path in stub output, got: %q", segments[0].Text)
	}
}

func TestEngine_Close(t *testing.T) {
	e, _ := NewEngine("test.bin")
	err := e.Close()
	if err != nil {
		t.Errorf("Close returned unexpected error: %v", err)
	}

	// Double close should also be safe.
	err = e.Close()
	if err != nil {
		t.Errorf("double Close returned unexpected error: %v", err)
	}
}

func TestEngine_TranscribeStream_VariousDurations(t *testing.T) {
	e, _ := NewEngine("test.bin")
	ctx := context.Background()

	tests := []struct {
		name     string
		samples  int
		wantDur  string
	}{
		{"zero", 0, "0.0s"},
		{"half_second", 8000, "0.5s"},
		{"one_second", 16000, "1.0s"},
		{"five_seconds", 80000, "5.0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcm := make([]int16, tt.samples)
			text, err := e.TranscribeStream(ctx, pcm)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if !strings.Contains(text, tt.wantDur) {
				t.Errorf("want duration %q in output, got: %q", tt.wantDur, text)
			}
		})
	}
}
