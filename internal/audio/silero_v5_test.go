//go:build onnx
// +build onnx

package audio

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// The Silero v5 interface (input/state/sr) must work and tell speech from
// silence; it replaces the energy detector, which loses quiet speakers.
func TestSileroV5DetectsSpeech(t *testing.T) {
	if testing.Short() {
		t.Skip("model-backed test")
	}
	home, _ := os.UserHomeDir()
	model := filepath.Join(home, ".gleann", "models", "silero_vad.onnx")
	if _, err := os.Stat(model); err != nil {
		t.Skipf("model missing: %s", model)
	}
	v, err := NewSileroVAD(model)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if !v.v5 {
		t.Skip("model is not v5")
	}
	pcm, err := core.DecodeFile(context.Background(), filepath.Join("..", "..", "test", "testdata", "jfk.wav"))
	if err != nil {
		t.Skip(err)
	}
	frac := func(x []int16) float64 {
		v.Reset()
		hit, n := 0, 0
		for i := 0; i+480 <= len(x); i += 480 {
			if v.IsSpeech(x[i : i+480]) {
				hit++
			}
			n++
		}
		return float64(hit) / float64(n)
	}
	speech, silence := frac(pcm), frac(make([]int16, 16000*3))
	t.Logf("speech frames %.0f%%, silence frames %.0f%%", speech*100, silence*100)
	if speech < 0.6 {
		t.Errorf("only %.0f%% of a speech recording flagged as speech", speech*100)
	}
	if silence > 0.01 {
		t.Errorf("%.0f%% of digital silence flagged as speech", silence*100)
	}
}
