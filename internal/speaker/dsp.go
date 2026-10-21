// Package speaker identifies who is talking by comparing voice embeddings
// (NVIDIA TitaNet-small over ONNX Runtime) with enrolled voice profiles.
//
// Unsupervised diarization turned out to be unreliable when a TV or radio is
// playing in the room: every voice becomes a "speaker". Enrolled voices are
// robust to that (100 % turn accuracy at 20 dB, 97 % at 10 dB in our
// tests), and anyone who matches nobody stays "unknown" instead of being
// forced into a cluster.
package speaker

import (
	"math"
)

const (
	sampleRate = 16000
	nFFT       = 512
	winLength  = 400
	hopLength  = 160
	numMels    = 80
	preemph    = 0.97
	logGuard   = 1.0 / (1 << 24) // 2^-24
)

// fft is an in-place radix-2 FFT of length len(re) (a power of two).
func fft(re, im []float64) {
	n := len(re)
	for i, j := 0, 0; i < n-1; i++ {
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
		m := n >> 1
		for m >= 1 && j >= m {
			j -= m
			m >>= 1
		}
		j += m
	}
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		ang := -2 * math.Pi / float64(size)
		wr, wi := math.Cos(ang), math.Sin(ang)
		for start := 0; start < n; start += size {
			cr, ci := 1.0, 0.0
			for k := 0; k < half; k++ {
				a, b := start+k, start+k+half
				tr := re[b]*cr - im[b]*ci
				ti := re[b]*ci + im[b]*cr
				re[b], im[b] = re[a]-tr, im[a]-ti
				re[a], im[a] = re[a]+tr, im[a]+ti
				cr, ci = cr*wr-ci*wi, cr*wi+ci*wr
			}
		}
	}
}

// melFilterbank builds librosa's Slaney-scaled, area-normalised triangular
// filters: [numMels][nFFT/2+1].
func melFilterbank() [][]float64 {
	const fSp = 200.0 / 3.0
	const minLogHz = 1000.0
	minLogMel := minLogHz / fSp
	logStep := math.Log(6.4) / 27.0
	hzToMel := func(hz float64) float64 {
		if hz < minLogHz {
			return hz / fSp
		}
		return minLogMel + math.Log(hz/minLogHz)/logStep
	}
	melToHz := func(m float64) float64 {
		if m < minLogMel {
			return fSp * m
		}
		return minLogHz * math.Exp(logStep*(m-minLogMel))
	}
	bins := nFFT/2 + 1
	hz := make([]float64, numMels+2)
	lo, hi := hzToMel(0), hzToMel(sampleRate/2)
	for i := range hz {
		hz[i] = melToHz(lo + (hi-lo)*float64(i)/float64(numMels+1))
	}
	fb := make([][]float64, numMels)
	for m := 0; m < numMels; m++ {
		fb[m] = make([]float64, bins)
		enorm := 2.0 / (hz[m+2] - hz[m])
		for k := 0; k < bins; k++ {
			f := float64(k) * sampleRate / nFFT
			lower := (f - hz[m]) / (hz[m+1] - hz[m])
			upper := (hz[m+2] - f) / (hz[m+2] - hz[m+1])
			fb[m][k] = math.Max(0, math.Min(lower, upper)) * enorm
		}
	}
	return fb
}

var fbank = melFilterbank()

// Features computes TitaNet's input from 16 kHz mono samples in [-1, 1]:
// log-mel with NeMo's recipe (pre-emphasis, centred reflect-padded STFT,
// symmetric Hann window, Slaney mel, ln(x+2^-24), last frame dropped,
// per-feature mean/variance normalisation). Result is [numMels][frames].
func Features(x []float32) [][]float32 {
	n := len(x)
	if n < nFFT/2+1 {
		return nil
	}
	// Pre-emphasis.
	pe := make([]float64, n)
	pe[0] = float64(x[0])
	for i := 1; i < n; i++ {
		pe[i] = float64(x[i]) - preemph*float64(x[i-1])
	}
	// Centre: reflect-pad nFFT/2 on both sides.
	pad := nFFT / 2
	padded := make([]float64, n+2*pad)
	copy(padded[pad:], pe)
	for i := 1; i <= pad; i++ {
		padded[pad-i] = pe[i]
		padded[pad+n-1+i] = pe[n-1-i]
	}
	// Symmetric Hann window of winLength, centred inside nFFT.
	win := make([]float64, nFFT)
	off := (nFFT - winLength) / 2
	for i := 0; i < winLength; i++ {
		win[off+i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(winLength-1)))
	}

	frames := n / hopLength // librosa gives 1 + n/hop; NeMo drops the last
	out := make([][]float32, numMels)
	for m := range out {
		out[m] = make([]float32, frames)
	}
	re := make([]float64, nFFT)
	im := make([]float64, nFFT)
	bins := nFFT/2 + 1
	power := make([]float64, bins)
	for f := 0; f < frames; f++ {
		seg := padded[f*hopLength : f*hopLength+nFFT]
		for i := 0; i < nFFT; i++ {
			re[i] = seg[i] * win[i]
			im[i] = 0
		}
		fft(re, im)
		for k := 0; k < bins; k++ {
			power[k] = re[k]*re[k] + im[k]*im[k]
		}
		for m := 0; m < numMels; m++ {
			var s float64
			for k, w := range fbank[m] {
				if w != 0 {
					s += w * power[k]
				}
			}
			out[m][f] = float32(math.Log(s + logGuard))
		}
	}
	// Per-feature normalisation (unbiased std, as NeMo).
	for m := range out {
		row := out[m]
		var mean float64
		for _, v := range row {
			mean += float64(v)
		}
		mean /= float64(len(row))
		var ss float64
		for _, v := range row {
			d := float64(v) - mean
			ss += d * d
		}
		std := 0.0
		if len(row) > 1 {
			std = math.Sqrt(ss / float64(len(row)-1))
		}
		for i, v := range row {
			row[i] = float32((float64(v) - mean) / (std + 1e-5))
		}
	}
	return out
}
