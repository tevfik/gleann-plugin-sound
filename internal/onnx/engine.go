//go:build onnx
// +build onnx

// Package onnx provides an ONNX Runtime-based Whisper transcription engine.
//
// This file is compiled ONLY when the "onnx" build tag is set.
// When building without the tag, the stub in stub.go is used instead.
//
// The implementation uses github.com/yalue/onnxruntime_go to run whisper
// models exported to ONNX format.  This enables GPU acceleration via CUDA,
// DirectML, CoreML providers, or CPU-only inference.
//
// ONNX model directory structure (from https://github.com/openai/whisper):
//
//	model_dir/
//	├── encoder.onnx
//	├── decoder.onnx
//	├── config.json         (model config: n_mels, n_vocab, etc.)
//	└── tokenizer.json      (vocabulary + special tokens)
//
// Build requirements:
//   - ONNX Runtime shared library installed (libonnxruntime.so / .dylib / .dll)
//   - Set ORT_LIB_PATH if not in default library search path
package onnx

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/tevfik/gleann-sound/internal/core"
)

const (
	sampleRate  = 16000
	nFFT        = 400
	hopLength   = 160
	nMels       = 80
	chunkLength = 30 // seconds
	maxTokens   = 448

	// Special token IDs (Whisper standard).
	sotToken       = 50258 // <|startoftranscript|>
	eotToken       = 50257 // <|endoftext|>
	transcribeTask = 50359 // <|transcribe|>
	noTimestamps   = 50363 // <|notimestamps|>
)

// Language token IDs for common languages.
// These are sotToken + 1 + language_index in whisper's vocabulary.
var languageTokens = map[string]int64{
	"en": 50259, "zh": 50260, "de": 50261, "es": 50262,
	"ru": 50263, "ko": 50264, "fr": 50265, "ja": 50266,
	"pt": 50267, "tr": 50268, "pl": 50269, "nl": 50271,
	"ar": 50272, "it": 50274,
}

// ---------------------------------------------------------------------------
// Engine — ONNX Runtime whisper implementation
// ---------------------------------------------------------------------------

// Engine wraps ONNX Runtime encoder/decoder sessions for Whisper inference.
type Engine struct {
	mu      sync.Mutex
	encoder *ort.DynamicAdvancedSession
	decoder *ort.DynamicAdvancedSession
	vocab   []string // token ID → string
	lang    string   // language code

	// Pre-computed mel filterbank weights [nMels × (nFFT/2+1)].
	melFilters []float32

	modelDir string
}

// Compile-time interface check.
var _ core.Transcriber = (*Engine)(nil)

func init() {
	core.RegisterBackend("onnx", func(model string) (core.Transcriber, error) {
		return NewEngine(model)
	})
}

// NewEngine loads the ONNX whisper model from the given directory.
// The directory must contain encoder.onnx, decoder.onnx, and tokenizer.json.
func NewEngine(modelDir string) (*Engine, error) {
	// Initialize ONNX Runtime (idempotent).
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("onnx: failed to initialize runtime: %w", err)
	}

	encoderPath := filepath.Join(modelDir, "encoder.onnx")
	decoderPath := filepath.Join(modelDir, "decoder.onnx")
	tokenizerPath := filepath.Join(modelDir, "tokenizer.json")

	for _, p := range []string{encoderPath, decoderPath, tokenizerPath} {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return nil, fmt.Errorf("onnx: missing required file: %s", p)
		}
	}

	// Load vocabulary.
	vocab, err := loadTokenizer(tokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("onnx: failed to load tokenizer: %w", err)
	}

	// Create encoder session (DynamicAdvancedSession for auto-allocated outputs).
	// Input:  "input_features" [1, nMels, 3000] float32
	// Output: "last_hidden_state" [1, 1500, dim] float32
	encoderInputNames := []string{"input_features"}
	encoderOutputNames := []string{"last_hidden_state"}
	encoder, err := ort.NewDynamicAdvancedSession(encoderPath, encoderInputNames, encoderOutputNames, nil)
	if err != nil {
		return nil, fmt.Errorf("onnx: failed to create encoder session: %w", err)
	}

	// Create decoder session (DynamicAdvancedSession for variable-length inputs).
	// Inputs:  "input_ids" [1, seq] int64, "encoder_hidden_states" [1, 1500, dim] float32
	// Output:  "logits" [1, seq, n_vocab] float32
	decoderInputNames := []string{"input_ids", "encoder_hidden_states"}
	decoderOutputNames := []string{"logits"}
	decoder, err := ort.NewDynamicAdvancedSession(decoderPath, decoderInputNames, decoderOutputNames, nil)
	if err != nil {
		encoder.Destroy()
		return nil, fmt.Errorf("onnx: failed to create decoder session: %w", err)
	}

	// Build mel filterbank.
	melFilters := buildMelFilterbank(sampleRate, nFFT, nMels)

	log.Printf("[onnx] model loaded from: %s (%d vocab tokens)", modelDir, len(vocab))

	return &Engine{
		encoder:    encoder,
		decoder:    decoder,
		vocab:      vocab,
		melFilters: melFilters,
		modelDir:   modelDir,
	}, nil
}

// SetLanguage sets the language for transcription.
func (e *Engine) SetLanguage(lang string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lang = lang
	log.Printf("[onnx] language set to: %q", lang)
}

// TranscribeStream processes raw 16 kHz 16-bit mono PCM and returns text.
func (e *Engine) TranscribeStream(ctx context.Context, pcmData []int16) (string, error) {
	segments, err := e.TranscribeStreamSegments(ctx, pcmData)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, seg := range segments {
		sb.WriteString(seg.Text)
	}
	return strings.TrimSpace(sb.String()), nil
}

// TranscribeStreamSegments processes raw PCM and returns timestamped segments.
func (e *Engine) TranscribeStreamSegments(ctx context.Context, pcmData []int16) ([]core.Segment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(pcmData) == 0 {
		return nil, nil
	}

	// Convert int16 → float32.
	samples := make([]float32, len(pcmData))
	for i, s := range pcmData {
		samples[i] = float32(s) / 32768.0
	}

	// Compute log-mel spectrogram.
	melSpec := e.computeMelSpectrogram(samples)

	// Pad or truncate to [nMels, 3000] (30s chunk).
	const melFrames = 3000
	padded := make([]float32, nMels*melFrames)
	copyLen := len(melSpec)
	if copyLen > nMels*melFrames {
		copyLen = nMels * melFrames
	}
	copy(padded, melSpec[:copyLen])

	// Run encoder.
	encoderInput, err := ort.NewTensor(ort.NewShape(1, int64(nMels), int64(melFrames)), padded)
	if err != nil {
		return nil, fmt.Errorf("onnx encoder input: %w", err)
	}
	defer encoderInput.Destroy()

	// Output is nil → DynamicAdvancedSession auto-allocates it.
	encoderOutputs := []ort.Value{nil}
	if err := e.encoder.Run([]ort.Value{encoderInput}, encoderOutputs); err != nil {
		return nil, fmt.Errorf("onnx encoder run: %w", err)
	}
	// encoderOutputs[0] is now the auto-allocated hidden state tensor.
	encoderOutput := encoderOutputs[0]
	defer encoderOutput.Destroy()

	// Greedy decoder loop.
	tokens := e.buildInitialTokens()
	durSec := float64(len(pcmData)) / float64(sampleRate)

	for i := 0; i < maxTokens; i++ {
		if ctx.Err() != nil {
			break
		}

		// Prepare decoder input_ids [1, seqLen].
		seqLen := int64(len(tokens))
		tokenData := make([]int64, len(tokens))
		copy(tokenData, tokens)

		inputIDs, err := ort.NewTensor(ort.NewShape(1, seqLen), tokenData)
		if err != nil {
			return nil, fmt.Errorf("onnx decoder input: %w", err)
		}

		// Output is nil → auto-allocated by DynamicAdvancedSession.
		decoderOutputs := []ort.Value{nil}
		err = e.decoder.Run(
			[]ort.Value{inputIDs, encoderOutput},
			decoderOutputs,
		)
		inputIDs.Destroy()
		if err != nil {
			return nil, fmt.Errorf("onnx decoder run: %w", err)
		}

		// Extract logits from auto-allocated output.
		logitsValue := decoderOutputs[0]
		logitsShape := logitsValue.GetShape()
		nVocab := logitsShape[len(logitsShape)-1]

		// Cast to typed tensor to access data.
		logitsTensor, ok := logitsValue.(*ort.Tensor[float32])
		if !ok {
			logitsValue.Destroy()
			return nil, fmt.Errorf("onnx: unexpected logits type")
		}
		logitsData := logitsTensor.GetData()

		// Get logits for the last token position.
		lastPos := (seqLen - 1) * nVocab
		logits := logitsData[lastPos : lastPos+nVocab]

		// Greedy argmax.
		nextToken := argmax(logits)
		logitsValue.Destroy()

		if nextToken == eotToken {
			break
		}
		tokens = append(tokens, int64(nextToken))
	}

	// Decode tokens to text.
	text := e.decodeTokens(tokens)
	text = strings.TrimSpace(text)

	if text == "" {
		return nil, nil
	}

	log.Printf("[onnx] transcribed: %q (%.1fs audio)", text, durSec)

	return []core.Segment{
		{
			Start: 0,
			End:   time.Duration(durSec * float64(time.Second)),
			Text:  text,
		},
	}, nil
}

// TranscribeFile transcribes an audio/video file via ffmpeg → PCM → ONNX.
func (e *Engine) TranscribeFile(ctx context.Context, filepath string) ([]core.Segment, error) {
	cmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-i", filepath,
		"-ar", "16000",
		"-ac", "1",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-",
	)
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("onnx: ffmpeg decode failed: %w", err)
	}

	nSamples := len(raw) / 2
	pcm := make([]int16, nSamples)
	for i := 0; i < nSamples; i++ {
		pcm[i] = int16(uint16(raw[i*2]) | uint16(raw[i*2+1])<<8)
	}

	return e.TranscribeStreamSegments(ctx, pcm)
}

// Close releases ONNX Runtime resources.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.encoder != nil {
		e.encoder.Destroy()
		e.encoder = nil
	}
	if e.decoder != nil {
		e.decoder.Destroy()
		e.decoder = nil
	}
	log.Println("[onnx] engine closed")
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// buildInitialTokens creates the initial decoder prompt tokens.
func (e *Engine) buildInitialTokens() []int64 {
	tokens := []int64{sotToken}

	// Language token.
	if e.lang != "" {
		if langTok, ok := languageTokens[e.lang]; ok {
			tokens = append(tokens, langTok)
		}
	}

	// Task = transcribe, no timestamps.
	tokens = append(tokens, transcribeTask, noTimestamps)
	return tokens
}

// decodeTokens converts token IDs to text, skipping special tokens.
func (e *Engine) decodeTokens(tokens []int64) string {
	var sb strings.Builder
	for _, t := range tokens {
		if t >= sotToken { // skip all special tokens
			continue
		}
		if int(t) < len(e.vocab) {
			sb.WriteString(e.vocab[t])
		}
	}
	return sb.String()
}

// argmax returns the index of the maximum value in a float32 slice.
func argmax(logits []float32) int {
	maxIdx := 0
	maxVal := logits[0]
	for i := 1; i < len(logits); i++ {
		if logits[i] > maxVal {
			maxVal = logits[i]
			maxIdx = i
		}
	}
	return maxIdx
}

// ---------------------------------------------------------------------------
// Mel Spectrogram
// ---------------------------------------------------------------------------

// computeMelSpectrogram computes log-mel spectrogram features from audio samples.
// Returns flat [nMels, numFrames] float32.
func (e *Engine) computeMelSpectrogram(samples []float32) []float32 {
	// Pad to at least nFFT.
	if len(samples) < nFFT {
		padded := make([]float32, nFFT)
		copy(padded, samples)
		samples = padded
	}

	numFrames := (len(samples) - nFFT) / hopLength + 1
	if numFrames < 1 {
		numFrames = 1
	}

	// STFT magnitude squared.
	fftSize := nFFT/2 + 1
	magnitudes := make([]float32, numFrames*fftSize)

	// Hann window.
	window := make([]float32, nFFT)
	for i := range window {
		window[i] = float32(0.5 * (1.0 - math.Cos(2.0*math.Pi*float64(i)/float64(nFFT))))
	}

	for frame := 0; frame < numFrames; frame++ {
		offset := frame * hopLength

		// Apply window and compute DFT (real-valued input).
		for k := 0; k < fftSize; k++ {
			var realPart, imagPart float64
			freq := 2.0 * math.Pi * float64(k) / float64(nFFT)
			for n := 0; n < nFFT; n++ {
				if offset+n >= len(samples) {
					break
				}
				val := float64(samples[offset+n]) * float64(window[n])
				angle := freq * float64(n)
				realPart += val * math.Cos(angle)
				imagPart -= val * math.Sin(angle)
			}
			magnitudes[frame*fftSize+k] = float32(realPart*realPart + imagPart*imagPart)
		}
	}

	// Apply mel filterbank.
	mel := make([]float32, nMels*numFrames)
	for m := 0; m < nMels; m++ {
		for frame := 0; frame < numFrames; frame++ {
			var sum float32
			for k := 0; k < fftSize; k++ {
				sum += e.melFilters[m*fftSize+k] * magnitudes[frame*fftSize+k]
			}
			// Log scale with floor.
			if sum < 1e-10 {
				sum = 1e-10
			}
			mel[m*numFrames+frame] = float32(math.Log10(float64(sum)))
		}
	}

	return mel
}

// buildMelFilterbank creates triangular mel filterbank weights.
// Returns [nMels × (nFFT/2+1)] float32.
func buildMelFilterbank(sr, fftSize, numMels int) []float32 {
	fftBins := fftSize/2 + 1

	// Convert Hz to mel scale.
	hzToMel := func(hz float64) float64 {
		return 2595.0 * math.Log10(1.0+hz/700.0)
	}
	melToHz := func(mel float64) float64 {
		return 700.0 * (math.Pow(10.0, mel/2595.0) - 1.0)
	}

	melMin := hzToMel(0)
	melMax := hzToMel(float64(sr) / 2.0)

	// numMels + 2 equally spaced points in mel space.
	melPoints := make([]float64, numMels+2)
	for i := range melPoints {
		melPoints[i] = melMin + (melMax-melMin)*float64(i)/float64(numMels+1)
	}

	// Convert back to Hz then to FFT bin indices.
	binIndices := make([]float64, numMels+2)
	for i, m := range melPoints {
		hz := melToHz(m)
		binIndices[i] = hz * float64(fftSize) / float64(sr)
	}

	// Build triangular filters.
	filters := make([]float32, numMels*fftBins)
	for m := 0; m < numMels; m++ {
		left := binIndices[m]
		center := binIndices[m+1]
		right := binIndices[m+2]

		for k := 0; k < fftBins; k++ {
			fk := float64(k)
			if fk >= left && fk <= center && center > left {
				filters[m*fftBins+k] = float32((fk - left) / (center - left))
			} else if fk > center && fk <= right && right > center {
				filters[m*fftBins+k] = float32((right - fk) / (right - center))
			}
		}
	}

	return filters
}
