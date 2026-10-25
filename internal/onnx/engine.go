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

	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

const (
	sampleRate  = 16000
	nFFT        = 400
	hopLength   = 160
	nMels       = 80
	chunkLength = 30 // seconds
	maxTokens   = 448

	// maxStreamTokens limits the decoder loop for streaming windows.
	// Streaming windows are 5-10s, producing at most ~40 tokens of text.
	// Keeping this low drastically reduces the O(n²) decoder cost when
	// there is no KV cache.
	maxStreamTokens = 80

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

// ExecutionProvider selects the hardware provider for ONNX inference.
const (
	ProviderAuto = "auto" // Try CUDA first, fallback to CPU (default).
	ProviderCUDA = "cuda" // Force CUDA GPU acceleration.
	ProviderCPU  = "cpu"  // Force CPU-only inference.
)

// executionProvider is the package-level setting for the execution provider.
// Set via SetExecutionProvider() before calling NewEngine().
var executionProvider = ProviderAuto

// SetExecutionProvider configures the ONNX Runtime execution provider.
// Must be called before NewEngine(). Valid values: "auto", "cuda", "cpu".
func SetExecutionProvider(provider string) {
	switch provider {
	case ProviderCUDA, ProviderCPU, ProviderAuto:
		executionProvider = provider
	default:
		executionProvider = ProviderAuto
	}
	log.Printf("[onnx] execution provider set to: %s", executionProvider)
}

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
	provider string // actual execution provider used ("cuda" or "cpu")

	// streamPrompt holds the previous window's text for streaming context.
	streamPrompt string
}

// Compile-time interface checks.
var _ core.Transcriber = (*Engine)(nil)
var _ core.StreamingTranscriber = (*Engine)(nil)

func init() {
	core.RegisterBackend("onnx", func(model string) (core.Transcriber, error) {
		return NewEngine(model)
	})
}

// discoverONNXLibrary attempts to find the ONNX Runtime shared library.
// Returns true if a library was found and SetSharedLibraryPath was called.
// Checks: env vars → ~/.gleann/lib/ → system paths → app bundles → Python packages.
// Prefers CUDA-capable builds over CPU-only builds.
func discoverONNXLibrary() bool {
	// 1. Honour explicit env vars.
	for _, envKey := range []string{"ORT_SHARED_LIBRARY_PATH", "ORT_LIB_PATH"} {
		if p := os.Getenv(envKey); p != "" {
			log.Printf("[onnx] using library from %s=%s", envKey, p)
			ort.SetSharedLibraryPath(p)
			return true
		}
	}

	// 2. Check gleann's own library cache (~/.gleann/lib/).
	if libPath := config.ONNXRuntimePath(); libPath != "" {
		if _, err := os.Stat(libPath); err == nil {
			log.Printf("[onnx] using cached library: %s", libPath)
			ort.SetSharedLibraryPath(libPath)
			return true
		}
	}

	// 2.5. Check pip-installed ONNX Runtime (~/.gleann/lib/onnxrt-pip/).
	if libPath := config.FindPipInstalledONNXRuntime(); libPath != "" {
		isCUDA := config.HasPipInstalledCUDAProvider()
		if isCUDA {
			log.Printf("[onnx] found pip-installed CUDA ONNX Runtime: %s", libPath)
		} else {
			log.Printf("[onnx] found pip-installed ONNX Runtime: %s", libPath)
		}
		ort.SetSharedLibraryPath(libPath)
		return true
	}

	// 3. Collect all candidate libraries, then pick the best one.
	//    CUDA-capable builds are preferred over CPU-only builds.
	searchGlobs := []string{
		// Standard system library paths.
		"/usr/lib/libonnxruntime.so",
		"/usr/lib/libonnxruntime.so.*",
		"/usr/local/lib/libonnxruntime.so",
		"/usr/local/lib/libonnxruntime.so.*",
		"/usr/lib/x86_64-linux-gnu/libonnxruntime.so",
		"/usr/lib/x86_64-linux-gnu/libonnxruntime.so.*",
		"/usr/lib/aarch64-linux-gnu/libonnxruntime.so.*",
		// macOS paths.
		"/usr/local/lib/libonnxruntime.dylib",
		"/usr/local/lib/libonnxruntime.*.dylib",
		"/opt/homebrew/lib/libonnxruntime.dylib",
		"/opt/homebrew/lib/libonnxruntime.*.dylib",
	}

	// Python site-packages (may be CPU or GPU depending on pip package).
	if home, err := os.UserHomeDir(); err == nil {
		pyGlobs := []string{
			home + "/.local/lib/python*/site-packages/onnxruntime/capi/libonnxruntime.so.*",
			home + "/.local/share/pipx/venvs/*/lib/python*/site-packages/onnxruntime/capi/libonnxruntime.so.*",
		}
		searchGlobs = append(searchGlobs, pyGlobs...)
	}

	// LD_LIBRARY_PATH entries — user may have set this to point to a CUDA build.
	if ldPath := os.Getenv("LD_LIBRARY_PATH"); ldPath != "" {
		for _, dir := range strings.Split(ldPath, ":") {
			if dir != "" {
				searchGlobs = append(searchGlobs, filepath.Join(dir, "libonnxruntime.so"))
				searchGlobs = append(searchGlobs, filepath.Join(dir, "libonnxruntime.so.*"))
			}
		}
	}

	var cpuCandidate string // fallback: first found library
	for _, pattern := range searchGlobs {
		matches, _ := filepath.Glob(pattern)
		for _, path := range matches {
			if _, err := os.Stat(path); err != nil {
				continue
			}
			// Check if a CUDA provider .so exists alongside this library.
			dir := filepath.Dir(path)
			cudaProvider := filepath.Join(dir, "libonnxruntime_providers_cuda.so")
			if _, err := os.Stat(cudaProvider); err == nil {
				log.Printf("[onnx] found CUDA-capable ONNX Runtime: %s", path)
				ort.SetSharedLibraryPath(path)
				return true
			}
			if cpuCandidate == "" {
				cpuCandidate = path
			}
		}
	}

	// No CUDA build found — use CPU-only fallback.
	if cpuCandidate != "" {
		log.Printf("[onnx] found ONNX Runtime library (CPU-only): %s", cpuCandidate)
		ort.SetSharedLibraryPath(cpuCandidate)
		return true
	}

	return false
}

// NewEngine loads the ONNX whisper model from the given directory.
// The directory must contain encoder.onnx, decoder.onnx, and tokenizer.json.
func NewEngine(modelDir string) (*Engine, error) {
	// Auto-discover ONNX Runtime library location.
	if !discoverONNXLibrary() {
		// Not found anywhere — attempt auto-download.
		log.Printf("[onnx] runtime not found on system, downloading...")
		libPath, dlErr := config.DownloadONNXRuntime()
		if dlErr != nil {
			return nil, fmt.Errorf("onnx: runtime not found and auto-download failed: %w", dlErr)
		}
		ort.SetSharedLibraryPath(libPath)
	}

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

	// ── Session options with execution provider selection ──────
	sessionOpts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("onnx: failed to create session options: %w", err)
	}
	defer sessionOpts.Destroy()

	actualProvider := "cpu"
	switch executionProvider {
	case ProviderCUDA:
		// Force CUDA — fail loudly if not available.
		cudaOpts, cudaErr := ort.NewCUDAProviderOptions()
		if cudaErr != nil {
			return nil, fmt.Errorf("onnx: CUDA requested but not available: %w", cudaErr)
		}
		if err := sessionOpts.AppendExecutionProviderCUDA(cudaOpts); err != nil {
			cudaOpts.Destroy()
			return nil, fmt.Errorf("onnx: CUDA provider failed: %w", err)
		}
		cudaOpts.Destroy()
		actualProvider = "cuda"
		log.Println("[onnx] execution provider: CUDA (forced)")

	case ProviderCPU:
		// Force CPU — no GPU attempt.
		log.Println("[onnx] execution provider: CPU (forced)")

	default: // ProviderAuto
		// Try CUDA first, fall back to CPU silently.
		cudaOpts, cudaErr := ort.NewCUDAProviderOptions()
		if cudaErr == nil {
			if err := sessionOpts.AppendExecutionProviderCUDA(cudaOpts); err != nil {
				log.Printf("[onnx] CUDA auto-detect failed, using CPU: %v", err)
			} else {
				actualProvider = "cuda"
				log.Println("[onnx] execution provider: CUDA (auto-detected)")
			}
			cudaOpts.Destroy()
		} else {
			log.Printf("[onnx] CUDA not available, using CPU: %v", cudaErr)
		}
	}

	// Create encoder session (DynamicAdvancedSession for auto-allocated outputs).
	// Input:  "input_features" [1, nMels, 3000] float32
	// Output: "last_hidden_state" [1, 1500, dim] float32
	encoderInputNames := []string{"input_features"}
	encoderOutputNames := []string{"last_hidden_state"}
	encoder, err := ort.NewDynamicAdvancedSession(encoderPath, encoderInputNames, encoderOutputNames, sessionOpts)
	if err != nil {
		return nil, fmt.Errorf("onnx: failed to create encoder session: %w", err)
	}

	// Create decoder session (DynamicAdvancedSession for variable-length inputs).
	// Non-merged decoder (decoder_model.onnx): simple two-input model.
	// Inputs:  "input_ids" [1, seq] int64,
	//          "encoder_hidden_states" [1, 1500, dim] float32
	// Output:  "logits" [1, seq, n_vocab] float32
	decoderInputNames := []string{"input_ids", "encoder_hidden_states"}
	decoderOutputNames := []string{"logits"}
	decoder, err := ort.NewDynamicAdvancedSession(decoderPath, decoderInputNames, decoderOutputNames, sessionOpts)
	if err != nil {
		encoder.Destroy()
		return nil, fmt.Errorf("onnx: failed to create decoder session: %w", err)
	}

	// Build mel filterbank.
	melFilters := buildMelFilterbank(sampleRate, nFFT, nMels)

	log.Printf("[onnx] model loaded from: %s (%d vocab tokens, provider: %s)", modelDir, len(vocab), actualProvider)

	return &Engine{
		encoder:    encoder,
		decoder:    decoder,
		vocab:      vocab,
		melFilters: melFilters,
		modelDir:   modelDir,
		provider:   actualProvider,
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

	// Pad or truncate to [nMels, 3000] (30s chunk) row-by-row to preserve layout.
	const melFrames = 3000
	padded := make([]float32, nMels*melFrames)
	numSamples := len(samples)
	if numSamples < nFFT {
		numSamples = nFFT
	}
	numFrames := (numSamples-nFFT)/hopLength + 1
	if numFrames < 1 {
		numFrames = 1
	}
	copyFrames := numFrames
	if copyFrames > melFrames {
		copyFrames = melFrames
	}
	for m := 0; m < nMels; m++ {
		srcOffset := m * numFrames
		dstOffset := m * melFrames
		copy(padded[dstOffset:dstOffset+copyFrames], melSpec[srcOffset:srcOffset+copyFrames])
	}

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

	if text == "" || core.IsHallucination(text) || core.IsRepetitive(text) {
		if text != "" {
			log.Printf("[onnx] filtered hallucination: %q", text)
		}
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

// TranscribeWindow processes a single sliding window of PCM data with context
// from the previous transcription.  The promptText conditions the decoder
// by prepending prompt tokens to the initial token sequence.
func (e *Engine) TranscribeWindow(ctx context.Context, pcmData []int16, promptText string) (core.StreamResult, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(pcmData) == 0 {
		return core.StreamResult{}, promptText, nil
	}

	// Convert int16 → float32.
	samples := make([]float32, len(pcmData))
	for i, s := range pcmData {
		samples[i] = float32(s) / 32768.0
	}

	// Compute log-mel spectrogram.
	melSpec := e.computeMelSpectrogram(samples)

	// Pad or truncate to [nMels, 3000] (30s chunk) row-by-row to preserve layout.
	const melFrames = 3000
	padded := make([]float32, nMels*melFrames)
	numSamples := len(samples)
	if numSamples < nFFT {
		numSamples = nFFT
	}
	numFrames := (numSamples-nFFT)/hopLength + 1
	if numFrames < 1 {
		numFrames = 1
	}
	copyFrames := numFrames
	if copyFrames > melFrames {
		copyFrames = melFrames
	}
	for m := 0; m < nMels; m++ {
		srcOffset := m * numFrames
		dstOffset := m * melFrames
		copy(padded[dstOffset:dstOffset+copyFrames], melSpec[srcOffset:srcOffset+copyFrames])
	}

	// Run encoder.
	encoderInput, err := ort.NewTensor(ort.NewShape(1, int64(nMels), int64(melFrames)), padded)
	if err != nil {
		return core.StreamResult{}, promptText, fmt.Errorf("onnx encoder input: %w", err)
	}
	defer encoderInput.Destroy()

	encoderOutputs := []ort.Value{nil}
	if err := e.encoder.Run([]ort.Value{encoderInput}, encoderOutputs); err != nil {
		return core.StreamResult{}, promptText, fmt.Errorf("onnx encoder run: %w", err)
	}
	encoderOutput := encoderOutputs[0]
	defer encoderOutput.Destroy()

	// Build initial tokens with optional prompt context.
	tokens := e.buildInitialTokens()

	// If we have prompt text from the previous window, tokenize it as context.
	// We use a simple approach: encode each word as a vocabulary lookup.
	if promptText != "" {
		promptTokens := e.tokenizePrompt(promptText)
		if len(promptTokens) > 0 {
			// Insert prompt tokens after SOT+lang but before transcribe+notimestamps.
			// Format: [SOT, lang?, ...prompt..., transcribe, notimestamps]
			insertPos := 1 // after SOT
			if e.lang != "" {
				if _, ok := languageTokens[e.lang]; ok {
					insertPos = 2 // after SOT + lang
				}
			}
			prefix := make([]int64, insertPos)
			copy(prefix, tokens[:insertPos])
			suffix := tokens[insertPos:]
			tokens = append(prefix, promptTokens...)
			tokens = append(tokens, suffix...)
		}
	}

	durSec := float64(len(pcmData)) / float64(sampleRate)

	// Use reduced token limit for streaming — windows are short and
	// the decoder cost is O(n²) without KV cache.
	for i := 0; i < maxStreamTokens; i++ {
		if ctx.Err() != nil {
			break
		}

		seqLen := int64(len(tokens))
		tokenData := make([]int64, len(tokens))
		copy(tokenData, tokens)

		inputIDs, err := ort.NewTensor(ort.NewShape(1, seqLen), tokenData)
		if err != nil {
			return core.StreamResult{}, promptText, fmt.Errorf("onnx decoder input: %w", err)
		}

		decoderOutputs := []ort.Value{nil}
		err = e.decoder.Run([]ort.Value{inputIDs, encoderOutput}, decoderOutputs)
		inputIDs.Destroy()
		if err != nil {
			return core.StreamResult{}, promptText, fmt.Errorf("onnx decoder run: %w", err)
		}

		logitsValue := decoderOutputs[0]
		logitsShape := logitsValue.GetShape()
		nVocab := logitsShape[len(logitsShape)-1]

		logitsTensor, ok := logitsValue.(*ort.Tensor[float32])
		if !ok {
			logitsValue.Destroy()
			return core.StreamResult{}, promptText, fmt.Errorf("onnx: unexpected logits type")
		}
		logitsData := logitsTensor.GetData()
		lastPos := (seqLen - 1) * nVocab
		logits := logitsData[lastPos : lastPos+nVocab]

		nextToken := argmax(logits)
		logitsValue.Destroy()

		if nextToken == eotToken {
			break
		}
		tokens = append(tokens, int64(nextToken))
	}

	text := e.decodeTokens(tokens)
	text = strings.TrimSpace(text)

	// Filter hallucinations and repetitive output.
	if text == "" || core.IsHallucination(text) || core.IsRepetitive(text) {
		if text != "" {
			log.Printf("[onnx] filtered hallucination: %q (%.1fs audio)", text, durSec)
		}
		return core.StreamResult{}, promptText, nil
	}

	// Note: ONNX engine uses noTimestamps token, so per-segment timestamps
	// are not available.  result.Segments stays nil; the pipeline will
	// fall back to text-based deduplication.
	result := core.StreamResult{
		Text: text,
		End:  time.Duration(durSec * float64(time.Second)),
	}

	// Carry forward: last ~50 chars as prompt for next window.
	// Keep short to reduce decoder bias and prompt poisoning risk.
	nextPrompt := text
	if len(nextPrompt) > 50 {
		nextPrompt = nextPrompt[len(nextPrompt)-50:]
		if idx := strings.Index(nextPrompt, " "); idx >= 0 {
			nextPrompt = nextPrompt[idx+1:]
		}
	}

	log.Printf("[onnx] streaming window: %q (%.1fs audio)", text, durSec)
	return result, nextPrompt, nil
}

// tokenizePrompt does a simple greedy longest-match tokenization of prompt
// text against the vocabulary.  This is approximate but sufficient for
// decoder conditioning.  Returns at most 32 tokens.
func (e *Engine) tokenizePrompt(text string) []int64 {
	if len(e.vocab) == 0 || text == "" {
		return nil
	}

	// Build a simple lookup for greedy matching.
	var tokens []int64
	remaining := text
	for len(remaining) > 0 && len(tokens) < 32 {
		bestLen := 0
		bestToken := int64(-1)
		// Try matching from the vocabulary (greedy longest match).
		for id, word := range e.vocab {
			if int64(id) >= sotToken {
				continue // skip special tokens
			}
			if len(word) > bestLen && strings.HasPrefix(remaining, word) {
				bestLen = len(word)
				bestToken = int64(id)
			}
		}
		if bestToken >= 0 {
			tokens = append(tokens, bestToken)
			remaining = remaining[bestLen:]
		} else {
			// Skip one byte if no match found.
			remaining = remaining[1:]
		}
	}
	return tokens
}

// ResetStream clears accumulated streaming context.
func (e *Engine) ResetStream() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.streamPrompt = ""
	log.Println("[onnx] stream context reset")
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
//
// Uses Cooley-Tukey FFT (O(N log N) per frame) instead of naive DFT (O(N²)),
// providing ~17× speedup for the STFT computation.
func (e *Engine) computeMelSpectrogram(samples []float32) []float32 {
	// Pad to at least nFFT.
	if len(samples) < nFFT {
		padded := make([]float32, nFFT)
		copy(padded, samples)
		samples = padded
	}

	numFrames := (len(samples)-nFFT)/hopLength + 1
	if numFrames < 1 {
		numFrames = 1
	}

	fftBins := nFFT/2 + 1
	magnitudes := make([]float32, numFrames*fftBins)

	// Hann window (computed once).
	window := make([]float32, nFFT)
	for i := range window {
		window[i] = float32(0.5 * (1.0 - math.Cos(2.0*math.Pi*float64(i)/float64(nFFT))))
	}

	// Reusable FFT work buffers (avoid allocation per frame).
	fftReal := make([]float64, fftPaddedSize)
	fftImag := make([]float64, fftPaddedSize)
	frameMag := make([]float32, fftBins)

	for frame := 0; frame < numFrames; frame++ {
		offset := frame * hopLength
		end := offset + nFFT
		if end > len(samples) {
			end = len(samples)
		}
		frameData := samples[offset:end]

		fftMagnitudeSquared(frameData, window, fftReal, fftImag, frameMag)
		copy(magnitudes[frame*fftBins:], frameMag)
	}

	// Apply mel filterbank.
	mel := make([]float32, nMels*numFrames)
	var maxVal float32 = -1e20
	for m := 0; m < nMels; m++ {
		for frame := 0; frame < numFrames; frame++ {
			var sum float32
			for k := 0; k < fftBins; k++ {
				sum += e.melFilters[m*fftBins+k] * magnitudes[frame*fftBins+k]
			}
			// Log scale with floor.
			if sum < 1e-10 {
				sum = 1e-10
			}
			val := float32(math.Log10(float64(sum)))
			mel[m*numFrames+frame] = val
			if val > maxVal {
				maxVal = val
			}
		}
	}

	// Dynamic range clipping and rescaling to [-1, 1] range: (log_mel + 4.0) / 4.0
	clipFloor := maxVal - 8.0
	for i := range mel {
		val := mel[i]
		if val < clipFloor {
			val = clipFloor
		}
		mel[i] = (val + 4.0) / 4.0
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
