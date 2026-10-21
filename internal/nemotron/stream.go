//go:build onnx
// +build onnx

package nemotron

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

const (
	preEncodeCache = 9                            // mel frames of left context per chunk
	chunkFrames    = 56                           // new mel frames per encoder call (560 ms)
	targetFrames   = preEncodeCache + chunkFrames // encoder input length
	blankToken     = int64(13087)
	maxSymbols     = 5                     // symbols per encoder frame
	encFrameDur    = 80 * time.Millisecond // one encoder frame = 8 mel frames

	// Automatic gain: the signal is scaled so its recent peak sits near full
	// scale. The floor keeps room noise from being amplified into speech.
	gainFloor    = 0.02
	gainHalfLife = 30 * time.Second
)

var _ core.LanguageLiveTranscriber = (*Engine)(nil)

// NewLiveStream starts an independent streaming session. Sessions share the
// loaded model but not their caches, so several can be open at once.
func (e *Engine) NewLiveStream() (core.LiveStream, error) {
	return e.newStream(), nil
}

// NewLiveStreamLang is NewLiveStream with an explicit language ("" = auto).
func (e *Engine) NewLiveStreamLang(lang string) (core.LiveStream, error) {
	s := e.newStream()
	s.langID = langIDFor(lang)
	return s, nil
}

func (e *Engine) langID() int64 { return langIDFor(e.lang) }

// langIDFor maps a language code ("tr", "en-US", "18", "auto") to the model's
// language prompt id; unknown codes fall back to auto-detection.
func langIDFor(lang string) int64 {
	if lang == "" {
		return 101
	}
	if id, err := strconv.ParseInt(lang, 10, 64); err == nil {
		return id
	}
	if id, ok := nemotronLangIDs[strings.ToLower(lang)]; ok {
		return id
	}
	if strings.HasPrefix(strings.ToLower(lang), "en") {
		return 0
	}
	return 101
}

type emittedToken struct {
	id    int64
	frame int // global encoder frame index
}

// stream is the cache-aware RNN-T state for one utterance: encoder caches,
// the prediction network state and the unfinished audio.
type stream struct {
	e      *Engine
	langID int64
	fe     *melFrontend

	// Encoder streaming caches.
	cacheChan    []float32
	cacheTime    []float32
	cacheChanLen []int64

	// Prediction network state.
	decH, decC   []float32
	lastToken    int64
	decOut       []float32 // prediction output for lastToken
	decOutValid  bool
	nextH, nextC []float32 // states after lastToken, adopted when a symbol is emitted

	hist    [][]float32 // last preEncodeCache frames of the previous chunk
	pending [][]float32 // frames not yet encoded
	chunks  int

	encFrames int // encoder frames produced so far
	tokens    []emittedToken
	emitted   int // bytes of the decoded text already released

	audio time.Duration

	// gain: fixed when > 0, otherwise tracked from the signal.
	fixedGain float32
	peak      float32
}

func (e *Engine) newStream() *stream {
	s := &stream{
		e:      e,
		langID: e.langID(),
		fe:     newMelFrontend(e.melFilters),
	}
	s.allocState()
	return s
}

func (s *stream) allocState() {
	s.cacheChan = make([]float32, 1*24*56*1024)
	s.cacheTime = make([]float32, 1*24*1024*8)
	s.cacheChanLen = []int64{0}
	s.decH = make([]float32, 2*1*640)
	s.decC = make([]float32, 2*1*640)
	s.decOut = make([]float32, 640)
	s.lastToken = blankToken
}

// Reset starts a fresh utterance.
func (s *stream) Reset() {
	clear(s.cacheChan)
	clear(s.cacheTime)
	s.cacheChanLen[0] = 0
	clear(s.decH)
	clear(s.decC)
	s.lastToken = blankToken
	s.decOutValid = false
	s.fe.reset()
	s.hist, s.pending = nil, nil
	s.chunks, s.encFrames, s.emitted = 0, 0, 0
	s.tokens = s.tokens[:0]
	s.peak = 0
}

func (s *stream) Close() error { return nil }

func (s *stream) Accept(ctx context.Context, pcm []int16) (core.LiveUpdate, error) {
	if len(pcm) == 0 {
		return s.update(false), nil
	}
	samples := s.scale(pcm)
	s.audio += time.Duration(len(pcm)) * time.Second / sampleRate
	s.fe.push(samples)
	if err := s.consume(ctx, false); err != nil {
		return core.LiveUpdate{}, err
	}
	return s.update(false), nil
}

func (s *stream) Flush(ctx context.Context) (core.LiveUpdate, error) {
	if err := s.consume(ctx, true); err != nil {
		return core.LiveUpdate{}, err
	}
	return s.update(true), nil
}

// scale converts PCM to float with the gain applied.
func (s *stream) scale(pcm []int16) []float32 {
	out := make([]float32, len(pcm))
	var peak float32
	for i, v := range pcm {
		x := float32(v) / 32768.0
		out[i] = x
		if a := float32(math.Abs(float64(x))); a > peak {
			peak = a
		}
	}
	g := s.fixedGain
	if g <= 0 {
		dt := time.Duration(len(pcm)) * time.Second / sampleRate
		s.peak *= float32(math.Pow(0.5, float64(dt)/float64(gainHalfLife)))
		if peak > s.peak {
			s.peak = peak
		}
		g = 1 / (max(s.peak, gainFloor) + 1e-5)
	}
	for i := range out {
		out[i] *= g
	}
	return out
}

// consume encodes every full chunk; with flush, also the final partial one.
func (s *stream) consume(ctx context.Context, flush bool) error {
	s.pending = append(s.pending, s.fe.ready(flush)...)
	for len(s.pending) >= chunkFrames {
		if err := s.runChunk(ctx, s.pending[:chunkFrames]); err != nil {
			return err
		}
		s.pending = s.pending[chunkFrames:]
	}
	if flush && len(s.pending) > 0 {
		err := s.runChunk(ctx, s.pending)
		s.pending = nil
		return err
	}
	return nil
}

func (s *stream) runChunk(ctx context.Context, frames [][]float32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Layout [targetFrames][nMels]: left context, new frames, silence padding.
	in := make([]float32, targetFrames*nMels)
	for i := range in {
		in[i] = silenceLogMel
	}
	chunkLen := preEncodeCache // padding counts as valid on the first chunk
	if len(s.hist) == preEncodeCache {
		for i, fr := range s.hist {
			copy(in[i*nMels:], fr)
		}
	}
	for i, fr := range frames {
		copy(in[(preEncodeCache+i)*nMels:], fr)
	}
	chunkLen += len(frames)

	if n := len(frames); n >= preEncodeCache {
		s.hist = append(s.hist[:0], frames[n-preEncodeCache:]...)
	} else {
		s.hist = append(s.hist, frames...)
		if len(s.hist) > preEncodeCache {
			s.hist = s.hist[len(s.hist)-preEncodeCache:]
		}
	}

	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	if s.e.encoder == nil {
		return fmt.Errorf("nemotron: engine closed")
	}

	enc, err := s.encode(in, chunkLen)
	if err != nil {
		return err
	}
	defer enc.Destroy()

	data := enc.GetData()
	shape := enc.GetShape()
	if len(shape) < 2 {
		return fmt.Errorf("nemotron: unexpected encoder output shape %v", shape)
	}
	n := int(shape[1])
	if len(data) < n*1024 {
		return fmt.Errorf("nemotron: encoder output too short (%d < %d)", len(data), n*1024)
	}
	if err := s.decode(ctx, data, n); err != nil {
		return err
	}
	s.encFrames += n
	s.chunks++
	return nil
}

// encode runs the cache-aware encoder for one chunk and rolls the caches.
func (s *stream) encode(in []float32, chunkLen int) (*ort.Tensor[float32], error) {
	audioIn, err := ort.NewTensor(ort.NewShape(1, targetFrames, nMels), in)
	if err != nil {
		return nil, fmt.Errorf("nemotron encoder input: %w", err)
	}
	defer audioIn.Destroy()
	lenIn, err := ort.NewTensor(ort.NewShape(1), []int64{int64(chunkLen)})
	if err != nil {
		return nil, err
	}
	defer lenIn.Destroy()
	chanIn, err := ort.NewTensor(ort.NewShape(1, 24, 56, 1024), s.cacheChan)
	if err != nil {
		return nil, err
	}
	defer chanIn.Destroy()
	timeIn, err := ort.NewTensor(ort.NewShape(1, 24, 1024, 8), s.cacheTime)
	if err != nil {
		return nil, err
	}
	defer timeIn.Destroy()
	chanLenIn, err := ort.NewTensor(ort.NewShape(1), s.cacheChanLen)
	if err != nil {
		return nil, err
	}
	defer chanLenIn.Destroy()
	langIn, err := ort.NewTensor(ort.NewShape(1), []int64{s.langID})
	if err != nil {
		return nil, err
	}
	defer langIn.Destroy()

	out := []ort.Value{nil, nil, nil, nil, nil}
	err = s.e.encoder.Run([]ort.Value{audioIn, lenIn, chanIn, timeIn, chanLenIn, langIn}, out)
	if err != nil {
		for _, v := range out {
			if v != nil {
				v.Destroy()
			}
		}
		return nil, fmt.Errorf("nemotron encoder run failed: %w", err)
	}
	if t, ok := out[2].(*ort.Tensor[float32]); ok {
		copy(s.cacheChan, t.GetData())
	}
	if t, ok := out[3].(*ort.Tensor[float32]); ok {
		copy(s.cacheTime, t.GetData())
	}
	if t, ok := out[4].(*ort.Tensor[int64]); ok {
		copy(s.cacheChanLen, t.GetData())
	}
	for _, i := range []int{1, 2, 3, 4} {
		if out[i] != nil {
			out[i].Destroy()
		}
	}
	t, ok := out[0].(*ort.Tensor[float32])
	if !ok {
		if out[0] != nil {
			out[0].Destroy()
		}
		return nil, fmt.Errorf("nemotron: unexpected encoder output tensor type")
	}
	return t, nil
}

// decode runs RNN-T greedy search over n encoder frames. The prediction
// network only runs when the previous symbol changed; after a blank its
// output is reused for the next frame.
func (s *stream) decode(ctx context.Context, enc []float32, n int) error {
	hShape, cShape := ort.NewShape(2, 1, 640), ort.NewShape(2, 1, 640)

	for t := 0; t < n; t++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame, err := ort.NewTensor(ort.NewShape(1, 1, 1024), enc[t*1024:(t+1)*1024])
		if err != nil {
			return fmt.Errorf("joint encoder tensor: %w", err)
		}
		for sym := 0; sym < maxSymbols; sym++ {
			if !s.decOutValid {
				if err := s.predict(hShape, cShape); err != nil {
					frame.Destroy()
					return err
				}
			}
			y, err := s.joint(frame)
			if err != nil {
				frame.Destroy()
				return err
			}
			if y == blankToken {
				break
			}
			s.tokens = append(s.tokens, emittedToken{id: y, frame: s.encFrames + t})
			s.lastToken = y
			copy(s.decH, s.nextH)
			copy(s.decC, s.nextC)
			s.decOutValid = false
		}
		frame.Destroy()
	}
	return nil
}

// predict runs the prediction network for lastToken and caches its output;
// the new states are kept aside and become current only if a symbol is emitted.
func (s *stream) predict(hShape, cShape ort.Shape) error {
	tgt, err := ort.NewTensor(ort.NewShape(1, 1), []int64{s.lastToken})
	if err != nil {
		return err
	}
	defer tgt.Destroy()
	hIn, err := ort.NewTensor(hShape, s.decH)
	if err != nil {
		return err
	}
	defer hIn.Destroy()
	cIn, err := ort.NewTensor(cShape, s.decC)
	if err != nil {
		return err
	}
	defer cIn.Destroy()

	out := []ort.Value{nil, nil, nil}
	if err := s.e.decoder.Run([]ort.Value{tgt, hIn, cIn}, out); err != nil {
		for _, v := range out {
			if v != nil {
				v.Destroy()
			}
		}
		return fmt.Errorf("nemotron decoder failed: %w", err)
	}
	defer func() {
		for _, v := range out {
			v.Destroy()
		}
	}()
	copy(s.decOut, out[0].(*ort.Tensor[float32]).GetData())
	s.decOutValid = true
	s.nextH = append(s.nextH[:0], out[1].(*ort.Tensor[float32]).GetData()...)
	s.nextC = append(s.nextC[:0], out[2].(*ort.Tensor[float32]).GetData()...)
	return nil
}

func (s *stream) joint(frame *ort.Tensor[float32]) (int64, error) {
	dec, err := ort.NewTensor(ort.NewShape(1, 1, 640), s.decOut)
	if err != nil {
		return 0, err
	}
	defer dec.Destroy()
	out := []ort.Value{nil}
	if err := s.e.joint.Run([]ort.Value{frame, dec}, out); err != nil {
		return 0, fmt.Errorf("nemotron joint failed: %w", err)
	}
	defer out[0].Destroy()
	logits := out[0].(*ort.Tensor[float32]).GetData()
	best := 0
	for i := 1; i < len(logits); i++ {
		if logits[i] > logits[best] {
			best = i
		}
	}
	return int64(best), nil
}

// text decodes tokens with SentencePiece semantics: "▁" opens a new word,
// other pieces continue the current one; <special> tokens are dropped.
func (e *Engine) text(tokens []emittedToken) string {
	var sb strings.Builder
	for _, t := range tokens {
		if t.id < 0 || t.id >= int64(len(e.vocab)) {
			continue
		}
		tok := e.vocab[t.id]
		if strings.HasPrefix(tok, "<") && strings.HasSuffix(tok, ">") {
			continue
		}
		if rest, ok := strings.CutPrefix(tok, "▁"); ok {
			if sb.Len() > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(rest)
		} else {
			sb.WriteString(tok)
		}
	}
	return strings.TrimSpace(sb.String())
}

// update releases newly finalised text. Until flush, the last word is held
// back because the next token may still extend it.
func (s *stream) update(final bool) core.LiveUpdate {
	full := s.e.text(s.tokens)
	stable := full
	if !final {
		if i := strings.LastIndexByte(full, ' '); i >= 0 {
			stable = full[:i]
		} else {
			stable = ""
		}
	}
	u := core.LiveUpdate{Audio: s.audio}
	if len(stable) > s.emitted {
		u.Delta = stable[s.emitted:]
		if s.emitted > 0 && !strings.HasPrefix(u.Delta, " ") {
			u.Delta = " " + u.Delta
		}
		s.emitted = len(stable)
	}
	if !final && len(full) > len(stable) {
		u.Pending = strings.TrimSpace(full[len(stable):])
	}
	return u
}

// segments splits the utterance at pauses of at least gap.
func (s *stream) segments(gap time.Duration) []core.Segment {
	var segs []core.Segment
	var cur []emittedToken
	flush := func() {
		if len(cur) == 0 {
			return
		}
		text := s.e.text(cur)
		if text != "" {
			segs = append(segs, core.Segment{
				Start: time.Duration(cur[0].frame) * encFrameDur,
				End:   time.Duration(cur[len(cur)-1].frame+1) * encFrameDur,
				Text:  text,
			})
		}
		cur = nil
	}
	for _, t := range s.tokens {
		if len(cur) > 0 && time.Duration(t.frame-cur[len(cur)-1].frame)*encFrameDur >= gap {
			flush()
		}
		cur = append(cur, t)
	}
	flush()
	return segs
}
