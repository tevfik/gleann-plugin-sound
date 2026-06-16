//go:build onnx
// +build onnx

package nemotron

import (
	"bufio"
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

// Language token IDs for standard languages in Nemotron ASR
var nemotronLangIDs = map[string]int64{
	"bg":    0,  "bg-bg": 0,
	"cs":    1,  "cs-cz": 1,
	"da":    2,  "da-dk": 2,
	"de":    3,  "de-de": 3,
	"el":    4,  "el-gr": 4,
	"et":    5,  "et-ee": 5,
	"fi":    6,  "fi-fi": 6,
	"fr":    7,  "fr-fr": 7,
	"hu":    8,  "hu-hu": 8,
	"hr":    9,  "hr-hr": 9,
	"it":    10, "it-it": 10,
	"lt":    11, "lt-lt": 11,
	"lv":    12, "lv-lv": 12,
	"nl":    13, "nl-nl": 13,
	"pl":    14, "pl-pl": 14,
	"pt":    15, "pt-br": 15,
	"ro":    16, "ro-ro": 16,
	"ru":    17, "ru-ru": 17,
	"sk":    18, "sk-sk": 18,
	"sl":    19, "sl-sl": 19,
	"sv":    20, "sv-se": 20,
	"uk":    21, "uk-ua": 21,
	"ar":    22, "ar-ar": 22,
	"en-gb": 23,
	"en":    24, "en-us": 24,
	"es":    25, "es-es": 25,
	"es-us": 26,
	"fr-ca": 27,
	"he":    28, "he-il": 28,
	"hi":    29, "hi-in": 29,
	"ja":    30, "ja-jp": 30,
	"ko":    31, "ko-kr": 31,
	"nb":    32, "nb-no": 32,
	"nn":    33, "nn-no": 33,
	"pt-pt": 34,
	"th":    35, "th-th": 35,
	"tr":    36, "tr-tr": 36,
	"zh":    37, "zh-cn": 37,
	"vi":    38, "vi-vn": 38,
}

// Execution providers matching onnx package settings
const (
	ProviderAuto = "auto"
	ProviderCUDA = "cuda"
	ProviderCPU  = "cpu"
)

var executionProvider = ProviderAuto

// SetExecutionProvider configures the execution provider.
func SetExecutionProvider(provider string) {
	switch provider {
	case ProviderCUDA, ProviderCPU, ProviderAuto:
		executionProvider = provider
	default:
		executionProvider = ProviderAuto
	}
	log.Printf("[nemotron] execution provider set to: %s", executionProvider)
}

// Engine wraps ONNX Runtime sessions for Nemotron ASR
type Engine struct {
	mu      sync.Mutex
	encoder *ort.DynamicAdvancedSession
	decoder *ort.DynamicAdvancedSession
	joint   *ort.DynamicAdvancedSession
	vocab   []string
	lang    string

	melFilters []float32
	modelDir   string
	provider   string

	// Causal cache variables for streaming dictation window
	cacheChan           []float32
	cacheTime           []float32
	cacheChanLen        []int64
	streamPrompt        string

	// Decoder states and history for streaming dictation window
	decoderH            []float32
	decoderC            []float32
	lastToken           int64
}

// Compile-time interface validation
var _ core.Transcriber = (*Engine)(nil)
var _ core.StreamingTranscriber = (*Engine)(nil)

func init() {
	core.RegisterBackend("nemotron", func(model string) (core.Transcriber, error) {
		return NewEngine(model)
	})
}

// discoverONNXLibrary locates the ONNX Runtime library
func discoverONNXLibrary() bool {
	for _, envKey := range []string{"ORT_SHARED_LIBRARY_PATH", "ORT_LIB_PATH"} {
		if p := os.Getenv(envKey); p != "" {
			log.Printf("[nemotron] using library from %s=%s", envKey, p)
			ort.SetSharedLibraryPath(p)
			return true
		}
	}

	if libPath := config.ONNXRuntimePath(); libPath != "" {
		if _, err := os.Stat(libPath); err == nil {
			log.Printf("[nemotron] using cached library: %s", libPath)
			ort.SetSharedLibraryPath(libPath)
			return true
		}
	}

	if libPath := config.FindPipInstalledONNXRuntime(); libPath != "" {
		ort.SetSharedLibraryPath(libPath)
		return true
	}

	searchGlobs := []string{
		"/usr/lib/libonnxruntime.so",
		"/usr/lib/libonnxruntime.so.*",
		"/usr/local/lib/libonnxruntime.so",
		"/usr/local/lib/libonnxruntime.so.*",
		"/usr/lib/x86_64-linux-gnu/libonnxruntime.so",
		"/usr/lib/x86_64-linux-gnu/libonnxruntime.so.*",
		"/usr/lib/aarch64-linux-gnu/libonnxruntime.so.*",
		"/opt/homebrew/lib/libonnxruntime.dylib",
	}

	if home, err := os.UserHomeDir(); err == nil {
		pyGlobs := []string{
			home + "/.local/lib/python*/site-packages/onnxruntime/capi/libonnxruntime.so.*",
		}
		searchGlobs = append(searchGlobs, pyGlobs...)
	}

	for _, pattern := range searchGlobs {
		matches, _ := filepath.Glob(pattern)
		for _, path := range matches {
			if _, err := os.Stat(path); err == nil {
				log.Printf("[nemotron] found ONNX Runtime library: %s", path)
				ort.SetSharedLibraryPath(path)
				return true
			}
		}
	}

	return false
}

// NewEngine creates a new Nemotron ASR engine session
func NewEngine(modelDir string) (*Engine, error) {
	if !discoverONNXLibrary() {
		log.Printf("[nemotron] runtime library not found, downloading...")
		libPath, dlErr := config.DownloadONNXRuntime()
		if dlErr != nil {
			return nil, fmt.Errorf("nemotron: library not found and download failed: %w", dlErr)
		}
		ort.SetSharedLibraryPath(libPath)
	}

	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("nemotron: failed to initialize runtime env: %w", err)
	}

	encoderPath := filepath.Join(modelDir, "encoder.onnx")
	decoderPath := filepath.Join(modelDir, "decoder.onnx")
	jointPath := filepath.Join(modelDir, "joint.onnx")
	vocabPath := filepath.Join(modelDir, "vocab.txt")

	for _, p := range []string{encoderPath, decoderPath, jointPath, vocabPath} {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return nil, fmt.Errorf("nemotron: missing required file: %s", p)
		}
	}

	vocab, err := loadTokenizer(vocabPath)
	if err != nil {
		return nil, fmt.Errorf("nemotron: failed to load vocabulary: %w", err)
	}

	sessionOpts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("nemotron: failed to create session options: %w", err)
	}
	defer sessionOpts.Destroy()

	actualProvider := "cpu"
	switch executionProvider {
	case ProviderCUDA:
		cudaOpts, cudaErr := ort.NewCUDAProviderOptions()
		if cudaErr != nil {
			return nil, fmt.Errorf("nemotron: CUDA requested but not available: %w", cudaErr)
		}
		if err := sessionOpts.AppendExecutionProviderCUDA(cudaOpts); err != nil {
			cudaOpts.Destroy()
			return nil, fmt.Errorf("nemotron: CUDA provider failed: %w", err)
		}
		cudaOpts.Destroy()
		actualProvider = "cuda"
		log.Println("[nemotron] execution provider: CUDA (forced)")
	case ProviderCPU:
		log.Println("[nemotron] execution provider: CPU (forced)")
	default:
		cudaOpts, cudaErr := ort.NewCUDAProviderOptions()
		if cudaErr == nil {
			if err := sessionOpts.AppendExecutionProviderCUDA(cudaOpts); err != nil {
				log.Printf("[nemotron] CUDA auto-detect failed, using CPU: %v", err)
			} else {
				actualProvider = "cuda"
				log.Println("[nemotron] execution provider: CUDA (auto-detected)")
			}
			cudaOpts.Destroy()
		} else {
			log.Printf("[nemotron] CUDA not available, using CPU: %v", cudaErr)
		}
	}

	encoder, err := ort.NewDynamicAdvancedSession(
		encoderPath,
		[]string{"audio_signal", "length", "cache_last_channel", "cache_last_time", "cache_last_channel_len", "lang_id"},
		[]string{"outputs", "encoded_lengths", "cache_last_channel_next", "cache_last_time_next", "cache_last_channel_len_next"},
		sessionOpts,
	)
	if err != nil {
		return nil, fmt.Errorf("nemotron: failed to create encoder session: %w", err)
	}

	decoder, err := ort.NewDynamicAdvancedSession(
		decoderPath,
		[]string{"targets", "h_in", "c_in"},
		[]string{"decoder_output", "h_out", "c_out"},
		sessionOpts,
	)
	if err != nil {
		encoder.Destroy()
		return nil, fmt.Errorf("nemotron: failed to create decoder session: %w", err)
	}

	joint, err := ort.NewDynamicAdvancedSession(
		jointPath,
		[]string{"encoder_output", "decoder_output"},
		[]string{"joint_output"},
		sessionOpts,
	)
	if err != nil {
		encoder.Destroy()
		decoder.Destroy()
		return nil, fmt.Errorf("nemotron: failed to create joint session: %w", err)
	}

	melFilters := buildMelFilterbank(sampleRate, nFFT, nMels)

	log.Printf("[nemotron] model loaded from: %s (%d vocab tokens, provider: %s)", modelDir, len(vocab), actualProvider)

	return &Engine{
		encoder:      encoder,
		decoder:      decoder,
		joint:        joint,
		vocab:        vocab,
		melFilters:   melFilters,
		modelDir:     modelDir,
		provider:     actualProvider,
		cacheChan:    make([]float32, 1*24*56*1024),
		cacheTime:    make([]float32, 1*24*1024*8),
		cacheChanLen: []int64{0},
		decoderH:     make([]float32, 2*1*640),
		decoderC:     make([]float32, 2*1*640),
		lastToken:    int64(13087),
	}, nil
}

// loadTokenizer reads vocabulary list file
func loadTokenizer(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var vocab []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		vocab = append(vocab, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return vocab, nil
}

// SetLanguage sets the target language code
func (e *Engine) SetLanguage(lang string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lang = lang
	log.Printf("[nemotron] language set to: %q", lang)
}

// TranscribeStream transcribes continuous PCM audio buffer
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

// TranscribeStreamSegments transcribes continuous PCM audio returning segments
func (e *Engine) TranscribeStreamSegments(ctx context.Context, pcmData []int16) ([]core.Segment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(pcmData) == 0 {
		return nil, nil
	}

	samples := make([]float32, len(pcmData))
	for i, s := range pcmData {
		samples[i] = float32(s) / 32768.0
	}

	melSpec := e.computeMelSpectrogram(samples)
	numFrames := len(melSpec) / nMels
	if numFrames == 0 {
		return nil, nil
	}

	if len(melSpec) > 0 {
		minM := melSpec[0]
		maxM := melSpec[0]
		var sumM float64
		for _, v := range melSpec {
			if v < minM {
				minM = v
			}
			if v > maxM {
				maxM = v
			}
			sumM += float64(v)
		}
		log.Printf("[nemotron] Mel spectrogram stats: min=%.4f, max=%.4f, avg=%.4f, frames=%d", minM, maxM, sumM/float64(len(melSpec)), numFrames)
	}

	// Causal cache variables for the duration of this transcription stream
	streamCacheChan := make([]float32, 1*24*56*1024)
	streamCacheTime := make([]float32, 1*24*1024*8)
	streamCacheChanLen := []int64{0}

	// Decoder states and history for the duration of this transcription stream
	hInData := make([]float32, 2*1*640)
	cInData := make([]float32, 2*1*640)
	lastToken := int64(13087)

	const preEncodeCache = 9
	const chunkSize = 56
	const targetFrames = 65 // preEncodeCache + chunkSize
	var finalTexts []string

	chunkIdx := 0
	// Process in sequential chunks of chunkSize (56)
	for chunkStart := 0; chunkStart < numFrames; chunkStart += chunkSize {
		if ctx.Err() != nil {
			break
		}

		// Prepare 65 frames for this chunk (shape [1, 65, 128])
		transposed := make([]float32, targetFrames*nMels)
		for i := range transposed {
			transposed[i] = -23.02585
		}
		chunkLen := 0

		// 1. Fill pre-encode cache from previous frames
		if chunkIdx > 0 && chunkStart >= preEncodeCache {
			cacheStart := chunkStart - preEncodeCache
			for f := 0; f < preEncodeCache; f++ {
				frameIdx := cacheStart + f
				chunkLen++
				for m := 0; m < nMels; m++ {
					transposed[f*nMels+m] = melSpec[m*numFrames+frameIdx]
				}
			}
		} else {
			// First chunk or near boundary: pad first 9 frames with silence value
			chunkLen += preEncodeCache
		}

		// 2. Fill main chunk (up to 56 new frames)
		mainLen := 0
		for f := 0; f < chunkSize; f++ {
			frameIdx := chunkStart + f
			destIdx := preEncodeCache + f
			if frameIdx < numFrames {
				mainLen++
				chunkLen++
				for m := 0; m < nMels; m++ {
					transposed[destIdx*nMels+m] = melSpec[m*numFrames+frameIdx]
				}
			} else {
				// Pad remaining frames with silence value
			}
		}

		// If no new frames were processed in this chunk, we can stop
		if mainLen == 0 {
			break
		}

		// Shape must be [1, targetFrames (65), nMels (128)] to match model layout
		audioSignalInput, err := ort.NewTensor(ort.NewShape(1, targetFrames, int64(nMels)), transposed)
		if err != nil {
			return nil, fmt.Errorf("nemotron encoder input: %w", err)
		}

		lengthInput, err := ort.NewTensor(ort.NewShape(1), []int64{int64(chunkLen)})
		if err != nil {
			audioSignalInput.Destroy()
			return nil, fmt.Errorf("nemotron encoder length: %w", err)
		}

		cacheChanInput, err := ort.NewTensor(ort.NewShape(1, 24, 56, 1024), streamCacheChan)
		if err != nil {
			audioSignalInput.Destroy()
			lengthInput.Destroy()
			return nil, fmt.Errorf("nemotron encoder cache channel: %w", err)
		}

		cacheTimeInput, err := ort.NewTensor(ort.NewShape(1, 24, 1024, 8), streamCacheTime)
		if err != nil {
			audioSignalInput.Destroy()
			lengthInput.Destroy()
			cacheChanInput.Destroy()
			return nil, fmt.Errorf("nemotron encoder cache time: %w", err)
		}

		cacheChanLenInput, err := ort.NewTensor(ort.NewShape(1), streamCacheChanLen)
		if err != nil {
			audioSignalInput.Destroy()
			lengthInput.Destroy()
			cacheChanInput.Destroy()
			cacheTimeInput.Destroy()
			return nil, fmt.Errorf("nemotron encoder cache len: %w", err)
		}

		langID := int64(101) // Default auto-detect
		if e.lang != "" {
			if strings.HasPrefix(strings.ToLower(e.lang), "en") {
				langID = 0 // Correct English index in prompt dictionary
			} else if id, ok := nemotronLangIDs[strings.ToLower(e.lang)]; ok {
				langID = id
			}
		}

		langIDInput, err := ort.NewTensor(ort.NewShape(1), []int64{langID})
		if err != nil {
			audioSignalInput.Destroy()
			lengthInput.Destroy()
			cacheChanInput.Destroy()
			cacheTimeInput.Destroy()
			cacheChanLenInput.Destroy()
			return nil, fmt.Errorf("nemotron encoder lang id: %w", err)
		}

		encoderOutputs := []ort.Value{nil, nil, nil, nil, nil}
		encoderInputs := []ort.Value{
			audioSignalInput,
			lengthInput,
			cacheChanInput,
			cacheTimeInput,
			cacheChanLenInput,
			langIDInput,
		}

		err = e.encoder.Run(encoderInputs, encoderOutputs)
		audioSignalInput.Destroy()
		lengthInput.Destroy()
		cacheChanInput.Destroy()
		cacheTimeInput.Destroy()
		cacheChanLenInput.Destroy()
		langIDInput.Destroy()
		if err != nil {
			return nil, fmt.Errorf("nemotron encoder run failed: %w", err)
		}

		encOutput := encoderOutputs[0]

		// Update stream cache for the next chunk
		nextCacheChanTensor := encoderOutputs[2].(*ort.Tensor[float32])
		copy(streamCacheChan, nextCacheChanTensor.GetData())
		encoderOutputs[2].Destroy()

		nextCacheTimeTensor := encoderOutputs[3].(*ort.Tensor[float32])
		copy(streamCacheTime, nextCacheTimeTensor.GetData())
		encoderOutputs[3].Destroy()

		nextCacheLenTensor := encoderOutputs[4].(*ort.Tensor[int64])
		copy(streamCacheChanLen, nextCacheLenTensor.GetData())
		encoderOutputs[4].Destroy()

		if encoderOutputs[1] != nil {
			encoderOutputs[1].Destroy()
		}

		encOutputShape := encOutput.GetShape()
		encOutputLen := encOutputShape[1]

		typedEncOutput, ok := encOutput.(*ort.Tensor[float32])
		if !ok {
			encOutput.Destroy()
			return nil, fmt.Errorf("nemotron: unexpected encoder output tensor type")
		}
		encOutData := typedEncOutput.GetData()

		// Run greedy decoding on this chunk's encoded outputs
		text, err := e.greedyDecode(ctx, encOutData, encOutputLen, hInData, cInData, &lastToken)
		encOutput.Destroy()
		if err != nil {
			return nil, err
		}

		if text != "" {
			finalTexts = append(finalTexts, text)
		}

		chunkIdx++
	}

	combinedText := strings.Join(finalTexts, " ")
	combinedText = strings.ReplaceAll(combinedText, "  ", " ")
	combinedText = strings.TrimSpace(combinedText)

	durSec := float64(len(pcmData)) / float64(sampleRate)
	log.Printf("[nemotron] transcribed: %q (%.1fs audio)", combinedText, durSec)

	return []core.Segment{
		{
			Start: 0,
			End:   time.Duration(durSec * float64(time.Second)),
			Text:  combinedText,
		},
	}, nil
}

// TranscribeWindow processes sliding window stream of dictation PCM audio
func (e *Engine) TranscribeWindow(ctx context.Context, pcmData []int16, promptText string) (core.StreamResult, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(pcmData) == 0 {
		return core.StreamResult{}, promptText, nil
	}

	samples := make([]float32, len(pcmData))
	for i, s := range pcmData {
		samples[i] = float32(s) / 32768.0
	}

	melSpec := e.computeMelSpectrogram(samples)
	numFrames := len(melSpec) / nMels
	if numFrames == 0 {
		return core.StreamResult{}, promptText, nil
	}

	const preEncodeCache = 9
	const chunkSize = 56
	const targetFrames = 65 // preEncodeCache + chunkSize
	var finalTexts []string

	chunkIdx := 0
	for chunkStart := 0; chunkStart < numFrames; chunkStart += chunkSize {
		if ctx.Err() != nil {
			break
		}

		// Prepare 65 frames for this chunk (shape [1, 65, 128])
		transposed := make([]float32, targetFrames*nMels)
		for i := range transposed {
			transposed[i] = -23.02585
		}
		chunkLen := 0

		// 1. Fill pre-encode cache from previous frames in this window
		if chunkIdx > 0 && chunkStart >= preEncodeCache {
			cacheStart := chunkStart - preEncodeCache
			for f := 0; f < preEncodeCache; f++ {
				frameIdx := cacheStart + f
				chunkLen++
				for m := 0; m < nMels; m++ {
					transposed[f*nMels+m] = melSpec[m*numFrames+frameIdx]
				}
			}
		} else {
			// First chunk or near boundary: pad first 9 frames with silence value
			chunkLen += preEncodeCache
		}

		// 2. Fill main chunk (up to 56 new frames)
		mainLen := 0
		for f := 0; f < chunkSize; f++ {
			frameIdx := chunkStart + f
			destIdx := preEncodeCache + f
			if frameIdx < numFrames {
				mainLen++
				chunkLen++
				for m := 0; m < nMels; m++ {
					transposed[destIdx*nMels+m] = melSpec[m*numFrames+frameIdx]
				}
			}
		}

		// If no new frames were processed in this chunk, we can stop
		if mainLen == 0 {
			break
		}

		audioSignalInput, err := ort.NewTensor(ort.NewShape(1, targetFrames, int64(nMels)), transposed)
		if err != nil {
			return core.StreamResult{}, promptText, fmt.Errorf("nemotron encoder input: %w", err)
		}

		lengthInput, err := ort.NewTensor(ort.NewShape(1), []int64{int64(chunkLen)})
		if err != nil {
			audioSignalInput.Destroy()
			return core.StreamResult{}, promptText, fmt.Errorf("nemotron encoder length: %w", err)
		}

		cacheChanInput, err := ort.NewTensor(ort.NewShape(1, 24, 56, 1024), e.cacheChan)
		if err != nil {
			audioSignalInput.Destroy()
			lengthInput.Destroy()
			return core.StreamResult{}, promptText, fmt.Errorf("nemotron encoder cache channel: %w", err)
		}

		cacheTimeInput, err := ort.NewTensor(ort.NewShape(1, 24, 1024, 8), e.cacheTime)
		if err != nil {
			audioSignalInput.Destroy()
			lengthInput.Destroy()
			cacheChanInput.Destroy()
			return core.StreamResult{}, promptText, fmt.Errorf("nemotron encoder cache time: %w", err)
		}

		cacheChanLenInput, err := ort.NewTensor(ort.NewShape(1), e.cacheChanLen)
		if err != nil {
			audioSignalInput.Destroy()
			lengthInput.Destroy()
			cacheChanInput.Destroy()
			cacheTimeInput.Destroy()
			return core.StreamResult{}, promptText, fmt.Errorf("nemotron encoder cache len: %w", err)
		}

		langID := int64(101) // Default auto-detect
		if e.lang != "" {
			if strings.HasPrefix(strings.ToLower(e.lang), "en") {
				langID = 0
			} else if id, ok := nemotronLangIDs[strings.ToLower(e.lang)]; ok {
				langID = id
			}
		}

		langIDInput, err := ort.NewTensor(ort.NewShape(1), []int64{langID})
		if err != nil {
			audioSignalInput.Destroy()
			lengthInput.Destroy()
			cacheChanInput.Destroy()
			cacheTimeInput.Destroy()
			cacheChanLenInput.Destroy()
			return core.StreamResult{}, promptText, fmt.Errorf("nemotron encoder lang id: %w", err)
		}

		encoderOutputs := []ort.Value{nil, nil, nil, nil, nil}
		encoderInputs := []ort.Value{
			audioSignalInput,
			lengthInput,
			cacheChanInput,
			cacheTimeInput,
			cacheChanLenInput,
			langIDInput,
		}

		err = e.encoder.Run(encoderInputs, encoderOutputs)
		audioSignalInput.Destroy()
		lengthInput.Destroy()
		cacheChanInput.Destroy()
		cacheTimeInput.Destroy()
		cacheChanLenInput.Destroy()
		langIDInput.Destroy()
		if err != nil {
			return core.StreamResult{}, promptText, fmt.Errorf("nemotron encoder run failed: %w", err)
		}

		encOutput := encoderOutputs[0]

		// Update causal caches for streaming continuation
		nextCacheChanTensor := encoderOutputs[2].(*ort.Tensor[float32])
		copy(e.cacheChan, nextCacheChanTensor.GetData())
		encoderOutputs[2].Destroy()

		nextCacheTimeTensor := encoderOutputs[3].(*ort.Tensor[float32])
		copy(e.cacheTime, nextCacheTimeTensor.GetData())
		encoderOutputs[3].Destroy()

		nextCacheLenTensor := encoderOutputs[4].(*ort.Tensor[int64])
		copy(e.cacheChanLen, nextCacheLenTensor.GetData())
		encoderOutputs[4].Destroy()

		// Destroy length output
		if encoderOutputs[1] != nil {
			encoderOutputs[1].Destroy()
		}

		encOutputShape := encOutput.GetShape()
		encOutputLen := encOutputShape[1]

		typedEncOutput, ok := encOutput.(*ort.Tensor[float32])
		if !ok {
			encOutput.Destroy()
			return core.StreamResult{}, promptText, fmt.Errorf("nemotron: unexpected encoder output tensor type")
		}
		encOutData := typedEncOutput.GetData()

		text, err := e.greedyDecode(ctx, encOutData, encOutputLen, e.decoderH, e.decoderC, &e.lastToken)
		encOutput.Destroy()
		if err != nil {
			return core.StreamResult{}, promptText, err
		}

		if text != "" {
			finalTexts = append(finalTexts, text)
		}

		chunkIdx++
	}

	combinedText := strings.Join(finalTexts, " ")
	combinedText = strings.ReplaceAll(combinedText, "  ", " ")
	combinedText = strings.TrimSpace(combinedText)

	durSec := float64(len(pcmData)) / float64(sampleRate)

	result := core.StreamResult{
		Text: combinedText,
		End:  time.Duration(durSec * float64(time.Second)),
	}

	log.Printf("[nemotron] streaming window: %q (%.1fs audio)", combinedText, durSec)

	return result, promptText, nil
}

// ResetStream clears accumulated streaming causal cache states
func (e *Engine) ResetStream() {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Reset causal caches to zeros
	for i := range e.cacheChan {
		e.cacheChan[i] = 0
	}
	for i := range e.cacheTime {
		e.cacheTime[i] = 0
	}
	e.cacheChanLen[0] = 0
	e.streamPrompt = ""

	// Reset persistent decoder states to zeros and blankToken
	for i := range e.decoderH {
		e.decoderH[i] = 0
	}
	for i := range e.decoderC {
		e.decoderC[i] = 0
	}
	e.lastToken = int64(13087)

	log.Println("[nemotron] stream context caches reset")
}

// Close closes model sessions and releases ORT allocations
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
	if e.joint != nil {
		e.joint.Destroy()
		e.joint = nil
	}
	log.Println("[nemotron] engine closed")
	return nil
}

// TranscribeFile transcribes offline audio files
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
		return nil, fmt.Errorf("nemotron: ffmpeg decode failed: %w", err)
	}

	nSamples := len(raw) / 2
	pcm := make([]int16, nSamples)
	for i := 0; i < nSamples; i++ {
		pcm[i] = int16(uint16(raw[i*2]) | uint16(raw[i*2+1])<<8)
	}

	return e.TranscribeStreamSegments(ctx, pcm)
}

// greedyDecode performs RNN-T greedy decoding loop over all frames
func (e *Engine) greedyDecode(ctx context.Context, encOutData []float32, encOutputLen int64, hInData, cInData []float32, lastToken *int64) (string, error) {
	blankToken := int64(13087)
	u := *lastToken

	hInShape := ort.NewShape(2, 1, 640)
	cInShape := ort.NewShape(2, 1, 640)

	var predictedTokens []int64

	for t := int64(0); t < encOutputLen; t++ {
		if ctx.Err() != nil {
			break
		}

		// Get encoder frame vector of shape [1, 1, 1024]
		frameOffset := t * 1024
		frameData := encOutData[frameOffset : frameOffset+1024]

		frameTensor, err := ort.NewTensor(ort.NewShape(1, 1, 1024), frameData)
		if err != nil {
			return "", fmt.Errorf("joint encoder tensor creation failed: %w", err)
		}

		symbolsCount := 0
		for symbolsCount < 5 { // Max 5 symbols per acoustic frame to avoid infinite loops
			hInTensor, err := ort.NewTensor(hInShape, hInData)
			if err != nil {
				frameTensor.Destroy()
				return "", fmt.Errorf("decoder h_in tensor creation failed: %w", err)
			}
			cInTensor, err := ort.NewTensor(cInShape, cInData)
			if err != nil {
				frameTensor.Destroy()
				hInTensor.Destroy()
				return "", fmt.Errorf("decoder c_in tensor creation failed: %w", err)
			}

			targetsTensor, err := ort.NewTensor(ort.NewShape(1, 1), []int64{u})
			if err != nil {
				frameTensor.Destroy()
				hInTensor.Destroy()
				cInTensor.Destroy()
				return "", fmt.Errorf("decoder targets tensor creation failed: %w", err)
			}

			decoderOutputs := []ort.Value{nil, nil, nil}
			decoderInputs := []ort.Value{targetsTensor, hInTensor, cInTensor}

			err = e.decoder.Run(decoderInputs, decoderOutputs)
			targetsTensor.Destroy()
			hInTensor.Destroy()
			cInTensor.Destroy()
			if err != nil {
				frameTensor.Destroy()
				return "", fmt.Errorf("decoder execution failed: %w", err)
			}

			decOutputTensor := decoderOutputs[0].(*ort.Tensor[float32])
			decOutData := decOutputTensor.GetData()

			jointDecTensor, err := ort.NewTensor(ort.NewShape(1, 1, 640), decOutData)
			if err != nil {
				frameTensor.Destroy()
				decoderOutputs[0].Destroy()
				decoderOutputs[1].Destroy()
				decoderOutputs[2].Destroy()
				return "", fmt.Errorf("joint decoder tensor creation failed: %w", err)
			}

			jointOutputs := []ort.Value{nil}
			jointInputs := []ort.Value{frameTensor, jointDecTensor}

			err = e.joint.Run(jointInputs, jointOutputs)
			jointDecTensor.Destroy()
			if err != nil {
				frameTensor.Destroy()
				decoderOutputs[0].Destroy()
				decoderOutputs[1].Destroy()
				decoderOutputs[2].Destroy()
				return "", fmt.Errorf("joint execution failed: %w", err)
			}

			logitsTensor := jointOutputs[0].(*ort.Tensor[float32])
			logits := logitsTensor.GetData()

			// Find argmax over vocabulary size (13088)
			maxIdx := 0
			maxVal := logits[0]
			for i := 1; i < len(logits); i++ {
				if logits[i] > maxVal {
					maxVal = logits[i]
					maxIdx = i
				}
			}
			y := int64(maxIdx)

			if t < 5 && symbolsCount == 0 {
				var tokenStr string
				if y >= 0 && y < int64(len(e.vocab)) {
					tokenStr = e.vocab[y]
				} else {
					tokenStr = "OUT_OF_BOUNDS"
				}
				log.Printf("[nemotron] Frame %d: y=%d (%q), logit=%.4f (blank=%d, blank_logit=%.4f)", t, y, tokenStr, maxVal, blankToken, logits[blankToken])
			}

			logitsTensor.Destroy()
			decoderOutputs[0].Destroy()

			if y == blankToken {
				// Blank token predicted — advance to next frame, discard decoder state updates
				decoderOutputs[1].Destroy()
				decoderOutputs[2].Destroy()
				break
			}

			// Non-blank token predicted — update targets, update decoder hidden states, and loop
			u = y
			predictedTokens = append(predictedTokens, y)

			nextH := decoderOutputs[1].(*ort.Tensor[float32])
			copy(hInData, nextH.GetData())
			decoderOutputs[1].Destroy()

			nextC := decoderOutputs[2].(*ort.Tensor[float32])
			copy(cInData, nextC.GetData())
			decoderOutputs[2].Destroy()

			symbolsCount++
		}

		frameTensor.Destroy()
	}

	*lastToken = u
	return e.decodeTokens(predictedTokens), nil
}

// decodeTokens converts predicted token indices to standard text
func (e *Engine) decodeTokens(tokens []int64) string {
	var sb strings.Builder
	for _, id := range tokens {
		if id < 0 || id >= int64(len(e.vocab)) {
			continue
		}
		token := e.vocab[id]
		if strings.HasPrefix(token, "<") && strings.HasSuffix(token, ">") {
			continue
		}
		// Replace U+2581 or custom space block indicator '▁' with space
		token = strings.ReplaceAll(token, "▁", " ")
		sb.WriteString(token)
	}
	text := sb.String()
	text = strings.ReplaceAll(text, "  ", " ")
	return strings.TrimSpace(text)
}

// computeMelSpectrogram transforms raw PCM float32 samples to log-mel bins
func (e *Engine) computeMelSpectrogram(samples []float32) []float32 {
	if len(samples) < nFFT {
		padded := make([]float32, nFFT)
		copy(padded, samples)
		samples = padded
	}

	// Apply preemphasis: s[t] = x[t] - 0.97 * x[t-1]
	preemphasized := make([]float32, len(samples))
	if len(samples) > 0 {
		preemphasized[0] = samples[0]
		for i := 1; i < len(samples); i++ {
			preemphasized[i] = samples[i] - 0.97*samples[i-1]
		}
	}
	samples = preemphasized

	// Center padding: pad by nFFT / 2 (256) samples on both ends.
	// This matches center=True in librosa/torchaudio.
	const padSize = nFFT / 2
	paddedSamples := make([]float32, len(samples)+2*padSize)
	copy(paddedSamples[padSize:], samples)
	samples = paddedSamples

	numFrames := (len(samples) - windowLength) / hopLength + 1
	if numFrames < 1 {
		numFrames = 1
	}

	fftBins := nFFT/2 + 1
	magnitudes := make([]float32, numFrames*fftBins)

	window := buildHannWindow(windowLength)

	fftReal := make([]float64, fftPaddedSize)
	fftImag := make([]float64, fftPaddedSize)
	frameMag := make([]float32, fftBins)

	for frame := 0; frame < numFrames; frame++ {
		offset := frame * hopLength
		var frameData []float32
		if offset < len(samples) {
			end := offset + windowLength
			if end > len(samples) {
				end = len(samples)
			}
			frameData = samples[offset:end]
		}

		fftMagnitudeSquared(frameData, window, fftReal, fftImag, frameMag)
		copy(magnitudes[frame*fftBins:], frameMag)
	}

	mel := make([]float32, nMels*numFrames)
	for m := 0; m < nMels; m++ {
		for frame := 0; frame < numFrames; frame++ {
			var sum float32
			for k := 0; k < fftBins; k++ {
				sum += e.melFilters[m*fftBins+k] * magnitudes[frame*fftBins+k]
			}
			// NeMo/Nemotron expects natural log (ln) with 2^-24 (5.9604645e-8) zero guard
			mel[m*numFrames+frame] = float32(math.Log(float64(sum) + 5.9604645e-8))
		}
	}

	return mel
}
