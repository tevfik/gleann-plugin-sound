//go:build onnx
// +build onnx

package nemotron

import (
	"math"
)

const (
	sampleRate   = 16000
	nFFT         = 512
	windowLength = 400
	hopLength    = 160
	nMels        = 128
	fftPaddedSize = 512
)

// fft computes the discrete Fourier transform of complex input (real, imag) in-place.
// Input length n must be a power of 2 (512). Cooley-Tukey radix-2 decimation-in-time.
func fft(real, imag []float64, n int) {
	// Bit-reversal permutation.
	j := 0
	for i := 0; i < n-1; i++ {
		if i < j {
			real[i], real[j] = real[j], real[i]
			imag[i], imag[j] = imag[j], imag[i]
		}
		m := n >> 1
		for m >= 1 && j >= m {
			j -= m
			m >>= 1
		}
		j += m
	}

	// Butterfly stages.
	for stage := 1; stage < n; stage <<= 1 {
		angle := -math.Pi / float64(stage)
		wReal := math.Cos(angle)
		wImag := math.Sin(angle)
		for k := 0; k < n; k += stage << 1 {
			tReal, tImag := 1.0, 0.0
			for m := 0; m < stage; m++ {
				uIdx := k + m
				vIdx := k + m + stage
				vr := tReal*real[vIdx] - tImag*imag[vIdx]
				vi := tReal*imag[vIdx] + tImag*real[vIdx]
				real[vIdx] = real[uIdx] - vr
				imag[vIdx] = imag[uIdx] - vi
				real[uIdx] += vr
				imag[uIdx] += vi
				tReal, tImag = tReal*wReal-tImag*wImag, tReal*wImag+tImag*wReal
			}
		}
	}
}

// fftMagnitudeSquared computes |FFT(windowed_frame)|² for a single audio frame.
func fftMagnitudeSquared(frame []float32, window []float32, fftReal, fftImag []float64, out []float32) {
	n := fftPaddedSize

	// Zero the work buffers.
	for i := range fftReal[:n] {
		fftReal[i] = 0
		fftImag[i] = 0
	}

	// Apply window and copy into real part (zero-padded beyond windowLength).
	frameLen := len(frame)
	if frameLen > windowLength {
		frameLen = windowLength
	}
	for i := 0; i < frameLen; i++ {
		fftReal[i] = float64(frame[i]) * float64(window[i])
	}

	// In-place FFT.
	fft(fftReal, fftImag, n)

	// Extract magnitude squared for first nFFT/2+1 bins.
	fftBins := nFFT/2 + 1
	for k := 0; k < fftBins; k++ {
		out[k] = float32(fftReal[k]*fftReal[k] + fftImag[k]*fftImag[k])
	}
}

// buildHannWindow creates a Hann window of size N.
func buildHannWindow(n int) []float32 {
	w := make([]float32, n)
	for i := 0; i < n; i++ {
		w[i] = float32(0.5 * (1.0 - math.Cos(2.0*math.Pi*float64(i)/float64(n-1))))
	}
	return w
}

// buildMelFilterbank creates triangular mel filterbank weights.
// Returns [nMels × (nFFT/2+1)] float32.
func buildMelFilterbank(sr, fftSize, numMels int) []float32 {
	fftBins := fftSize/2 + 1

	// Convert Hz to mel scale.
	hzToMel := func(hz float64) float64 {
		return 2595.0 * math.Log10(1.0+hz/700.0)
	}
	melToHz := func(mel float64) float64 {
		return 700.0 * (math.Pow(10.0, mel/2595.0) - 1.0)
	}

	melMin := hzToMel(0)
	melMax := hzToMel(float64(sr) / 2.0)

	// numMels + 2 equally spaced points in mel space.
	melPoints := make([]float64, numMels+2)
	for i := range melPoints {
		melPoints[i] = melMin + (melMax-melMin)*float64(i)/float64(numMels+1)
	}

	// Convert back to Hz then to FFT bin indices.
	binIndices := make([]float64, numMels+2)
	for i, m := range melPoints {
		hz := melToHz(m)
		binIndices[i] = hz * float64(fftSize) / float64(sr)
	}

	// Build triangular filters.
	filters := make([]float32, numMels*fftBins)
	for m := 0; m < numMels; m++ {
		left := binIndices[m]
		center := binIndices[m+1]
		right := binIndices[m+2]

		for k := 0; k < fftBins; k++ {
			fk := float64(k)
			if fk >= left && fk <= center && center > left {
				filters[m*fftBins+k] = float32((fk - left) / (center - left))
			} else if fk > center && fk <= right && right > center {
				filters[m*fftBins+k] = float32((right - fk) / (right - center))
			}
		}
	}

	return filters
}
