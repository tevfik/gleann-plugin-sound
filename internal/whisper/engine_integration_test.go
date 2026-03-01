//go:build whisper
// +build whisper

package whisper_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/tevfik/gleann-sound/internal/whisper"
)

// TestEngine_TranscribeFile_JFK verifies that the whisper engine can
// transcribe the JFK speech sample correctly.  This test requires:
//   - Build tag: whisper
//   - Model:     models/ggml-base.en.bin (or set WHISPER_MODEL env)
//   - Test data: testdata/jfk.wav
//
// Run:
//
//	CGO_CFLAGS="..." CGO_LDFLAGS="..." go test -tags whisper -v -run TestEngine_TranscribeFile_JFK ./internal/whisper/
func TestEngine_TranscribeFile_JFK(t *testing.T) {
	modelPath := os.Getenv("WHISPER_MODEL")
	if modelPath == "" {
		modelPath = "../../models/ggml-base.en.bin"
	}

	wavPath := "../../testdata/jfk.wav"

	// Skip if model or test data is missing.
	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		t.Skipf("model not found: %s (set WHISPER_MODEL env or run: make whisper-model)", modelPath)
	}
	if _, err := os.Stat(wavPath); os.IsNotExist(err) {
		t.Skipf("test WAV not found: %s", wavPath)
	}

	engine, err := whisper.NewEngine(modelPath)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer engine.Close()

	ctx := context.Background()
	segments, err := engine.TranscribeFile(ctx, wavPath)
	if err != nil {
		t.Fatalf("TranscribeFile: %v", err)
	}

	if len(segments) == 0 {
		t.Fatal("expected at least one segment")
	}

	// Concatenate all segment texts.
	var texts []string
	for _, seg := range segments {
		t.Logf("segment [%v-%v]: %s", seg.Start, seg.End, seg.Text)
		texts = append(texts, seg.Text)
	}
	full := strings.ToLower(strings.Join(texts, " "))

	// Check that key phrases are present.
	expected := []string{"fellow americans", "ask not", "your country"}
	for _, phrase := range expected {
		if !strings.Contains(full, phrase) {
			t.Errorf("expected phrase %q not found in transcription: %s", phrase, full)
		}
	}
}
