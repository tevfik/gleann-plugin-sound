// Package core defines the shared domain interfaces and types for gleann-sound.
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
// Plugin / RPC
// ---------------------------------------------------------------------------

// TranscriptionEvent is the payload streamed from the daemon to the main gleann
// application over gRPC.
type TranscriptionEvent struct {
	// Segments contains one or more timestamped transcriptions.
	Segments []Segment `json:"segments"`
	// Final indicates that no more segments will follow for this utterance.
	Final bool `json:"final"`
}

// TranscriptionHandler is a callback-style sink for streaming transcription
// events consumed by the gRPC plugin layer.
type TranscriptionHandler func(event TranscriptionEvent)
