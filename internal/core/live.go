package core

import (
	"context"
	"time"
)

// LiveUpdate is what a LiveStream reports after consuming audio.
type LiveUpdate struct {
	// Delta is newly finalised text since the previous update. Words are only
	// released once they cannot change any more, so Delta can be typed or
	// appended as-is. It starts with a space when it continues an utterance.
	Delta string
	// Pending is the unfinished tail word the model is still working on. It is
	// meant for a live preview only and is never repeated in a later Delta.
	Pending string
	// Audio is the total amount of audio consumed by the stream so far.
	Audio time.Duration
}

// LiveStream is an incremental recognition session. Audio goes in as it is
// captured; text comes out with sub-second latency. A stream is not safe for
// concurrent use.
type LiveStream interface {
	// Accept consumes PCM (16 kHz mono) and returns any newly finalised text.
	Accept(ctx context.Context, pcm []int16) (LiveUpdate, error)
	// Flush processes the audio still buffered and releases every remaining
	// word, including the pending one. The stream can be reused after Reset.
	Flush(ctx context.Context) (LiveUpdate, error)
	// Reset starts a new utterance: decoder state and buffers are cleared.
	Reset()
	// Close releases the stream.
	Close() error
}

// LiveTranscriber is implemented by backends with native streaming support
// (cache-aware models such as Nemotron). Prefer it over StreamingTranscriber,
// which re-decodes overlapping windows.
type LiveTranscriber interface {
	Transcriber
	NewLiveStream() (LiveStream, error)
}

// LanguageLiveTranscriber lets each stream pick its own language, which a
// server needs when sessions with different languages share one model.
type LanguageLiveTranscriber interface {
	LiveTranscriber
	NewLiveStreamLang(lang string) (LiveStream, error)
}
