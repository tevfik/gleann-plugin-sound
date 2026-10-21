//go:build onnx
// +build onnx

package speaker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

func cosine(a, b []float32) float64 {
	var d float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
	}
	return d
}

// Two recordings of different people must be further apart than two halves of
// the same recording.
func TestEmbedderSeparatesVoices(t *testing.T) {
	if testing.Short() {
		t.Skip("model-backed test")
	}
	home, _ := os.UserHomeDir()
	model := filepath.Join(home, ".gleann", "models", "nemo_en_titanet_small.onnx")
	if _, err := os.Stat(model); err != nil {
		t.Skipf("model missing: %s", model)
	}
	emb, err := NewEmbedder(model)
	if err != nil {
		t.Fatal(err)
	}
	defer emb.Close()

	load := func(name string) []int16 {
		pcm, err := core.DecodeFile(context.Background(), filepath.Join("..", "..", "test", "testdata", name))
		if err != nil {
			t.Skip(err)
		}
		return pcm
	}
	jfk, tr := load("jfk.wav"), load("tr_test.wav")
	a1, _ := emb.Embed(jfk[:len(jfk)/2])
	a2, _ := emb.Embed(jfk[len(jfk)/2:])
	b, err := emb.Embed(tr)
	if err != nil {
		t.Fatal(err)
	}
	same, diff := cosine(a1, a2), cosine(a1, b)
	t.Logf("same speaker %.2f, different speaker %.2f", same, diff)
	if len(a1) != 192 || same <= diff+0.2 {
		t.Errorf("embeddings do not separate speakers: same=%.2f diff=%.2f", same, diff)
	}
	if _, err := emb.Embed(jfk[:1000]); err == nil {
		t.Error("audio under 0.5 s must be rejected")
	}
}
