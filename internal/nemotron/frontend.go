//go:build onnx
// +build onnx

package nemotron

import "math"

// silenceLogMel is the log-mel value of digital silence; it pads chunks.
const silenceLogMel = -23.02585

// melFrontend computes log-mel frames incrementally. It produces exactly the
// frames a one-shot computation over the whole signal would (preemphasis,
// centred STFT with nFFT/2 zero padding on both sides), but a frame is emitted
// as soon as the audio it needs has arrived, so a live stream only waits for
// the 144-sample look-ahead of the Hann window.
type melFrontend struct {
	filters []float32
	window  []float32

	buf   []float32 // preemphasised signal; buf[0] is signal index base
	base  int
	prev  float32 // last raw sample, for preemphasis
	total int     // samples pushed so far
	next  int     // index of the next frame to emit

	fftRe, fftIm []float64
	mag          []float32
}

func newMelFrontend(filters []float32) *melFrontend {
	return &melFrontend{
		filters: filters,
		window:  buildHannWindow(windowLength),
		fftRe:   make([]float64, fftPaddedSize),
		fftIm:   make([]float64, fftPaddedSize),
		mag:     make([]float32, nFFT/2+1),
	}
}

func (f *melFrontend) reset() {
	f.buf = f.buf[:0]
	f.base, f.total, f.next = 0, 0, 0
	f.prev = 0
}

// push appends raw samples (already gain-adjusted).
func (f *melFrontend) push(samples []float32) {
	for _, s := range samples {
		if f.total == 0 {
			f.buf = append(f.buf, s)
		} else {
			f.buf = append(f.buf, s-0.97*f.prev)
		}
		f.prev = s
		f.total++
	}
}

// ready emits every frame whose input is complete. With flush set, the signal
// is treated as ended: the right padding is zeros and the remaining frames are
// emitted.
func (f *melFrontend) ready(flush bool) [][]float32 {
	var limit int // frames [next, limit) can be computed
	if flush {
		total := f.total
		if total < nFFT {
			// A one-shot computation zero-pads very short input to nFFT.
			f.push(make([]float32, nFFT-total))
			total = nFFT
		}
		limit = (total+nFFT-windowLength)/hopLength + 1
	} else {
		// Frame i needs signal samples up to i*hop - nFFT/2 + windowLength.
		if n := f.total + nFFT/2 - windowLength; n >= 0 {
			limit = n/hopLength + 1
		}
	}
	var out [][]float32
	for ; f.next < limit; f.next++ {
		out = append(out, f.frame(f.next))
	}
	f.trim()
	return out
}

func (f *melFrontend) frame(idx int) []float32 {
	start := idx*hopLength - nFFT/2 // signal coordinates
	var data [windowLength]float32
	for i := 0; i < windowLength; i++ {
		p := start + i - f.base
		if start+i >= 0 && p >= 0 && p < len(f.buf) {
			data[i] = f.buf[p]
		}
	}
	fftMagnitudeSquared(data[:], f.window, f.fftRe, f.fftIm, f.mag)

	bins := nFFT/2 + 1
	mel := make([]float32, nMels)
	for m := 0; m < nMels; m++ {
		var sum float32
		row := f.filters[m*bins : (m+1)*bins]
		for k, w := range row {
			sum += w * f.mag[k]
		}
		mel[m] = float32(math.Log(float64(sum) + 1e-10))
	}
	return mel
}

// trim drops samples no future frame can reference.
func (f *melFrontend) trim() {
	keepFrom := f.next*hopLength - nFFT/2
	if drop := keepFrom - f.base; drop > 0 && drop <= len(f.buf) {
		f.buf = append(f.buf[:0], f.buf[drop:]...)
		f.base = keepFrom
	}
}
