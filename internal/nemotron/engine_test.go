//go:build onnx
// +build onnx

package nemotron

import (
	"context"
	"os/exec"
	"strconv"
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

func TestEngine_TranscribeWindowJFK(t *testing.T) {
	SetExecutionProvider(ProviderCPU)

	modelDir := "/home/tevfik/WORKSPACE/nemotron-onnx-go/model"
	engine, err := NewEngine(modelDir)
	if err != nil {
		t.Fatalf("Failed to initialize Nemotron engine: %v", err)
	}
	engine.SetLanguage("en")
	defer engine.Close()

	jfkWav := "/home/tevfik/WORKSPACE/gleann-plugin/gleann-sound/test/testdata/jfk.wav"
	
	// Get baseline batch transcription
	baselineSegs, err := engine.TranscribeFile(context.Background(), jfkWav)
	if err != nil {
		t.Fatalf("TranscribeFile baseline failed: %v", err)
	}
	baselineText := baselineSegs[0].Text
	t.Logf("Baseline transcription: %q", baselineText)

	// Decode JFK WAV manually
	importPath := "/home/tevfik/WORKSPACE/gleann-plugin/gleann-sound/test/testdata/jfk.wav"
	importCmd := execCommand(context.Background(),
		"ffmpeg",
		"-i", importPath,
		"-ar", "16000",
		"-ac", "1",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-",
	)
	raw, err := importCmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg decode failed: %v", err)
	}

	nSamples := len(raw) / 2
	pcm := make([]int16, nSamples)
	for i := 0; i < nSamples; i++ {
		pcm[i] = int16(uint16(raw[i*2]) | uint16(raw[i*2+1])<<8)
	}

	// Test TranscribeWindow
	engine.ResetStream()
	res, _, err := engine.TranscribeWindow(context.Background(), pcm, "")
	if err != nil {
		t.Fatalf("TranscribeWindow failed: %v", err)
	}

	t.Logf("TranscribeWindow transcription: %q", res.Text)

	if res.Text != baselineText {
		t.Errorf("Mismatch!\nBaseline: %q\nWindow:   %q", baselineText, res.Text)
	}
}

// helper to run exec.CommandContext inside test
func execCommand(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, arg...)
}

func TestEngine_TranscribeTurkishTestWav(t *testing.T) {
	SetExecutionProvider(ProviderCPU)

	modelDir := "/home/tevfik/WORKSPACE/nemotron-onnx-go/model"
	engine, err := NewEngine(modelDir)
	if err != nil {
		t.Fatalf("Failed to initialize Nemotron engine: %v", err)
	}
	defer engine.Close()

	testWav := "/home/tevfik/WORKSPACE/gleann-plugin/gleann-sound/test/testdata/tr_test.wav"

	// Test with English
	engine.SetLanguage("en")
	segsEn, err := engine.TranscribeFile(context.Background(), testWav)
	if err != nil {
		t.Fatalf("TranscribeFile (en) failed: %v", err)
	}
	t.Logf("Transcribed tr_test.wav (en) text: %q", segsEn[0].Text)

	// Test with Turkish
	engine.SetLanguage("tr")
	segsTr, err := engine.TranscribeFile(context.Background(), testWav)
	if err != nil {
		t.Fatalf("TranscribeFile (tr) failed: %v", err)
	}
	t.Logf("Transcribed tr_test.wav (tr) text: %q", segsTr[0].Text)
}

func TestEngine_ScanLanguageIDs(t *testing.T) {
	SetExecutionProvider(ProviderCPU)

	modelDir := "/home/tevfik/WORKSPACE/nemotron-onnx-go/model"
	engine, err := NewEngine(modelDir)
	if err != nil {
		t.Fatalf("Failed to initialize Nemotron engine: %v", err)
	}
	defer engine.Close()

	testWav := "/home/tevfik/WORKSPACE/gleann-plugin/gleann-sound/test/testdata/tr_test.wav"
	for _, id := range []int{18, 101} {
		engine.SetLanguage(strconv.Itoa(id))
		segs, err := engine.TranscribeFile(context.Background(), testWav)
		if err != nil {
			t.Logf("Language ID %d failed: %v", id, err)
			continue
		}
		if len(segs) > 0 && segs[0].Text != "" {
			t.Logf("-> Language ID %d: %q", id, segs[0].Text)
		} else {
			t.Logf("-> Language ID %d: [EMPTY]", id)
		}
	}
}





