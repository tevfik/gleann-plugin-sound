package audio

import (
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// Resample tests
// ---------------------------------------------------------------------------

func TestResample_SameRate(t *testing.T) {
	in := []int16{100, 200, 300, 400, 500}
	out, err := Resample(in, 16000, 16000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Same rate should return the original slice (no copy).
	if &out[0] != &in[0] {
		t.Error("expected same slice returned for identical rates")
	}
}

func TestResample_Downsample(t *testing.T) {
	// 48 kHz → 16 kHz is a 3:1 ratio.
	// Generate a simple ramp at 48 kHz.
	in := make([]int16, 480) // 10ms at 48kHz
	for i := range in {
		in[i] = int16(i)
	}

	out, err := Resample(in, 48000, 16000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expected output length ≈ 480 / 3 = 160.
	expectedLen := int(math.Ceil(float64(len(in)) / 3.0))
	if len(out) != expectedLen {
		t.Errorf("expected output length %d, got %d", expectedLen, len(out))
	}

	// First sample should match.
	if out[0] != in[0] {
		t.Errorf("first sample mismatch: want %d, got %d", in[0], out[0])
	}
}

func TestResample_Upsample(t *testing.T) {
	// 8 kHz → 16 kHz is a 1:2 ratio.
	in := []int16{0, 1000, 2000, 3000}
	out, err := Resample(in, 8000, 16000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expected output length ≈ 4 / 0.5 = 8.
	if len(out) != 8 {
		t.Errorf("expected 8 samples, got %d", len(out))
	}

	// Interpolated midpoint between 0 and 1000 should be ~500.
	if out[1] != 500 {
		t.Errorf("interpolated sample: want 500, got %d", out[1])
	}
}

func TestResample_EmptyInput(t *testing.T) {
	out, err := Resample(nil, 48000, 16000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != nil {
		t.Errorf("expected nil output for empty input, got %v", out)
	}
}

func TestResample_InvalidRates(t *testing.T) {
	_, err := Resample([]int16{1, 2, 3}, 0, 16000)
	if err == nil {
		t.Error("expected error for zero source rate")
	}

	_, err = Resample([]int16{1, 2, 3}, 16000, -1)
	if err == nil {
		t.Error("expected error for negative destination rate")
	}
}

// ---------------------------------------------------------------------------
// StereoToMono tests
// ---------------------------------------------------------------------------

func TestStereoToMono_Basic(t *testing.T) {
	// L=100 R=200 → mono = 150
	// L=1000 R=2000 → mono = 1500
	stereo := []int16{100, 200, 1000, 2000}
	mono := StereoToMono(stereo)

	if len(mono) != 2 {
		t.Fatalf("expected 2 mono samples, got %d", len(mono))
	}
	if mono[0] != 150 {
		t.Errorf("sample 0: want 150, got %d", mono[0])
	}
	if mono[1] != 1500 {
		t.Errorf("sample 1: want 1500, got %d", mono[1])
	}
}

func TestStereoToMono_Empty(t *testing.T) {
	mono := StereoToMono(nil)
	if len(mono) != 0 {
		t.Errorf("expected empty output, got %d samples", len(mono))
	}
}

func TestStereoToMono_NegativeValues(t *testing.T) {
	// Test with negative samples to verify averaging handles negative numbers.
	stereo := []int16{-1000, -2000, 500, -500}
	mono := StereoToMono(stereo)

	if mono[0] != -1500 {
		t.Errorf("sample 0: want -1500, got %d", mono[0])
	}
	if mono[1] != 0 {
		t.Errorf("sample 1: want 0, got %d", mono[1])
	}
}

// ---------------------------------------------------------------------------
// Int16ToFloat32 / Float32ToInt16 roundtrip tests
// ---------------------------------------------------------------------------

func TestInt16ToFloat32(t *testing.T) {
	pcm := []int16{0, 16384, -16384, 32767, -32768}
	f := Int16ToFloat32(pcm)

	if len(f) != len(pcm) {
		t.Fatalf("length mismatch: want %d, got %d", len(pcm), len(f))
	}

	// 0 → 0.0
	if f[0] != 0.0 {
		t.Errorf("sample 0: want 0.0, got %f", f[0])
	}
	// 16384 → ~0.5
	if math.Abs(float64(f[1])-0.5) > 0.001 {
		t.Errorf("sample 1: want ~0.5, got %f", f[1])
	}
	// -16384 → ~-0.5
	if math.Abs(float64(f[2])+0.5) > 0.001 {
		t.Errorf("sample 2: want ~-0.5, got %f", f[2])
	}
	// 32767 → ~1.0
	if math.Abs(float64(f[3])-1.0) > 0.001 {
		t.Errorf("sample 3: want ~1.0, got %f", f[3])
	}
	// -32768 → -1.0
	if f[4] != -1.0 {
		t.Errorf("sample 4: want -1.0, got %f", f[4])
	}
}

func TestFloat32ToInt16(t *testing.T) {
	f := []float32{0.0, 0.5, -0.5, 1.0, -1.0}
	pcm := Float32ToInt16(f)

	if len(pcm) != len(f) {
		t.Fatalf("length mismatch")
	}

	if pcm[0] != 0 {
		t.Errorf("sample 0: want 0, got %d", pcm[0])
	}
	// 0.5 * 32768 = 16384
	if pcm[1] != 16384 {
		t.Errorf("sample 1: want 16384, got %d", pcm[1])
	}
	if pcm[2] != -16384 {
		t.Errorf("sample 2: want -16384, got %d", pcm[2])
	}
}

func TestFloat32ToInt16_Clamping(t *testing.T) {
	// Values beyond [-1.0, 1.0] should be clamped to int16 range.
	f := []float32{2.0, -2.0}
	pcm := Float32ToInt16(f)

	if pcm[0] != math.MaxInt16 {
		t.Errorf("positive clamp: want %d, got %d", int16(math.MaxInt16), pcm[0])
	}
	if pcm[1] != math.MinInt16 {
		t.Errorf("negative clamp: want %d, got %d", int16(math.MinInt16), pcm[1])
	}
}

func TestInt16Float32Roundtrip(t *testing.T) {
	// A roundtrip int16 → float32 → int16 should be close to the original
	// (within ±1 due to floating point).
	original := []int16{0, 100, -100, 10000, -10000, 32767, -32768}
	f := Int16ToFloat32(original)
	back := Float32ToInt16(f)

	for i, want := range original {
		diff := int(back[i]) - int(want)
		if diff < -1 || diff > 1 {
			t.Errorf("sample %d: want %d, got %d (diff %d)", i, want, back[i], diff)
		}
	}
}
