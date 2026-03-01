package audio

import (
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// VAD unit tests
// ---------------------------------------------------------------------------

func TestDefaultVAD_Defaults(t *testing.T) {
	v := DefaultVAD()
	if v.ThresholdMultiplier != 2.0 {
		t.Errorf("ThresholdMultiplier: want 2.0, got %f", v.ThresholdMultiplier)
	}
	if v.MinAbsoluteEnergy != 150.0 {
		t.Errorf("MinAbsoluteEnergy: want 150.0, got %f", v.MinAbsoluteEnergy)
	}
}

func TestVAD_EmptyBuffer(t *testing.T) {
	v := DefaultVAD()
	if v.IsSpeech(nil) {
		t.Error("IsSpeech should return false for nil input")
	}
	if v.IsSpeech([]int16{}) {
		t.Error("IsSpeech should return false for empty input")
	}
}

func TestVAD_FirstFrameAlwaysFalse(t *testing.T) {
	v := DefaultVAD()
	// Even a loud frame should be false on the first call (calibration).
	loud := makeTone(1000, 5000)
	if v.IsSpeech(loud) {
		t.Error("first frame should return false (calibration)")
	}
}

func TestVAD_SilenceDetection(t *testing.T) {
	v := DefaultVAD()

	// Feed a few frames of near-silence to calibrate.
	silence := make([]int16, 480)
	for i := 0; i < 10; i++ {
		v.IsSpeech(silence)
	}

	// Silence with tiny noise should not be detected as speech.
	quietNoise := make([]int16, 480)
	for i := range quietNoise {
		quietNoise[i] = int16(i % 5) // very low energy
	}
	if v.IsSpeech(quietNoise) {
		t.Error("near-silence should not be classified as speech")
	}
}

func TestVAD_SpeechDetection(t *testing.T) {
	v := DefaultVAD()

	// Calibrate with silence.
	silence := make([]int16, 480)
	for i := 0; i < 50; i++ {
		v.IsSpeech(silence)
	}

	// A loud signal should be detected as speech.
	loud := makeTone(480, 5000)
	if !v.IsSpeech(loud) {
		t.Error("loud signal should be classified as speech after silence calibration")
	}
}

func TestVAD_Reset(t *testing.T) {
	v := DefaultVAD()

	// Feed some data.
	v.IsSpeech(makeTone(480, 3000))
	v.IsSpeech(makeTone(480, 3000))

	// Reset should clear the running average.
	v.Reset()

	// After reset, first frame should return false again (re-calibration).
	if v.IsSpeech(makeTone(480, 3000)) {
		t.Error("after Reset, first frame should return false")
	}
}

func TestVAD_MinAbsoluteEnergyGuard(t *testing.T) {
	v := DefaultVAD()
	v.MinAbsoluteEnergy = 10000.0 // very high floor

	// Calibrate with silence.
	silence := make([]int16, 480)
	for i := 0; i < 20; i++ {
		v.IsSpeech(silence)
	}

	// Medium-volume signal — above the dynamic threshold but below the
	// absolute floor.
	medium := makeTone(480, 500)
	if v.IsSpeech(medium) {
		t.Error("signal below MinAbsoluteEnergy should not be speech")
	}
}

// ---------------------------------------------------------------------------
// rmsEnergy tests
// ---------------------------------------------------------------------------

func TestRmsEnergy_Zero(t *testing.T) {
	e := rmsEnergy([]int16{0, 0, 0, 0})
	if e != 0 {
		t.Errorf("expected 0 energy for all-zero buffer, got %f", e)
	}
}

func TestRmsEnergy_Empty(t *testing.T) {
	e := rmsEnergy(nil)
	if e != 0 {
		t.Errorf("expected 0 energy for nil buffer, got %f", e)
	}
}

func TestRmsEnergy_Known(t *testing.T) {
	// All same value: RMS = |value|.
	pcm := []int16{1000, 1000, 1000, 1000}
	e := rmsEnergy(pcm)
	if math.Abs(e-1000.0) > 0.01 {
		t.Errorf("expected RMS ≈ 1000, got %f", e)
	}
}

func TestRmsEnergy_Negative(t *testing.T) {
	// RMS of {-500, -500} should equal 500.
	pcm := []int16{-500, -500}
	e := rmsEnergy(pcm)
	if math.Abs(e-500.0) > 0.01 {
		t.Errorf("expected RMS ≈ 500, got %f", e)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// makeTone creates a simple sine-ish tone buffer with the given amplitude.
func makeTone(nSamples int, amplitude int16) []int16 {
	out := make([]int16, nSamples)
	for i := range out {
		// Simple sine wave at ~440 Hz (for 16 kHz sample rate).
		phase := 2.0 * math.Pi * 440.0 * float64(i) / 16000.0
		out[i] = int16(float64(amplitude) * math.Sin(phase))
	}
	return out
}
