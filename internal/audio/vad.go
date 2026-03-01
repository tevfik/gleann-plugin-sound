package audio

import (
	"math"
	"sync"
)

// ---------------------------------------------------------------------------
// Voice Activity Detection — energy-based
// ---------------------------------------------------------------------------

// VAD performs simple energy-based Voice Activity Detection on a stream of
// 16-bit PCM samples.  It maintains a running average of frame energy and
// compares each incoming chunk against a configurable threshold multiplier.
//
// This is intentionally simple: for gleann-sound we just need to skip silence
// to avoid sending dead air to Whisper.  A more sophisticated VAD (e.g.
// Silero-VAD) can be plugged in later by swapping this component.
type VAD struct {
	mu sync.Mutex

	// ThresholdMultiplier controls how far above the running average a frame
	// must be to count as speech.  A value of 2.0 means the frame energy must
	// be at least 2× the running average.  Higher = less sensitive.
	ThresholdMultiplier float64

	// MinAbsoluteEnergy is the minimum absolute RMS energy value for a frame
	// to be considered speech, regardless of the running average.  This
	// prevents detecting very quiet noise as speech when the running average
	// is near zero.
	MinAbsoluteEnergy float64

	// smoothingAlpha is the exponential moving average decay factor.
	// Closer to 1.0 means faster adaptation; 0.01 is a good default.
	smoothingAlpha float64

	// running average of frame energy (RMS).
	avgEnergy float64

	// initialised tracks whether we've seen at least one frame.
	initialised bool
}

// DefaultVAD returns a VAD with sensible defaults for dictation use.
func DefaultVAD() *VAD {
	return &VAD{
		ThresholdMultiplier: 2.0,
		MinAbsoluteEnergy:   150.0,
		smoothingAlpha:      0.02,
	}
}

// IsSpeech returns true if the given PCM chunk likely contains human speech
// based on its RMS energy compared to the running average.
//
// Thread-safe: may be called from the audio callback goroutine.
func (v *VAD) IsSpeech(pcm []int16) bool {
	if len(pcm) == 0 {
		return false
	}

	energy := rmsEnergy(pcm)

	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.initialised {
		// Bootstrap the running average with the first frame.
		v.avgEnergy = energy
		v.initialised = true
		// First frame — assume it's silence while we calibrate.
		return false
	}

	// Update exponential moving average of energy.
	v.avgEnergy = v.smoothingAlpha*energy + (1.0-v.smoothingAlpha)*v.avgEnergy

	// A frame is speech if its energy exceeds both the absolute floor AND the
	// dynamic threshold derived from the running average.
	threshold := v.avgEnergy * v.ThresholdMultiplier
	return energy > v.MinAbsoluteEnergy && energy > threshold
}

// Reset clears the running average so the VAD re-calibrates on the next chunk.
func (v *VAD) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.avgEnergy = 0
	v.initialised = false
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// rmsEnergy computes the Root Mean Square energy of a PCM buffer.
func rmsEnergy(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}
	var sumSq float64
	for _, s := range pcm {
		v := float64(s)
		sumSq += v * v
	}
	return math.Sqrt(sumSq / float64(len(pcm)))
}
