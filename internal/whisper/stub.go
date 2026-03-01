//go:build !whisper
// +build !whisper

// Package whisper provides a stub Transcriber used when building without the
// "whisper" build tag.  This allows development, CI, and testing of the rest
// of the pipeline without requiring whisper.cpp or CGO.
package whisper

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/tevfik/gleann-sound/internal/core"
)

// ---------------------------------------------------------------------------
// StubEngine — no-op transcriber for non-CGO builds
// ---------------------------------------------------------------------------

// Engine is a stub that always returns a placeholder transcription.
// It satisfies core.Transcriber so the rest of the application compiles and
// runs without whisper.cpp.
type Engine struct {
	model string
	lang  string
}

// Compile-time interface check.
var _ core.Transcriber = (*Engine)(nil)

func init() {
	core.RegisterBackend("whisper", func(model string) (core.Transcriber, error) {
		return NewEngine(model)
	})
}

// NewEngine creates a stub engine.  The model path is stored but never loaded.
func NewEngine(model string) (*Engine, error) {
	log.Printf("[whisper-stub] model path noted (not loaded): %s", model)
	return &Engine{model: model}, nil
}

// SetLanguage sets the language code (no-op for stub).
func (e *Engine) SetLanguage(lang string) {
	e.lang = lang
	log.Printf("[whisper-stub] language set to: %q (no-op)", lang)
}

// TranscribeStream returns a placeholder string representing the audio length.
func (e *Engine) TranscribeStream(_ context.Context, pcmData []int16) (string, error) {
	dur := time.Duration(len(pcmData)) * time.Second / 16000
	text := fmt.Sprintf("[stub: %.1fs of audio — whisper not linked]", dur.Seconds())
	log.Printf("[whisper-stub] TranscribeStream: %d samples → %s", len(pcmData), text)
	return text, nil
}

// TranscribeStreamSegments returns a single stub segment.
func (e *Engine) TranscribeStreamSegments(_ context.Context, pcmData []int16) ([]core.Segment, error) {
	dur := time.Duration(len(pcmData)) * time.Second / 16000
	return []core.Segment{
		{
			Start: 0,
			End:   dur,
			Text:  fmt.Sprintf("[stub: %.1fs of audio — whisper not linked]", dur.Seconds()),
		},
	}, nil
}

// TranscribeFile returns a single stub segment.
func (e *Engine) TranscribeFile(_ context.Context, filepath string) ([]core.Segment, error) {
	return []core.Segment{
		{
			Start: 0,
			End:   0,
			Text:  fmt.Sprintf("[stub: file %q — whisper not linked]", filepath),
		},
	}, nil
}

// Close is a no-op for the stub engine.
func (e *Engine) Close() error {
	log.Println("[whisper-stub] engine closed (no-op)")
	return nil
}
