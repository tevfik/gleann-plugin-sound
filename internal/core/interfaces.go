// Package core defines the shared domain interfaces and types for gleann-plugin-sound.
//
// Every subsystem (audio capture, transcription, keyboard injection) implements
// one of these contracts so they can be composed, mocked, and swapped independently.
package core

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Transcription
// ---------------------------------------------------------------------------

// Segment represents a single timestamped piece of transcribed text.
type Segment struct {
	// Start is the offset from the beginning of the audio buffer.
	Start time.Duration `json:"start"`
	// End is the offset where this segment's speech ends.
	End time.Duration `json:"end"`
	// Text is the transcribed content for this segment.
	Text string `json:"text"`
	// Speaker names who spoke when speaker identification is on ("?" = unknown).
	Speaker string `json:"speaker,omitempty"`
}

// Transcriber handles the conversion of audio data to text using whisper.cpp.
//
// Implementations MUST be safe to call from a single goroutine but are NOT
// required to be safe for concurrent use; the caller is responsible for
// serialising access if needed.
type Transcriber interface {
	// TranscribeStream processes a raw 16kHz, 16-bit, Mono PCM buffer and
	// returns the concatenated transcription text.  The pcmData slice must
	// contain samples at 16 000 Hz sample-rate, single channel, signed 16-bit.
	TranscribeStream(ctx context.Context, pcmData []int16) (string, error)

	// TranscribeStreamSegments is like TranscribeStream but returns individual
	// timestamped segments for JSONL output.
	TranscribeStreamSegments(ctx context.Context, pcmData []int16) ([]Segment, error)

	// TranscribeFile processes a media file.  The implementation may shell out
	// to ffmpeg or use an in-process decoder to resample to 16kHz Mono PCM
	// before running inference.
	TranscribeFile(ctx context.Context, filepath string) ([]Segment, error)

	// SetLanguage configures the language for transcription.
	// Use ISO 639-1 codes (e.g. "tr", "en"). Empty = auto-detect.
	SetLanguage(lang string)

	// Close releases all resources held by the engine (model weights, CGO allocations).
	Close() error
}

// ---------------------------------------------------------------------------
// Backend Registry
// ---------------------------------------------------------------------------

// BackendFactory creates a Transcriber for the given model path.
type BackendFactory func(model string) (Transcriber, error)

var (
	backendsMu sync.RWMutex
	backends   = map[string]BackendFactory{}
)

// RegisterBackend registers a named transcription backend.
// Called from init() in backend packages (e.g. whisper, onnx).
func RegisterBackend(name string, factory BackendFactory) {
	backendsMu.Lock()
	defer backendsMu.Unlock()
	backends[name] = factory
}

// NewTranscriber creates a Transcriber using the named backend.
// Returns an error if the backend is not available (not compiled in).
func NewTranscriber(backend, model string) (Transcriber, error) {
	backendsMu.RLock()
	defer backendsMu.RUnlock()
	factory, ok := backends[backend]
	if !ok {
		available := make([]string, 0, len(backends))
		for k := range backends {
			available = append(available, k)
		}
		return nil, fmt.Errorf("backend %q not available (compiled backends: %v)", backend, available)
	}
	return factory(model)
}

// ---------------------------------------------------------------------------
// Audio Capture
// ---------------------------------------------------------------------------

// AudioCapturer hooks into the OS audio subsystem (microphone or loopback).
//
// The lifecycle is: Start → onData callbacks → Stop.
// Calling Start on an already-started capturer is an error.
type AudioCapturer interface {
	// Start begins capturing audio from the default input device.
	// onData is invoked on an internal goroutine with chunks of 16kHz, 16-bit
	// Mono PCM samples.  It is the caller's responsibility to handle the data
	// without blocking.
	Start(ctx context.Context, onData func(pcmData []int16)) error

	// Stop halts the audio capture stream and releases OS resources
	// (PipeWire / PulseAudio / WASAPI handles).
	Stop() error
}

// ---------------------------------------------------------------------------
// Keyboard Injection
// ---------------------------------------------------------------------------

// KeyboardInjector simulates physical keyboard inputs for the dictation mode.
type KeyboardInjector interface {
	// TypeText types the given UTF-8 string into the currently focused window
	// by injecting synthetic key events at the OS level.
	TypeText(text string) error
}

// ---------------------------------------------------------------------------
// Plugin / streaming events
// ---------------------------------------------------------------------------

// TranscriptionEvent is the payload streamed from the daemon to the main gleann
// application (for example over the /live WebSocket).
type TranscriptionEvent struct {
	// Segments contains one or more timestamped transcriptions.
	Segments []Segment `json:"segments"`
	// Final indicates that no more segments will follow for this utterance.
	Final bool `json:"final"`
}

// TranscriptionHandler is a callback-style sink for streaming transcription
// events consumed by the plugin server.
type TranscriptionHandler func(event TranscriptionEvent)

// ---------------------------------------------------------------------------
// Streaming Transcription
// ---------------------------------------------------------------------------

// StreamResult represents a single streaming transcription result from
// a sliding-window pipeline.
type StreamResult struct {
	// Text is the transcribed text for this window.
	Text string `json:"text"`
	// Segments holds per-segment timestamps from whisper.  When available,
	// the pipeline uses these for timestamp-based deduplication (more
	// reliable than text matching).  nil when the backend does not produce
	// per-segment timestamps (e.g. ONNX), in which case the pipeline
	// falls back to text-based dedup.
	Segments []Segment `json:"segments,omitempty"`
	// Start is the absolute start time from stream begin.
	Start time.Duration `json:"start"`
	// End is the absolute end time from stream begin.
	End time.Duration `json:"end"`
	// IsFinal is true when confirmed (next window has been processed).
	IsFinal bool `json:"is_final"`
	// WindowSeq is a monotonic window sequence number.
	WindowSeq int `json:"window_seq"`
}

// StreamingTranscriber extends Transcriber with sliding-window streaming
// capabilities.  Implementations maintain internal state (context prompt,
// timing) across consecutive TranscribeWindow calls.
type StreamingTranscriber interface {
	Transcriber

	// TranscribeWindow processes a single window of PCM data with context
	// from the previous transcription.  promptText carries the last output
	// from the prior window for decoder conditioning (initial_prompt for
	// whisper.cpp, token prepend for ONNX).
	//
	// Returns the transcription result and the prompt text to carry forward
	// to the next window.
	TranscribeWindow(ctx context.Context, pcm []int16, promptText string) (StreamResult, string, error)

	// ResetStream clears all accumulated context.  Call between sessions
	// (e.g. when the user stops and restarts listening).
	ResetStream()
}

// ---------------------------------------------------------------------------
// Voice Activity Detection
// ---------------------------------------------------------------------------

// VADProvider is a pluggable voice activity detector.  Implementations may
// be energy-based (simple RMS) or neural (e.g. Silero VAD).
type VADProvider interface {
	// IsSpeech returns true if the given PCM chunk likely contains speech.
	IsSpeech(pcm []int16) bool
	// Reset clears internal state so the VAD re-calibrates.
	Reset()
}
