//go:build onnx
// +build onnx

package speaker

import (
	"fmt"
	"math"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/tevfik/gleann-plugin-sound/internal/ortrt"
)

// Embedder turns speech into a unit-length voice embedding with TitaNet-small
// (192 dimensions).
type Embedder struct {
	mu   sync.Mutex
	sess *ort.DynamicAdvancedSession
}

const (
	embedDim     = 192
	maxEmbedSec  = 10 // longer audio is embedded in windows and averaged
	minEmbedSamp = sampleRate / 2
)

// NewEmbedder loads the ONNX model (nemo_en_titanet_small.onnx).
func NewEmbedder(model string) (*Embedder, error) {
	if err := ortrt.Ensure(); err != nil {
		return nil, fmt.Errorf("speaker: %w", err)
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer opts.Destroy()
	_ = opts.SetIntraOpNumThreads(2)
	_ = opts.SetInterOpNumThreads(1)
	_ = opts.AddSessionConfigEntry("session.intra_op.allow_spinning", "0")
	sess, err := ort.NewDynamicAdvancedSession(model,
		[]string{"audio_signal", "length"}, []string{"embs"}, opts)
	if err != nil {
		return nil, fmt.Errorf("speaker: load %s: %w", model, err)
	}
	return &Embedder{sess: sess}, nil
}

// Close releases the model.
func (e *Embedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess != nil {
		e.sess.Destroy()
		e.sess = nil
	}
	return nil
}

// Embed returns the unit-length embedding of pcm (>= 0.5 s). Audio longer than
// 10 s is split into windows whose embeddings are averaged.
func (e *Embedder) Embed(pcm []int16) ([]float32, error) {
	if len(pcm) < minEmbedSamp {
		return nil, fmt.Errorf("speaker: need at least 0.5 s of audio, got %d samples", len(pcm))
	}
	win := maxEmbedSec * sampleRate
	acc := make([]float64, embedDim)
	n := 0
	for off := 0; off < len(pcm); off += win {
		end := min(off+win, len(pcm))
		if end-off < minEmbedSamp {
			break // a tiny tail adds noise, not information
		}
		v, err := e.embedOne(pcm[off:end])
		if err != nil {
			return nil, err
		}
		for i, x := range v {
			acc[i] += float64(x)
		}
		n++
	}
	return normalize(acc), nil
}

func (e *Embedder) embedOne(pcm []int16) ([]float32, error) {
	x := make([]float32, len(pcm))
	for i, s := range pcm {
		x[i] = float32(s) / 32768
	}
	feats := Features(x)
	if feats == nil {
		return nil, fmt.Errorf("speaker: audio too short")
	}
	frames := len(feats[0])
	flat := make([]float32, numMels*frames)
	for m := range feats {
		copy(flat[m*frames:], feats[m])
	}
	in, err := ort.NewTensor(ort.NewShape(1, numMels, int64(frames)), flat)
	if err != nil {
		return nil, err
	}
	defer in.Destroy()
	ln, err := ort.NewTensor(ort.NewShape(1), []int64{int64(frames)})
	if err != nil {
		return nil, err
	}
	defer ln.Destroy()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess == nil {
		return nil, fmt.Errorf("speaker: embedder closed")
	}
	out := []ort.Value{nil}
	if err := e.sess.Run([]ort.Value{in, ln}, out); err != nil {
		return nil, fmt.Errorf("speaker: embed: %w", err)
	}
	defer out[0].Destroy()
	t, ok := out[0].(*ort.Tensor[float32])
	if !ok || len(t.GetData()) < embedDim {
		return nil, fmt.Errorf("speaker: unexpected embedding output")
	}
	v := make([]float64, embedDim)
	for i := range v {
		v[i] = float64(t.GetData()[i])
	}
	return normalize(v), nil
}

func normalize(v []float64) []float32 {
	var n float64
	for _, x := range v {
		n += x * x
	}
	n = math.Sqrt(n)
	out := make([]float32, len(v))
	if n == 0 {
		return out
	}
	for i, x := range v {
		out[i] = float32(x / n)
	}
	return out
}
