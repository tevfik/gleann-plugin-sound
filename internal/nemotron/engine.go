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
	"path/filepath"
	"strings"
	"sync"
	"time"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/ortrt"
)

var nemotronLangIDs = map[string]int64{
	"en-us": 0, "en": 0,
	"en-gb": 1,
	"es-es": 2, "es": 2,
	"es-us": 3,
	"zh-cn": 4, "zh": 4,
	"zh-tw": 5,
	"hi-in": 6, "hi": 6,
	"ar-ar": 7, "ar": 7,
	"fr-fr": 8, "fr": 8,
	"de-de": 9, "de": 9,
	"ja-jp": 10, "ja": 10,
	"ru-ru": 11, "ru": 11,
	"pt-br": 12, "pt": 12,
	"pt-pt": 13,
	"ko-kr": 14, "ko": 14,
	"it-it": 15, "it": 15,
	"nl-nl": 16, "nl": 16,
	"pl-pl": 17, "pl": 17,
	"tr-tr": 18, "tr": 18,
	"uk-ua": 19, "uk": 19,
	"ro-ro": 20, "ro": 20,
	"el-gr": 21, "el": 21,
	"cs-cz": 22, "cs": 22,
	"hu-hu": 23, "hu": 23,
	"sv-se": 24, "sv": 24,
	"da-dk": 25, "da": 25,
	"fi-fi": 26, "fi": 26,
	"no-no": 27, "no": 27,
	"sk-sk": 28, "sk": 28,
	"hr-hr": 29, "hr": 29,
	"bg-bg": 30, "bg": 30,
	"lt-lt": 31, "lt": 31,
	"th-th": 32, "th": 32,
	"vi-vn": 33, "vi": 33,
	"id-id": 34, "id": 34,
	"ms-my": 35, "ms": 35,
	"bn-in": 36, "bn": 36,
	"ur-pk": 37, "ur": 37,
	"fa-ir": 38, "fa": 38,
	"ta-in": 39, "ta": 39,
	"te-in": 40, "te": 40,
	"mr-in": 41, "mr": 41,
	"gu-in": 42, "gu": 42,
	"kn-in": 43, "kn": 43,
	"ml-in": 44, "ml": 44,
	"si-lk": 45, "si": 45,
	"ne-np": 46, "ne": 46,
	"km-kh": 47, "km": 47,
	"sw-ke": 48, "sw": 48,
	"am-et": 49, "am": 49,
	"ha-ng": 50, "ha": 50,
	"zu-za": 51, "zu": 51,
	"yo-ng": 52, "yo": 52,
	"ig-ng": 53, "ig": 53,
	"af-za": 54, "af": 54,
	"rw-rw": 55, "rw": 55,
	"so-so": 56, "so": 56,
	"ny-mw": 57, "ny": 57,
	"ln-cd": 58, "ln": 58,
	"or-ke": 59, "or": 59,
	"et-ee": 60,
	"lv-lv": 61,
	"sl-si": 62, "sl": 62,
	"he-il": 64, "he": 64,
	"ku-tr": 65, "ku": 65,
	"az-az": 66, "az": 66,
	"ka-ge": 67, "ka": 67,
	"hy-am": 68, "hy": 68,
	"uz-uz": 69, "uz": 69,
	"tg-tj": 70, "tg": 70,
	"ky-kg": 71, "ky": 71,
	"qu-pe": 80, "qu": 80,
	"ay-bo": 81, "ay": 81,
	"gn-py": 82, "gn": 82,
	"nah-mx": 83, "nah": 83,
	"mi-nz": 96, "mi": 96,
	"haw-us": 97, "haw": 97,
	"sm-ws": 98, "sm": 98,
	"to-to": 99, "to": 99,
	"fr-ca": 100,
	"mt-mt": 102, "mt": 102,
	"nb-no": 103,
	"nn-no": 104,
	"auto":  101,
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
}

// Compile-time interface validation
var _ core.Transcriber = (*Engine)(nil)

func init() {
	core.RegisterBackend("nemotron", func(model string) (core.Transcriber, error) {
		return NewEngine(model)
	})
}

// NewEngine creates a new Nemotron ASR engine session
func NewEngine(modelDir string) (*Engine, error) {
	if err := ortrt.Ensure(); err != nil {
		return nil, fmt.Errorf("nemotron: %w", err)
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

	// Optimize thread count to avoid thread contention on high-core CPUs.
	// 4 threads is generally optimal for speech transformer models.
	_ = sessionOpts.SetIntraOpNumThreads(4)
	_ = sessionOpts.SetInterOpNumThreads(1)
	// Idle worker threads would otherwise spin-wait between chunks and burn CPU
	// for the whole life of a live session.
	_ = sessionOpts.AddSessionConfigEntry("session.intra_op.allow_spinning", "0")

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
		encoder:    encoder,
		decoder:    decoder,
		joint:      joint,
		vocab:      vocab,
		melFilters: melFilters,
		modelDir:   modelDir,
		provider:   actualProvider,
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

// TranscribeStream transcribes a complete PCM buffer.
func (e *Engine) TranscribeStream(ctx context.Context, pcmData []int16) (string, error) {
	segments, err := e.TranscribeStreamSegments(ctx, pcmData)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		parts = append(parts, seg.Text)
	}
	return strings.Join(parts, " "), nil
}

// segmentGap is the pause that splits a recording into separate segments.
const segmentGap = 1500 * time.Millisecond

// TranscribeStreamSegments transcribes a complete PCM buffer through the same
// cache-aware streaming path used for live audio, returning one segment per
// phrase (split at pauses) with timestamps.
func (e *Engine) TranscribeStreamSegments(ctx context.Context, pcmData []int16) ([]core.Segment, error) {
	if len(pcmData) == 0 {
		return nil, nil
	}
	s := e.newStream()
	// With the whole recording at hand, normalise by its true peak.
	var peak float32
	for _, v := range pcmData {
		if a := float32(math.Abs(float64(v))) / 32768.0; a > peak {
			peak = a
		}
	}
	if peak > 1e-6 {
		s.fixedGain = 1 / (peak + 1e-5)
	} else {
		s.fixedGain = 1
	}
	if _, err := s.Accept(ctx, pcmData); err != nil {
		return nil, err
	}
	if _, err := s.Flush(ctx); err != nil {
		return nil, err
	}
	segs := s.segments(segmentGap)
	dur := float64(len(pcmData)) / float64(sampleRate)
	log.Printf("[nemotron] transcribed %.1fs audio into %d segment(s)", dur, len(segs))
	return segs, nil
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

// DecodeFile decodes any audio/video file to 16 kHz mono PCM through ffmpeg.
func DecodeFile(ctx context.Context, path string) ([]int16, error) {
	pcm, err := core.DecodeFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("nemotron: %w", err)
	}
	return pcm, nil
}

// TranscribeFile transcribes an audio or video file.
func (e *Engine) TranscribeFile(ctx context.Context, filepath string) ([]core.Segment, error) {
	pcm, err := DecodeFile(ctx, filepath)
	if err != nil {
		return nil, err
	}
	return e.TranscribeStreamSegments(ctx, pcm)
}
