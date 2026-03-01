package audio

import (
	"fmt"
	"math"
)

// ---------------------------------------------------------------------------
// Resampler — integer sample-rate conversion for Whisper compatibility
// ---------------------------------------------------------------------------

// Resample converts a buffer of signed 16-bit PCM samples from srcRate to
// dstRate using linear interpolation.  Both rates must be > 0.
//
// The output is a new []int16 slice at the destination sample rate.
// If srcRate == dstRate the input is returned as-is (no copy).
//
// This is a simple but effective approach for the common case of converting
// 44.1/48 kHz capture to 16 kHz.  For higher-quality resampling (e.g.
// sinc-windowed), consider replacing this function with a proper DSP library.
func Resample(samples []int16, srcRate, dstRate int) ([]int16, error) {
	if srcRate <= 0 || dstRate <= 0 {
		return nil, fmt.Errorf("resample: invalid sample rates src=%d dst=%d", srcRate, dstRate)
	}
	if srcRate == dstRate {
		return samples, nil
	}
	if len(samples) == 0 {
		return nil, nil
	}

	ratio := float64(srcRate) / float64(dstRate)
	outLen := int(math.Ceil(float64(len(samples)) / ratio))
	out := make([]int16, outLen)

	for i := 0; i < outLen; i++ {
		// Exact fractional position in the source buffer.
		srcPos := float64(i) * ratio

		// Integer indices surrounding srcPos.
		idx0 := int(srcPos)
		idx1 := idx0 + 1
		if idx1 >= len(samples) {
			idx1 = len(samples) - 1
		}

		// Fractional part for linear interpolation.
		frac := srcPos - float64(idx0)
		v := float64(samples[idx0])*(1.0-frac) + float64(samples[idx1])*frac

		// Clamp to int16 range.
		if v > math.MaxInt16 {
			v = math.MaxInt16
		} else if v < math.MinInt16 {
			v = math.MinInt16
		}
		out[i] = int16(v)
	}

	return out, nil
}

// StereoToMono converts interleaved stereo 16-bit PCM to mono by averaging
// each pair of left/right samples.  If the input has an odd number of samples
// the last sample is kept as-is.
func StereoToMono(stereo []int16) []int16 {
	n := len(stereo) / 2
	mono := make([]int16, n)
	for i := 0; i < n; i++ {
		l := int32(stereo[i*2])
		r := int32(stereo[i*2+1])
		mono[i] = int16((l + r) / 2)
	}
	return mono
}

// Int16ToFloat32 converts a 16-bit PCM buffer to 32-bit float in [-1.0, 1.0]
// range.  Whisper's C API often expects float32 input.
func Int16ToFloat32(pcm []int16) []float32 {
	out := make([]float32, len(pcm))
	for i, s := range pcm {
		out[i] = float32(s) / 32768.0
	}
	return out
}

// Float32ToInt16 converts 32-bit float PCM in [-1.0, 1.0] to signed 16-bit.
func Float32ToInt16(f []float32) []int16 {
	out := make([]int16, len(f))
	for i, s := range f {
		v := s * 32768.0
		if v > math.MaxInt16 {
			v = math.MaxInt16
		} else if v < math.MinInt16 {
			v = math.MinInt16
		}
		out[i] = int16(v)
	}
	return out
}
