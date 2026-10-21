//go:build onnx
// +build onnx

package nemotron

import (
	"context"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// modelDir locates the Nemotron model: $NEMOTRON_MODEL_DIR, else the default
// download location. Model-backed tests are skipped without it (and with -short).
func modelDir(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("model-backed test skipped with -short")
	}
	dir := os.Getenv("NEMOTRON_MODEL_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".gleann", "models", "nemotron-3.5-asr-int4-onnx")
	}
	if _, err := os.Stat(filepath.Join(dir, "encoder.onnx")); err != nil {
		t.Skipf("Nemotron model not found in %s (set NEMOTRON_MODEL_DIR)", dir)
	}
	return dir
}

func newTestEngine(t *testing.T, lang string) *Engine {
	t.Helper()
	SetExecutionProvider(ProviderCPU)
	e, err := NewEngine(modelDir(t))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	e.SetLanguage(lang)
	t.Cleanup(func() { e.Close() })
	return e
}

func loadWav(t *testing.T, name string) []int16 {
	t.Helper()
	pcm, err := DecodeFile(context.Background(), filepath.Join("..", "..", "test", "testdata", name))
	if err != nil {
		t.Skipf("cannot decode %s: %v", name, err)
	}
	return pcm
}

// referenceMel is the one-shot log-mel computation the streaming front-end
// must reproduce exactly.
func referenceMel(filters []float32, samples []float32) []float32 {
	if len(samples) < nFFT {
		padded := make([]float32, nFFT)
		copy(padded, samples)
		samples = padded
	}
	pre := make([]float32, len(samples))
	pre[0] = samples[0]
	for i := 1; i < len(samples); i++ {
		pre[i] = samples[i] - 0.97*samples[i-1]
	}
	padded := make([]float32, len(pre)+nFFT)
	copy(padded[nFFT/2:], pre)

	n := (len(padded)-windowLength)/hopLength + 1
	bins := nFFT/2 + 1
	win := buildHannWindow(windowLength)
	re, im := make([]float64, fftPaddedSize), make([]float64, fftPaddedSize)
	mag := make([]float32, bins)
	out := make([]float32, 0, n*nMels)
	for f := 0; f < n; f++ {
		fftMagnitudeSquared(padded[f*hopLength:f*hopLength+windowLength], win, re, im, mag)
		for m := 0; m < nMels; m++ {
			var sum float32
			for k := 0; k < bins; k++ {
				sum += filters[m*bins+k] * mag[k]
			}
			out = append(out, float32(math.Log(float64(sum)+1e-10)))
		}
	}
	return out
}

func TestMelFrontend_MatchesOneShot(t *testing.T) {
	filters := buildMelFilterbank(sampleRate, nFFT, nMels)
	rng := rand.New(rand.NewSource(1))
	for _, n := range []int{1, 100, 143, 144, 511, 512, 1000, 16000, 40001} {
		samples := make([]float32, n)
		for i := range samples {
			samples[i] = float32(rng.NormFloat64() * 0.1)
		}
		want := referenceMel(filters, samples)

		fe := newMelFrontend(filters)
		var got []float32
		collect := func(frames [][]float32) {
			for _, fr := range frames {
				got = append(got, fr...)
			}
		}
		for i := 0; i < n; {
			step := 1 + rng.Intn(700) // arbitrary capture buffer sizes
			if i+step > n {
				step = n - i
			}
			fe.push(samples[i : i+step])
			collect(fe.ready(false))
			i += step
		}
		collect(fe.ready(true))

		if len(got) != len(want) {
			t.Fatalf("n=%d: got %d values, want %d", n, len(got), len(want))
		}
		for i := range want {
			if d := math.Abs(float64(got[i] - want[i])); d > 1e-4 {
				t.Fatalf("n=%d: value %d differs by %g (%g vs %g)", n, i, d, got[i], want[i])
			}
		}
	}
}

func TestMelFrontend_BoundedMemory(t *testing.T) {
	fe := newMelFrontend(buildMelFilterbank(sampleRate, nFFT, nMels))
	chunk := make([]float32, 480)
	for i := 0; i < 2000; i++ { // ~60 s
		fe.push(chunk)
		fe.ready(false)
	}
	if len(fe.buf) > 2*nFFT {
		t.Fatalf("front-end keeps %d samples; trimming is broken", len(fe.buf))
	}
}

func norm(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.NewReplacer(".", "", ",", "").Replace(s)), " "))
}

func TestEngine_TranscribeJFK(t *testing.T) {
	e := newTestEngine(t, "en")
	segs, err := e.TranscribeFile(context.Background(), filepath.Join("..", "..", "test", "testdata", "jfk.wav"))
	if err != nil || len(segs) == 0 {
		t.Fatalf("TranscribeFile: %v (%d segments)", err, len(segs))
	}
	var parts []string
	for _, s := range segs {
		parts = append(parts, s.Text)
	}
	got := norm(strings.Join(parts, " "))
	t.Logf("segments=%d text=%q", len(segs), got)
	if !strings.Contains(got, "ask not what your country can do for you") {
		t.Errorf("unexpected transcript %q", got)
	}
	last := segs[len(segs)-1]
	if last.End <= last.Start || last.End > 12*time.Second {
		t.Errorf("implausible timestamps: %v-%v", last.Start, last.End)
	}
}

func TestEngine_TranscribeTurkish(t *testing.T) {
	e := newTestEngine(t, "tr")
	segs, err := e.TranscribeFile(context.Background(), filepath.Join("..", "..", "test", "testdata", "tr_test.wav"))
	if err != nil || len(segs) == 0 {
		t.Fatalf("TranscribeFile: %v (%d segments)", err, len(segs))
	}
	got := norm(segs[0].Text)
	t.Logf("text=%q", got)
	// Raw model output ("mergı dılan sesli klavye eklentisi ..."): only the
	// words it reliably gets right are asserted; names go through corrections.
	if !strings.Contains(got, "sesli klavye eklentisi") {
		t.Errorf("unexpected Turkish transcript %q", got)
	}
}

func TestLiveStream_MatchesBatchAndIsRealTime(t *testing.T) {
	e := newTestEngine(t, "en")
	pcm := loadWav(t, "jfk.wav")

	batch, err := e.TranscribeStream(context.Background(), pcm)
	if err != nil {
		t.Fatal(err)
	}

	st, err := e.NewLiveStream()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const bufSamples = 480 // 30 ms, like the capture callback
	var text strings.Builder
	var firstWordAt time.Duration
	var busy time.Duration
	ctx := context.Background()
	for i := 0; i < len(pcm); i += bufSamples {
		end := min(i+bufSamples, len(pcm))
		t0 := time.Now()
		u, err := st.Accept(ctx, pcm[i:end])
		busy += time.Since(t0)
		if err != nil {
			t.Fatal(err)
		}
		if u.Delta != "" && firstWordAt == 0 {
			firstWordAt = u.Audio
		}
		text.WriteString(u.Delta)
	}
	u, err := st.Flush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	text.WriteString(u.Delta)

	live := norm(text.String())
	t.Logf("live  : %q", live)
	t.Logf("batch : %q", norm(batch))
	if live != norm(batch) {
		t.Errorf("live transcript differs from batch")
	}

	dur := time.Duration(len(pcm)) * time.Second / sampleRate
	rtf := float64(busy) / float64(dur)
	t.Logf("first word released after %v of audio; RTF %.2f", firstWordAt, rtf)
	// Speech starts ~0.2 s in; a chunk is 0.56 s and one word is held back.
	if firstWordAt == 0 || firstWordAt > 3*time.Second {
		t.Errorf("first word came after %v of audio; streaming is not incremental", firstWordAt)
	}
	if rtf > 0.8 {
		t.Errorf("RTF %.2f is too slow for live use", rtf)
	}
}

func TestLiveStream_ResetStartsNewUtterance(t *testing.T) {
	e := newTestEngine(t, "en")
	pcm := loadWav(t, "jfk.wav")
	st, _ := e.NewLiveStream()
	defer st.Close()
	ctx := context.Background()

	run := func() string {
		var b strings.Builder
		u, err := st.Accept(ctx, pcm)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(u.Delta)
		u, _ = st.Flush(ctx)
		b.WriteString(u.Delta)
		return norm(b.String())
	}
	first := run()
	st.Reset()
	second := run()
	if first == "" || first != second {
		t.Errorf("reset did not isolate utterances:\n 1: %q\n 2: %q", first, second)
	}
}

func TestLiveStream_SilenceYieldsNothing(t *testing.T) {
	e := newTestEngine(t, "en")
	st, _ := e.NewLiveStream()
	defer st.Close()
	ctx := context.Background()
	quiet := make([]int16, 16000*3)
	rng := rand.New(rand.NewSource(2))
	for i := range quiet {
		quiet[i] = int16(rng.Intn(21) - 10) // faint noise
	}
	u, err := st.Accept(ctx, quiet)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := st.Flush(ctx)
	if got := strings.TrimSpace(u.Delta + f.Delta); got != "" {
		t.Errorf("faint noise produced text %q", got)
	}
}
