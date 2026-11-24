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
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/tevfik/gleann-plugin-sound/internal/audio"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// ---------------------------------------------------------------------------
// Engine — CGO whisper.cpp implementation
// ---------------------------------------------------------------------------

// Engine wraps a whisper.cpp context and exposes it through core.Transcriber
// and core.StreamingTranscriber.
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

	// streamPrompt holds the previous window's transcription text for
	// context carryover in streaming mode (used as initial_prompt).
	streamPrompt string
}

// Compile-time interface checks.
var _ core.Transcriber = (*Engine)(nil)
var _ core.StreamingTranscriber = (*Engine)(nil)

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
	if core.IsRepetitive(result) {
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
		if core.IsHallucination(text) {
			log.Printf("[whisper] segment %d skipped: hallucination pattern text=%q", i, text)
			continue
		}

		// Skip repetitive decoder-loop output.
		if core.IsRepetitive(text) {
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

// TranscribeWindow processes a single sliding window of PCM data with context
// from the previous transcription.  The promptText carries the prior window's
// output to condition the decoder (set as initial_prompt in whisper.cpp).
//
// Returns the transcription result and the prompt text to carry forward.
func (e *Engine) TranscribeWindow(ctx context.Context, pcmData []int16, promptText string) (core.StreamResult, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(pcmData) == 0 {
		return core.StreamResult{}, promptText, nil
	}

	// Convert int16 → float32 in [-1, 1].
	n := len(pcmData)
	if cap(e.floatBuf) < n {
		e.floatBuf = make([]float32, n)
	} else {
		e.floatBuf = e.floatBuf[:n]
	}
	for i, s := range pcmData {
		e.floatBuf[i] = float32(s) / 32768.0
	}

	params := C.whisper_full_default_params(C.WHISPER_SAMPLING_GREEDY)
	params.print_progress = C.bool(false)
	params.print_special = C.bool(false)
	params.print_realtime = C.bool(false)
	params.print_timestamps = C.bool(false)
	params.single_segment = C.bool(false)
	params.suppress_blank = C.bool(true)
	params.suppress_nst = C.bool(true)
	params.max_tokens = 64
	params.entropy_thold = 2.2
	params.logprob_thold = -1.0
	params.temperature = 0.0
	params.temperature_inc = 0.0

	nCPU := runtime.NumCPU()
	if nCPU > 16 {
		nCPU = 16
	}
	if nCPU < 1 {
		nCPU = 1
	}
	params.n_threads = C.int(nCPU)

	// Language.
	if e.lang != "" {
		cLang := C.CString(e.lang)
		defer C.free(unsafe.Pointer(cLang))
		params.language = cLang
	} else {
		params.language = nil
	}

	// Context carryover: use previous window's text as initial prompt.
	// This conditions the decoder to continue naturally from the prior output.
	if promptText != "" {
		params.no_context = C.bool(false)
		cPrompt := C.CString(promptText)
		defer C.free(unsafe.Pointer(cPrompt))
		params.initial_prompt = cPrompt
		log.Printf("[whisper] streaming window with prompt: %q", truncate(promptText, 60))
	} else {
		params.no_context = C.bool(true)
	}

	log.Printf("[whisper] streaming window: %d samples (%.2fs)",
		n, float64(n)/float64(audio.WhisperSampleRate))

	ret := C.whisper_full(e.ctx, params, (*C.float)(unsafe.Pointer(&e.floatBuf[0])), C.int(n))
	if ret != 0 {
		return core.StreamResult{}, promptText, fmt.Errorf("whisper: inference failed (code %d)", int(ret))
	}

	// Collect segments with per-segment timestamps for deduplication.
	nSeg := int(C.whisper_full_n_segments(e.ctx))
	var sb strings.Builder
	var segments []core.Segment
	for i := 0; i < nSeg; i++ {
		t0 := int64(C.whisper_full_get_segment_t0(e.ctx, C.int(i))) * 10 // centiseconds → ms
		t1 := int64(C.whisper_full_get_segment_t1(e.ctx, C.int(i))) * 10
		text := C.GoString(C.whisper_full_get_segment_text(e.ctx, C.int(i)))
		text = strings.TrimSpace(text)

		if len([]rune(text)) < 2 {
			continue
		}
		noSpeechProb := float64(C.whisper_full_get_segment_no_speech_prob(e.ctx, C.int(i)))
		if noSpeechProb > 0.6 {
			continue
		}
		if core.IsHallucination(text) || core.IsRepetitive(text) {
			continue
		}
		segments = append(segments, core.Segment{
			Start: time.Duration(t0) * time.Millisecond,
			End:   time.Duration(t1) * time.Millisecond,
			Text:  text,
		})
		if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(text)
	}

	resultText := strings.TrimSpace(sb.String())
	durSec := float64(n) / float64(audio.WhisperSampleRate)

	result := core.StreamResult{
		Text:     resultText,
		Segments: segments,
		End:      time.Duration(durSec * float64(time.Second)),
	}

	// Carry forward: use the last portion of this transcription as prompt
	// for the next window.  Keep short (~50 chars) to reduce decoder bias
	// and prompt poisoning risk.
	nextPrompt := resultText
	if len(nextPrompt) > 50 {
		nextPrompt = nextPrompt[len(nextPrompt)-50:]
		// Trim to word boundary.
		if idx := strings.Index(nextPrompt, " "); idx >= 0 {
			nextPrompt = nextPrompt[idx+1:]
		}
	}

	return result, nextPrompt, nil
}

// ResetStream clears accumulated streaming context.
func (e *Engine) ResetStream() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.streamPrompt = ""
	log.Println("[whisper] stream context reset")
}

// TranscribeFile transcribes an audio/video file by first converting it to
// 16 kHz mono WAV via ffmpeg, then running Whisper on the resulting PCM.
func (e *Engine) TranscribeFile(ctx context.Context, filepath string) ([]core.Segment, error) {
	// Shared decoder: honours the duration cap carried by ctx (core.WithMaxDecode).
	pcm, err := core.DecodeFile(ctx, filepath)
	if err != nil {
		return nil, fmt.Errorf("whisper: %w", err)
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
