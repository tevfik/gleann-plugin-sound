//go:build whisper
// +build whisper

// Package whisper provides the CGO-backed Whisper transcription engine.
//
// This file is compiled ONLY when the "whisper" build tag is set.  When
// building without the tag, the stub in stub.go is used instead.
//
// The implementation wraps the whisper.cpp Go bindings and ensures all memory
// allocated through CGO is properly freed.
//
// Build requirements:
//   - whisper.cpp must be built as a shared or static library
//   - Set WHISPER_DIR to the whisper.cpp source/install directory
//   - The Makefile passes CGO_CFLAGS and CGO_LDFLAGS via environment variables;
//     do NOT duplicate those directives here.
package whisper

/*
#include "whisper.h"
#include "ggml.h"
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/tevfik/gleann-sound/internal/audio"
	"github.com/tevfik/gleann-sound/internal/core"
)

// ---------------------------------------------------------------------------
// Engine — CGO whisper.cpp implementation
// ---------------------------------------------------------------------------

// Engine wraps a whisper.cpp context and exposes it through core.Transcriber.
//
// It is NOT safe for concurrent use; the caller must serialise calls or create
// multiple Engine instances.
type Engine struct {
	mu    sync.Mutex
	ctx   *C.struct_whisper_context
	model string
	lang  string // language code (e.g. "tr", "en"); empty = auto-detect

	// Reusable float buffer to avoid repeated allocations.
	// Sized to hold up to 30s of audio at 16 kHz.
	floatBuf []float32
}

// Compile-time interface check.
var _ core.Transcriber = (*Engine)(nil)

func init() {
	core.RegisterBackend("whisper", func(model string) (core.Transcriber, error) {
		return NewEngine(model)
	})
}

// NewEngine loads the GGML model file and returns a ready-to-use Engine.
//
//	model: path to a ggml model file (e.g. "models/ggml-base.en.bin")
func NewEngine(model string) (*Engine, error) {
	cpath := C.CString(model)
	defer C.free(unsafe.Pointer(cpath))

	cparams := C.whisper_context_default_params()
	wctx := C.whisper_init_from_file_with_params(cpath, cparams)
	if wctx == nil {
		return nil, fmt.Errorf("whisper: failed to load model %q", model)
	}

	log.Printf("[whisper] model loaded: %s", model)
	return &Engine{ctx: wctx, model: model}, nil
}

// SetLanguage sets the language for transcription.
// Use ISO 639-1 codes (e.g. "tr", "en", "de", "fr").
// Empty string means auto-detect (default for multilingual models).
func (e *Engine) SetLanguage(lang string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lang = lang
	log.Printf("[whisper] language set to: %q", lang)
}

// TranscribeStream processes raw 16 kHz 16-bit mono PCM samples and returns
// the concatenated transcription text.
func (e *Engine) TranscribeStream(ctx context.Context, pcmData []int16) (string, error) {
	segments, err := e.TranscribeStreamSegments(ctx, pcmData)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, seg := range segments {
		sb.WriteString(seg.Text)
	}
	result := strings.TrimSpace(sb.String())

	// Final safety net: detect repetitive decoder-loop output.
	if isRepetitive(result) {
		log.Printf("[whisper] repetitive output detected and discarded: %q", truncate(result, 80))
		return "", nil
	}

	return result, nil
}

// truncate shortens a string to maxLen runes, appending "…" if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}

// TranscribeStreamSegments processes raw PCM and returns timestamped segments.
func (e *Engine) TranscribeStreamSegments(ctx context.Context, pcmData []int16) ([]core.Segment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(pcmData) == 0 {
		return nil, nil
	}

	// Convert int16 → float32 in [-1, 1] for the whisper C API.
	// Reuse the float buffer to avoid repeated allocations (memory leak fix).
	n := len(pcmData)
	if cap(e.floatBuf) < n {
		e.floatBuf = make([]float32, n)
	} else {
		e.floatBuf = e.floatBuf[:n]
	}
	for i, s := range pcmData {
		e.floatBuf[i] = float32(s) / 32768.0
	}

	// Set up default full params — "greedy" strategy is fine for short clips.
	params := C.whisper_full_default_params(C.WHISPER_SAMPLING_GREEDY)
	params.print_progress = C.bool(false)
	params.print_special = C.bool(false)
	params.print_realtime = C.bool(false)
	params.print_timestamps = C.bool(false)
	params.single_segment = C.bool(false)

	// Prevent previous transcription context from bleeding into the next one.
	params.no_context = C.bool(true)

	// Suppress blank output and non-speech tokens.
	params.suppress_blank = C.bool(true)
	params.suppress_nst = C.bool(true)

	// ── Anti-repetition / decoder loop prevention ───────────────
	// Limit max tokens per segment. Whisper's decoder can get stuck
	// repeating the same token indefinitely (e.g. "bir amca yapar" × 28).
	// For dictation clips (1-30s), 64 tokens per segment is generous.
	params.max_tokens = 64

	// Entropy threshold: if a segment's average token entropy exceeds this,
	// it's likely a decoder loop. Default is 2.4; we use a tighter 2.2.
	params.entropy_thold = 2.2

	// Log probability threshold: if average log prob < this, the segment
	// quality is too low. Default is -1.0.
	params.logprob_thold = -1.0

	// ── Speed optimisations ──────────────────────────────────────
	// Disable temperature fallback retries — run once at temp 0 and done.
	params.temperature = 0.0
	params.temperature_inc = 0.0

	// Use available CPU cores for faster inference.
	nCPU := runtime.NumCPU()
	if nCPU > 16 {
		nCPU = 16
	}
	if nCPU < 1 {
		nCPU = 1
	}
	params.n_threads = C.int(nCPU)

	// Set language if specified; otherwise auto-detect.
	if e.lang != "" {
		cLang := C.CString(e.lang)
		defer C.free(unsafe.Pointer(cLang))
		params.language = cLang
		log.Printf("[whisper] using language: %s", e.lang)
	} else {
		// nil language = auto-detect for multilingual models
		params.language = nil
	}

	log.Printf("[whisper] running inference on %d float samples (%.2fs)...",
		n, float64(n)/float64(audio.WhisperSampleRate))

	// Run inference.
	ret := C.whisper_full(e.ctx, params, (*C.float)(unsafe.Pointer(&e.floatBuf[0])), C.int(n))
	if ret != 0 {
		return nil, fmt.Errorf("whisper: inference failed (code %d)", int(ret))
	}

	// Collect segments, filtering out likely hallucinations.
	nSeg := int(C.whisper_full_n_segments(e.ctx))
	log.Printf("[whisper] inference complete: %d segments", nSeg)
	segments := make([]core.Segment, 0, nSeg)
	for i := 0; i < nSeg; i++ {
		t0 := int64(C.whisper_full_get_segment_t0(e.ctx, C.int(i))) * 10 // centiseconds → ms
		t1 := int64(C.whisper_full_get_segment_t1(e.ctx, C.int(i))) * 10
		text := C.GoString(C.whisper_full_get_segment_text(e.ctx, C.int(i)))
		text = strings.TrimSpace(text)

		// Skip empty or very short segments (single char / punctuation only).
		if len([]rune(text)) < 2 {
			continue
		}

		// Skip segments where whisper itself is not confident there is speech.
		noSpeechProb := float64(C.whisper_full_get_segment_no_speech_prob(e.ctx, C.int(i)))
		if noSpeechProb > 0.6 {
			log.Printf("[whisper] segment %d skipped: no_speech_prob=%.2f text=%q", i, noSpeechProb, text)
			continue
		}

		// Skip known whisper hallucination patterns (common on silence/noise).
		if isHallucination(text) {
			log.Printf("[whisper] segment %d skipped: hallucination pattern text=%q", i, text)
			continue
		}

		// Skip repetitive decoder-loop output.
		if isRepetitive(text) {
			log.Printf("[whisper] segment %d skipped: repetitive text=%q", i, truncate(text, 60))
			continue
		}

		segments = append(segments, core.Segment{
			Start: time.Duration(t0) * time.Millisecond,
			End:   time.Duration(t1) * time.Millisecond,
			Text:  text,
		})
	}

	return segments, nil
}

// isHallucination returns true if the text matches known whisper hallucination
// patterns that commonly appear when processing silence or noise.
func isHallucination(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, pattern := range hallucinationPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// hallucinationPatterns lists common whisper hallucination strings that appear
// when the model processes silence, noise, or very short audio. These are
// well-documented in the whisper community across multiple languages.
var hallucinationPatterns = []string{
	// Turkish
	"altyazı",
	"izlediğiniz için teşekkür",
	"teşekkür ederim",
	"abone olmayı unutmayın",
	"abone olun",
	"beğenmeyi unutmayın",
	"bir sonraki videoda",
	"görüşmek üzere",
	"videoyu beğenmeyi",
	// English
	"thank you for watching",
	"thanks for watching",
	"please subscribe",
	"like and subscribe",
	"thank you for listening",
	"see you in the next",
	"don't forget to subscribe",
	// German
	"danke fürs zuschauen",
	"danke für das anschauen",
	"bis zum nächsten",
	// Common patterns (language-agnostic)
	"www.",
	"http",
	"[music]",
	"[müzik]",
	"[applause]",
	"♪",
}

// isRepetitive detects decoder-loop output where a short phrase is repeated
// many times (e.g. "bir amca yaparbir amca yaparbir amca yapar...").
// Returns true if the text contains a repeating pattern ≥3 times.
func isRepetitive(text string) bool {
	if len(text) < 12 {
		return false
	}
	runes := []rune(text)
	n := len(runes)
	// Check pattern lengths from 2 to n/3 runes.
	maxPatLen := n / 3
	if maxPatLen > 40 {
		maxPatLen = 40
	}
	for patLen := 2; patLen <= maxPatLen; patLen++ {
		pattern := string(runes[:patLen])
		count := 0
		for i := 0; i+patLen <= n; i += patLen {
			if string(runes[i:i+patLen]) == pattern {
				count++
			} else {
				break
			}
		}
		if count >= 3 {
			return true
		}
	}
	return false
}

// TranscribeFile transcribes an audio/video file by first converting it to
// 16 kHz mono WAV via ffmpeg, then running Whisper on the resulting PCM.
func (e *Engine) TranscribeFile(ctx context.Context, filepath string) ([]core.Segment, error) {
	// Use ffmpeg to decode any media format into raw 16-bit PCM.
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
		return nil, fmt.Errorf("whisper: ffmpeg decode failed: %w", err)
	}

	// Convert raw bytes to int16 samples.
	nSamples := len(raw) / 2
	pcm := make([]int16, nSamples)
	for i := 0; i < nSamples; i++ {
		pcm[i] = int16(uint16(raw[i*2]) | uint16(raw[i*2+1])<<8)
	}

	return e.TranscribeStreamSegments(ctx, pcm)
}

// Close releases the whisper.cpp context and frees all associated memory.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.ctx != nil {
		C.whisper_free(e.ctx)
		e.ctx = nil
		log.Println("[whisper] engine closed")
	}
	return nil
}
