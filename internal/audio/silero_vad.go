//go:build onnx
// +build onnx

package audio

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/ortrt"
)

// Compile-time check: SileroVAD satisfies core.VADProvider.
var _ core.VADProvider = (*SileroVAD)(nil)

// SileroVAD implements core.VADProvider using the Silero VAD v5 ONNX model.
//
// Silero VAD is a ~2MB neural network that classifies audio chunks as
// speech/non-speech with much higher accuracy than energy-based VAD.
// It maintains internal LSTM state (h, c) across calls for temporal context.
//
// Model inputs:
//
//	"input"  [1, chunk_size] float32  — audio samples (16kHz)
//	"sr"     [1] int64               — sample rate (16000)
//	"h"      [2, 1, 64] float32      — LSTM hidden state
//	"c"      [2, 1, 64] float32      — LSTM cell state
//
// Model outputs:
//
//	"output" [1, 1] float32           — speech probability [0, 1]
//	"hn"     [2, 1, 64] float32      — updated hidden state
//	"cn"     [2, 1, 64] float32      — updated cell state
type SileroVAD struct {
	mu      sync.Mutex
	session *ort.DynamicAdvancedSession

	// v5 models take (input, state, sr) with a 64-sample context in front of
	// every 512-sample chunk; v4 models take (input, sr, h, c).
	v5      bool
	state   []float32 // v5: [2*1*128]
	context []float32 // v5: last 64 samples of the previous chunk

	// LSTM state carried across calls (v4).
	hData []float32 // [2*1*64 = 128]
	cData []float32 // [2*1*64 = 128]

	// Schmitt trigger hysteresis for stable speech detection.
	triggered      bool
	enterThreshold float32 // probability to enter speech state (default 0.5)
	exitThreshold  float32 // probability to exit speech state (default 0.35)

	// Accumulation buffer for resampling to Silero's required chunk size.
	// Silero v5 with 16kHz expects 512-sample chunks.
	accumBuf []int16
}

const (
	sileroChunkSize = 512 // samples per Silero inference (32ms @ 16kHz)
	sileroStateSize = 128 // 2 * 1 * 64
	sileroContext   = 64  // v5: samples of left context per chunk
)

// NewSileroVAD loads the Silero VAD ONNX model and returns a ready-to-use VAD.
//
// modelPath should point to the silero_vad.onnx file.
// Download from: https://github.com/snakers4/silero-vad/raw/master/src/silero_vad/data/silero_vad.onnx
func NewSileroVAD(modelPath string) (*SileroVAD, error) {
	if err := ortrt.Ensure(); err != nil {
		return nil, fmt.Errorf("silero vad: %w", err)
	}

	// The upstream model changed its interface between v4 and v5; detect it.
	v5 := false
	if ins, _, err := ort.GetInputOutputInfo(modelPath); err == nil {
		for _, in := range ins {
			if in.Name == "state" {
				v5 = true
			}
		}
	}
	inputNames := []string{"input", "sr", "h", "c"}
	outputNames := []string{"output", "hn", "cn"}
	if v5 {
		inputNames = []string{"input", "state", "sr"}
		outputNames = []string{"output", "stateN"}
	}
	session, err := ort.NewDynamicAdvancedSession(modelPath, inputNames, outputNames, nil)
	if err != nil {
		return nil, fmt.Errorf("silero vad: failed to create session: %w", err)
	}

	v := &SileroVAD{
		session:        session,
		v5:             v5,
		state:          make([]float32, 2*128),
		context:        make([]float32, sileroContext),
		hData:          make([]float32, sileroStateSize),
		cData:          make([]float32, sileroStateSize),
		enterThreshold: 0.5,
		exitThreshold:  0.35,
	}

	log.Printf("[silero-vad] model loaded from: %s", modelPath)
	return v, nil
}

// IsSpeech returns true if the given PCM chunk likely contains speech.
// Chunks are accumulated to sileroChunkSize (512 samples) before running
// inference.  Returns the latest classification for sub-chunk inputs.
//
// Thread-safe: may be called from the audio callback goroutine.
func (v *SileroVAD) IsSpeech(pcm []int16) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.accumBuf = append(v.accumBuf, pcm...)

	// Process all complete chunks in the accumulation buffer.
	var lastResult bool
	processed := false
	for len(v.accumBuf) >= sileroChunkSize {
		chunk := v.accumBuf[:sileroChunkSize]
		v.accumBuf = v.accumBuf[sileroChunkSize:]

		prob := v.inferChunk(chunk)
		lastResult = v.applyHysteresis(prob)
		processed = true
	}

	if !processed {
		// Not enough data yet — return current triggered state.
		return v.triggered
	}
	return lastResult
}

// Reset clears LSTM state and accumulation buffer.
func (v *SileroVAD) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()

	clear(v.state)
	clear(v.context)
	for i := range v.hData {
		v.hData[i] = 0
	}
	for i := range v.cData {
		v.cData[i] = 0
	}
	v.accumBuf = v.accumBuf[:0]
	v.triggered = false
}

// Close releases ONNX resources.
func (v *SileroVAD) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.session != nil {
		v.session.Destroy()
		v.session = nil
	}
	return nil
}

// inferChunk runs a single Silero inference on a 512-sample chunk.
// Returns the speech probability [0, 1].
func (v *SileroVAD) inferChunk(pcm []int16) float32 {
	if v.v5 {
		return v.inferChunkV5(pcm)
	}
	// Convert int16 → float32.
	audioData := make([]float32, sileroChunkSize)
	for i, s := range pcm {
		audioData[i] = float32(s) / 32768.0
	}

	// Create input tensors.
	inputTensor, err := ort.NewTensor(ort.NewShape(1, int64(sileroChunkSize)), audioData)
	if err != nil {
		log.Printf("[silero-vad] input tensor error: %v", err)
		return 0
	}
	defer inputTensor.Destroy()

	srData := []int64{int64(WhisperSampleRate)}
	srTensor, err := ort.NewTensor(ort.NewShape(1), srData)
	if err != nil {
		log.Printf("[silero-vad] sr tensor error: %v", err)
		return 0
	}
	defer srTensor.Destroy()

	hTensor, err := ort.NewTensor(ort.NewShape(2, 1, 64), v.hData)
	if err != nil {
		log.Printf("[silero-vad] h tensor error: %v", err)
		return 0
	}
	defer hTensor.Destroy()

	cTensor, err := ort.NewTensor(ort.NewShape(2, 1, 64), v.cData)
	if err != nil {
		log.Printf("[silero-vad] c tensor error: %v", err)
		return 0
	}
	defer cTensor.Destroy()

	// Run inference with auto-allocated outputs.
	outputs := []ort.Value{nil, nil, nil}
	err = v.session.Run(
		[]ort.Value{inputTensor, srTensor, hTensor, cTensor},
		outputs,
	)
	if err != nil {
		log.Printf("[silero-vad] inference error: %v", err)
		return 0
	}

	// Extract speech probability.
	var prob float32
	if outputTensor, ok := outputs[0].(*ort.Tensor[float32]); ok {
		data := outputTensor.GetData()
		if len(data) > 0 {
			prob = data[0]
		}
	}
	outputs[0].Destroy()

	// Update LSTM hidden state.
	if hnTensor, ok := outputs[1].(*ort.Tensor[float32]); ok {
		copy(v.hData, hnTensor.GetData())
	}
	outputs[1].Destroy()

	// Update LSTM cell state.
	if cnTensor, ok := outputs[2].(*ort.Tensor[float32]); ok {
		copy(v.cData, cnTensor.GetData())
	}
	outputs[2].Destroy()

	return prob
}

// applyHysteresis implements Schmitt trigger logic for stable speech detection.
// Uses two thresholds to prevent rapid toggling at speech boundaries.
func (v *SileroVAD) applyHysteresis(prob float32) bool {
	if !v.triggered && prob >= v.enterThreshold {
		v.triggered = true
	} else if v.triggered && prob < v.exitThreshold {
		v.triggered = false
	}
	return v.triggered
}

// inferChunkV5 runs the v5 model: input is the previous 64 samples followed by
// the 512-sample chunk, state is one [2,1,128] tensor.
func (v *SileroVAD) inferChunkV5(pcm []int16) float32 {
	in := make([]float32, sileroContext+sileroChunkSize)
	copy(in, v.context)
	for i, s := range pcm {
		in[sileroContext+i] = float32(s) / 32768.0
	}
	copy(v.context, in[len(in)-sileroContext:])

	inputTensor, err := ort.NewTensor(ort.NewShape(1, int64(len(in))), in)
	if err != nil {
		log.Printf("[silero-vad] input tensor error: %v", err)
		return 0
	}
	defer inputTensor.Destroy()
	stateTensor, err := ort.NewTensor(ort.NewShape(2, 1, 128), v.state)
	if err != nil {
		log.Printf("[silero-vad] state tensor error: %v", err)
		return 0
	}
	defer stateTensor.Destroy()
	srTensor, err := ort.NewScalar(int64(WhisperSampleRate))
	if err != nil {
		log.Printf("[silero-vad] sr tensor error: %v", err)
		return 0
	}
	defer srTensor.Destroy()

	outputs := []ort.Value{nil, nil}
	if err := v.session.Run([]ort.Value{inputTensor, stateTensor, srTensor}, outputs); err != nil {
		log.Printf("[silero-vad] inference error: %v", err)
		return 0
	}
	var prob float32
	if t, ok := outputs[0].(*ort.Tensor[float32]); ok && len(t.GetData()) > 0 {
		prob = t.GetData()[0]
	}
	outputs[0].Destroy()
	if t, ok := outputs[1].(*ort.Tensor[float32]); ok {
		copy(v.state, t.GetData())
	}
	outputs[1].Destroy()
	return prob
}

// OpenVAD returns the best available voice activity detector: Silero when its
// model is present (it copes with quiet speakers and background noise), the
// energy VAD otherwise. The returned func releases it.
func OpenVAD() (core.VADProvider, func()) {
	for _, p := range sileroCandidates() {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if v, err := NewSileroVAD(p); err == nil {
			return v, func() { _ = v.Close() }
		} else {
			log.Printf("[vad] silero unavailable (%v); using the energy detector", err)
		}
		break
	}
	return DefaultVAD(), func() {}
}

func sileroCandidates() []string {
	var c []string
	if p := os.Getenv("GLEANN_SILERO_VAD"); p != "" {
		c = append(c, p)
	}
	if home, err := os.UserHomeDir(); err == nil {
		c = append(c, filepath.Join(home, ".gleann", "models", "silero_vad.onnx"))
	}
	return c
}
