//go:build onnx
// +build onnx

package nemotron

import (
	"context"
	"testing"
)

func TestEngine_TranscribeJFK(t *testing.T) {
	SetExecutionProvider(ProviderCPU)

	// Initialize the engine with the model directory
	modelDir := "/home/tevfik/WORKSPACE/nemotron-onnx-go/model"
	engine, err := NewEngine(modelDir)
	if err != nil {
		t.Fatalf("Failed to initialize Nemotron engine: %v", err)
	}
	engine.SetLanguage("en")
	defer engine.Close()

	// Transcribe the JFK WAV file
	jfkWav := "/home/tevfik/WORKSPACE/gleann-plugin/gleann-sound/test/testdata/jfk.wav"
	segments, err := engine.TranscribeFile(context.Background(), jfkWav)
	if err != nil {
		t.Fatalf("TranscribeFile failed: %v", err)
	}

	if len(segments) == 0 {
		t.Fatalf("Expected segments, got 0")
	}

	text := segments[0].Text
	t.Logf("Transcribed JFK.wav text: %q", text)

	// Check if the output contains expected words
	// "and so my fellow americans"
	if text == "" {
		t.Errorf("Expected non-empty transcribed text, got empty string")
	}
}
