//go:build onnx
// +build onnx

package nemotron

import (
	"math"
	"testing"
)

// naiveDFT computes a reference DFT using the O(N²) formula for correctness testing.
func naiveDFT(real, imag []float64, n int) ([]float64, []float64) {
	outR := make([]float64, n)
	outI := make([]float64, n)
	for k := 0; k < n; k++ {
		for j := 0; j < n; j++ {
			angle := 2.0 * math.Pi * float64(k) * float64(j) / float64(n)
			outR[k] += real[j]*math.Cos(angle) + imag[j]*math.Sin(angle)
			outI[k] += -real[j]*math.Sin(angle) + imag[j]*math.Cos(angle)
		}
	}
	return outR, outI
}

func TestFFT_Impulse(t *testing.T) {
	n := 16
	real := make([]float64, n)
	imag := make([]float64, n)
	real[0] = 1.0

	fft(real, imag, n)

	for k := 0; k < n; k++ {
		if math.Abs(real[k]-1.0) > 1e-10 {
			t.Errorf("bin %d: real = %f, want 1.0", k, real[k])
		}
		if math.Abs(imag[k]) > 1e-10 {
			t.Errorf("bin %d: imag = %f, want 0.0", k, imag[k])
		}
	}
}

func TestFFT_SineWave(t *testing.T) {
	n := 64
	real := make([]float64, n)
	imag := make([]float64, n)
	for i := 0; i < n; i++ {
		real[i] = math.Sin(2.0 * math.Pi * 4.0 * float64(i) / float64(n))
	}

	fft(real, imag, n)

	for k := 0; k < n; k++ {
		mag := math.Sqrt(real[k]*real[k] + imag[k]*imag[k])
		if k == 4 || k == n-4 {
			if mag < float64(n)/2.0-1.0 {
				t.Errorf("bin %d: magnitude = %f, expected ~%f", k, mag, float64(n)/2.0)
			}
		} else {
			if mag > 1e-8 {
				t.Errorf("bin %d: magnitude = %f, expected ~0", k, mag)
			}
		}
	}
}

func TestFFT_MatchesNaiveDFT(t *testing.T) {
	n := 128
	real := make([]float64, n)
	imag := make([]float64, n)
	for i := 0; i < n; i++ {
		real[i] = math.Sin(float64(i)*0.3) + math.Cos(float64(i)*0.7)
		imag[i] = math.Sin(float64(i) * 0.5)
	}

	refR, refI := naiveDFT(real, imag, n)

	fft(real, imag, n)

	for k := 0; k < n; k++ {
		if math.Abs(real[k]-refR[k]) > 1e-6 {
			t.Errorf("bin %d: real = %f, naive = %f", k, real[k], refR[k])
		}
		if math.Abs(imag[k]-refI[k]) > 1e-6 {
			t.Errorf("bin %d: imag = %f, naive = %f", k, imag[k], refI[k])
		}
	}
}

func BenchmarkFFT512(b *testing.B) {
	n := 512
	real := make([]float64, n)
	imag := make([]float64, n)
	for i := 0; i < n; i++ {
		real[i] = math.Sin(float64(i) * 0.1)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < n; j++ {
			real[j] = math.Sin(float64(j) * 0.1)
			imag[j] = 0
		}
		fft(real, imag, n)
	}
}
