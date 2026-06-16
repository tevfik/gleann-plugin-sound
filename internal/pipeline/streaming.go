package pipeline

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// SampleRate is Whisper's required sample rate.
const SampleRate = 16000

// Config controls the sliding-window streaming pipeline behaviour.
type Config struct {
	// WindowSizeSec is the total length of each transcription window in seconds.
	// Larger windows give whisper more context but increase latency.
	// Default: 5.0
	WindowSizeSec float64

	// StepSizeSec is how far the window advances between transcriptions.
	// Must be <= WindowSizeSec.  The overlap is WindowSizeSec - StepSizeSec.
	// Default: 3.0
	StepSizeSec float64

	// MinSpeechSec is the minimum amount of speech in a window (via VAD) to
	// bother sending it to the transcriber.  Windows below this are skipped.
	// Default: 0.15
	MinSpeechSec float64

	// MaxWindowsBeforeReset is the number of successful transcription windows
	// after which the pipeline performs a soft reset (clear prompt, dedup state,
	// recalibrate VAD).  This prevents prompt poisoning buildup in long-running
	// listen sessions.  0 = disabled (e.g. for short dictation sessions).
	// Default: 10 (~30s with 3s steps)
	MaxWindowsBeforeReset int

	// OnError is called when a transcription error occurs. The callback receives
	// the error and the number of consecutive errors so far.
	// Default: nil (errors are only logged)
	OnError func(err error, consecutiveCount int)

	// MaxConsecutiveErrors is the threshold after which the pipeline logs a
	// warning about sustained failures. Default: 5
	MaxConsecutiveErrors int
}

// DefaultConfig returns sensible defaults for whisper.cpp (fast native inference).
func DefaultConfig() Config {
	return Config{
		WindowSizeSec:         5.0,
		StepSizeSec:           3.0,
		MinSpeechSec:          0.15,
		MaxWindowsBeforeReset: 10,
		MaxConsecutiveErrors:  5,
	}
}

// ONNXConfig returns a config tuned for ONNX Runtime CPU inference which is
// significantly slower than whisper.cpp. Larger windows with a bigger step
// reduce the number of inference calls per minute.
func ONNXConfig() Config {
	return Config{
		WindowSizeSec:         10.0,
		StepSizeSec:           8.0,
		MinSpeechSec:          0.3,
		MaxWindowsBeforeReset: 5,
		MaxConsecutiveErrors:  5,
	}
}

// OnResult is the callback type for streaming transcription results.
type OnResult func(result core.StreamResult)

// StreamingPipeline orchestrates sliding-window transcription.
// It accumulates PCM chunks from the audio capturer, forms overlapping windows,
// runs VAD pre-checks, transcribes with context carryover, and deduplicates
// boundary text.
type StreamingPipeline struct {
	transcriber core.StreamingTranscriber
	vad         core.VADProvider
	cfg         Config

	windowSamples  int // windowSizeSec × SampleRate
	stepSamples    int // stepSizeSec × SampleRate
	overlapSamples int // windowSamples - stepSamples
	minSpeechSamp  int // minSpeechSec × SampleRate

	mu         sync.Mutex
	buf        []int16 // accumulation buffer
	overlapBuf []int16 // overlap from previous window
	prevText   string  // previous window text for dedup
	promptText string  // prompt text to carry forward for context
	windowSeq  int     // monotonic counter
	startTime  time.Time

	// Repetition guard: count consecutive same/empty results.
	// When too many occur, reset the prompt to break decoder loops.
	repeatCount int

	// Periodic reset counter: tracks successful windows since last reset.
	windowsSinceReset int

	// consecutiveErrors tracks how many transcription errors have occurred in a row.
	consecutiveErrors int
}

// NewStreamingPipeline creates a pipeline with the given transcriber, VAD, and config.
func NewStreamingPipeline(transcriber core.StreamingTranscriber, vad core.VADProvider, cfg Config) *StreamingPipeline {
	if cfg.WindowSizeSec <= 0 {
		cfg.WindowSizeSec = DefaultConfig().WindowSizeSec
	}
	if cfg.StepSizeSec <= 0 {
		cfg.StepSizeSec = DefaultConfig().StepSizeSec
	}
	if cfg.StepSizeSec > cfg.WindowSizeSec {
		cfg.StepSizeSec = cfg.WindowSizeSec
	}
	if cfg.MinSpeechSec <= 0 {
		cfg.MinSpeechSec = DefaultConfig().MinSpeechSec
	}
	if cfg.MaxConsecutiveErrors <= 0 {
		cfg.MaxConsecutiveErrors = 5
	}

	windowSamples := int(cfg.WindowSizeSec * SampleRate)
	stepSamples := int(cfg.StepSizeSec * SampleRate)
	overlapSamples := windowSamples - stepSamples

	return &StreamingPipeline{
		transcriber:    transcriber,
		vad:            vad,
		cfg:            cfg,
		windowSamples:  windowSamples,
		stepSamples:    stepSamples,
		overlapSamples: overlapSamples,
		minSpeechSamp:  int(cfg.MinSpeechSec * SampleRate),
		buf:            make([]int16, 0, windowSamples*2),
	}
}

// Run starts the streaming pipeline.  It reads PCM chunks from audioCh,
// forms sliding windows, and calls onResult for each transcribed window.
// Blocks until ctx is cancelled or audioCh is closed.
func (p *StreamingPipeline) Run(ctx context.Context, audioCh <-chan []int16, onResult OnResult) error {
	p.mu.Lock()
	p.startTime = time.Now()
	p.windowSeq = 0
	p.prevText = ""
	p.promptText = ""
	p.buf = p.buf[:0]
	p.overlapBuf = nil
	p.mu.Unlock()

	p.transcriber.ResetStream()

	for {
		select {
		case <-ctx.Done():
			// Flush any remaining audio.
			p.flush(ctx, onResult)
			return ctx.Err()

		case chunk, ok := <-audioCh:
			if !ok {
				// Channel closed — flush remaining.
				p.flush(ctx, onResult)
				return nil
			}

			p.mu.Lock()
			p.buf = append(p.buf, chunk...)

			// Adaptive skip: if buffer has accumulated more than 2× window
			// worth of audio, we're falling behind. Skip to the most recent
			// window's worth to stay current instead of processing stale audio.
			if len(p.buf) > p.windowSamples*2 {
				skip := len(p.buf) - p.windowSamples
				skippedSec := float64(skip) / SampleRate
				p.buf = p.buf[skip:]
				p.overlapBuf = nil // overlap is invalid after skip
				p.promptText = ""  // context is stale after gap
				p.prevText = ""    // dedup state is invalid
				log.Printf("[pipeline] skipped %.1fs of buffered audio — context reset", skippedSec)
			}

			// Process all complete step windows available.
			for len(p.buf) >= p.stepSamples {
				// Build the full window: overlap prefix + step data.
				var window []int16
				if len(p.overlapBuf) > 0 {
					window = make([]int16, 0, p.windowSamples)
					window = append(window, p.overlapBuf...)
					window = append(window, p.buf[:p.stepSamples]...)
				} else {
					// First window or no overlap yet — use what we have.
					end := p.windowSamples
					if end > len(p.buf) {
						end = len(p.buf)
					}
					window = make([]int16, end)
					copy(window, p.buf[:end])
				}

				// Save overlap for next window (last overlapSamples of the current window).
				if p.overlapSamples > 0 && len(window) >= p.overlapSamples {
					p.overlapBuf = make([]int16, p.overlapSamples)
					copy(p.overlapBuf, window[len(window)-p.overlapSamples:])
				}

				// Advance buffer past the step.
				p.buf = p.buf[p.stepSamples:]

				seq := p.windowSeq
				p.windowSeq++
				prevText := p.prevText
				promptText := p.promptText
				p.mu.Unlock()

				// VAD check — does this window contain enough speech?
				if !p.hasSufficientSpeech(window) {
					log.Printf("[pipeline] window #%d: insufficient speech — skipping", seq)
					p.mu.Lock()
					continue
				}

				// Compute timing.
				windowStart := time.Duration(float64(seq)*p.cfg.StepSizeSec*1000) * time.Millisecond
				windowEnd := windowStart + time.Duration(float64(len(window))/SampleRate*1000)*time.Millisecond

				// Transcribe with context.
				result, nextPrompt, err := p.transcriber.TranscribeWindow(ctx, window, promptText)
				if err != nil {
					p.mu.Lock()
					p.consecutiveErrors++
					ce := p.consecutiveErrors
					p.mu.Unlock()

					log.Printf("[pipeline] window #%d: transcription error: %v", seq, err)
					if p.cfg.OnError != nil {
						p.cfg.OnError(err, ce)
					}
					maxCE := p.cfg.MaxConsecutiveErrors
					if maxCE <= 0 {
						maxCE = 5
					}
					if ce >= maxCE {
						log.Printf("[pipeline] WARNING: %d consecutive transcription errors", ce)
					}
					p.mu.Lock()
					continue
				}

				// Successful transcription — reset consecutive error counter.
				p.mu.Lock()
				p.consecutiveErrors = 0
				p.mu.Unlock()

				// Filter repetitive/hallucinated output at the pipeline level.
				if result.Text != "" && (core.IsRepetitive(result.Text) || core.IsHallucination(result.Text)) {
					log.Printf("[pipeline] window #%d: filtered: %q", seq, result.Text)
					result.Text = ""
				}

				if result.Text == "" {
					p.mu.Lock()
					p.repeatCount++
					// After 2 empty/filtered results in a row, the prompt may be
					// poisoning the decoder. Reset it to break the loop.
					if p.repeatCount >= 2 && p.promptText != "" {
						log.Printf("[pipeline] resetting prompt after %d empty windows", p.repeatCount)
						p.promptText = ""
						p.prevText = ""
						p.repeatCount = 0
						p.transcriber.ResetStream()
					}
					continue
				}

				// Check if result is same as previous (stuck decoder loop).
				// Immediate reset — if the decoder repeats itself even once,
				// the prompt is likely poisoned.
				if core.IsSameAsLast(prevText, result.Text) {
					log.Printf("[pipeline] window #%d: same as previous, resetting: %q", seq, result.Text)
					p.mu.Lock()
					p.promptText = ""
					p.prevText = ""
					p.repeatCount = 0
					p.transcriber.ResetStream()
					continue
				}

				// Apply deduplication.  Prefer timestamp-based dedup when
				// per-segment timestamps are available (whisper backend);
				// fall back to text-based dedup otherwise (ONNX or first window).
				var deduped string
				if len(result.Segments) > 0 && seq > 0 {
					overlapSec := p.cfg.WindowSizeSec - p.cfg.StepSizeSec
					kept := DeduplicateByTimestamp(result.Segments, overlapSec)
					deduped = JoinSegments(kept)
					result.Segments = kept
				} else {
					deduped = DeduplicateOverlap(prevText, result.Text)
				}

				result.Text = deduped
				result.Start = windowStart
				result.End = windowEnd
				result.WindowSeq = seq

				if deduped != "" {
					onResult(result)
				}

				p.mu.Lock()
				p.repeatCount = 0 // Good result — reset repetition counter.
				p.windowsSinceReset++
				p.prevText = result.Text
				if deduped == "" {
					// If nothing new after dedup, keep old prevText.
					p.prevText = prevText
				}
				p.promptText = nextPrompt

				// Periodic soft reset to prevent gradual prompt poisoning
				// in long-running sessions (listen mode).
				if p.cfg.MaxWindowsBeforeReset > 0 && p.windowsSinceReset >= p.cfg.MaxWindowsBeforeReset {
					log.Printf("[pipeline] periodic reset after %d windows", p.windowsSinceReset)
					p.promptText = ""
					p.prevText = ""
					p.windowsSinceReset = 0
					if rc, ok := p.vad.(interface{ Recalibrate() }); ok {
						rc.Recalibrate()
					}
				}
			}
			p.mu.Unlock()
		}
	}
}

// Flush processes any remaining audio in the buffer as a final window.
func (p *StreamingPipeline) flush(ctx context.Context, onResult OnResult) {
	p.mu.Lock()
	remaining := p.buf
	p.buf = nil
	overlapBuf := p.overlapBuf
	seq := p.windowSeq
	p.windowSeq++
	prevText := p.prevText
	promptText := p.promptText
	p.mu.Unlock()

	log.Printf("[pipeline] flush called: remaining=%d samples, overlap=%d samples, minSpeechSamp=%d", len(remaining), len(overlapBuf), p.minSpeechSamp)

	if len(remaining) < p.minSpeechSamp {
		log.Printf("[pipeline] flush skipped: remaining samples (%d) < minSpeechSamp (%d)", len(remaining), p.minSpeechSamp)
		return
	}

	// Build final window with overlap prefix.
	var window []int16
	if len(overlapBuf) > 0 {
		window = make([]int16, 0, len(overlapBuf)+len(remaining))
		window = append(window, overlapBuf...)
		window = append(window, remaining...)
	} else {
		window = remaining
	}

	if !p.hasSufficientSpeech(window) {
		log.Printf("[pipeline] flush skipped: insufficient speech detected in window")
		return
	}

	windowStart := time.Duration(float64(seq)*p.cfg.StepSizeSec*1000) * time.Millisecond
	windowEnd := windowStart + time.Duration(float64(len(window))/SampleRate*1000)*time.Millisecond

	log.Printf("[pipeline] flush transcribing window #%d (%d samples)...", seq, len(window))
	result, _, err := p.transcriber.TranscribeWindow(ctx, window, promptText)
	if err != nil {
		log.Printf("[pipeline] flush window #%d: transcription error: %v", seq, err)
		return
	}

	if result.Text == "" {
		log.Printf("[pipeline] flush window #%d: transcribed empty text", seq)
		return
	}

	var deduped string
	if len(result.Segments) > 0 && seq > 0 {
		overlapSec := p.cfg.WindowSizeSec - p.cfg.StepSizeSec
		kept := DeduplicateByTimestamp(result.Segments, overlapSec)
		deduped = JoinSegments(kept)
		result.Segments = kept
	} else {
		deduped = DeduplicateOverlap(prevText, result.Text)
	}
	result.Text = deduped
	result.Start = windowStart
	result.End = windowEnd
	result.WindowSeq = seq
	result.IsFinal = true

	if deduped != "" {
		onResult(result)
	}
}

// hasSufficientSpeech checks if the window contains enough speech frames
// (via VAD) to be worth transcribing.
func (p *StreamingPipeline) hasSufficientSpeech(window []int16) bool {
	if p.vad == nil {
		return true // no VAD — always process
	}

	// Check speech in 30ms chunks (480 samples @ 16kHz).
	const chunkSize = SampleRate * 30 / 1000 // 480
	speechSamples := 0
	for i := 0; i+chunkSize <= len(window); i += chunkSize {
		if p.vad.IsSpeech(window[i : i+chunkSize]) {
			speechSamples += chunkSize
		}
	}

	return speechSamples >= p.minSpeechSamp
}

// Reset clears all pipeline state for a new session.
func (p *StreamingPipeline) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = p.buf[:0]
	p.overlapBuf = nil
	p.prevText = ""
	p.promptText = ""
	p.windowSeq = 0
	p.transcriber.ResetStream()
	if p.vad != nil {
		p.vad.Reset()
	}
}
