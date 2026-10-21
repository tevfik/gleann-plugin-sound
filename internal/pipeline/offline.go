package pipeline

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// FileConfig tunes TranscribeFile.
type FileConfig struct {
	// EndpointSilence merges speech separated by shorter pauses. Default 0.7 s.
	EndpointSilence time.Duration
	// MaxSegment splits longer stretches at the quietest nearby moment; very
	// long inputs hurt accuracy and latency of the refiner. Default 25 s.
	MaxSegment time.Duration
	// Pad is silence kept around each stretch so edges are not clipped. Default 0.25 s.
	Pad time.Duration
	// Corrector is applied to every segment's text.
	Corrector *core.Corrector
	// Speakers, if set, labels each stretch (and splits it at speaker changes
	// when it implements core.TurnSplitter).
	Speakers core.SpeakerLabeler
	// Progress, if set, is told how much audio has been transcribed so far.
	Progress func(done, total time.Duration)
}

type span struct{ start, end int }

// TranscribeFile transcribes a whole recording offline: the VAD finds the
// speech, each stretch is split at speaker changes, and the (accurate but slow)
// refiner transcribes every piece. It does not need a streaming model, which
// makes it both faster and more accurate than replaying the file through the
// live path.
func TranscribeFile(ctx context.Context, pcm []int16, vad core.VADProvider, ref core.Refiner, cfg FileConfig) ([]core.Segment, error) {
	if ref == nil {
		return nil, fmt.Errorf("TranscribeFile needs a refiner")
	}
	if cfg.EndpointSilence <= 0 {
		cfg.EndpointSilence = 700 * time.Millisecond
	}
	if cfg.MaxSegment <= 0 {
		cfg.MaxSegment = 25 * time.Second
	}
	if cfg.Pad <= 0 {
		cfg.Pad = 250 * time.Millisecond
	}
	samples := func(d time.Duration) int { return int(d * SampleRate / time.Second) }
	dur := func(n int) time.Duration { return time.Duration(n) * time.Second / SampleRate }

	spans := detectSpans(pcm, vad, samples(cfg.EndpointSilence), samples(cfg.Pad), samples(cfg.MaxSegment))
	total := dur(len(pcm))
	var out []core.Segment
	for _, sp := range spans {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		piece := pcm[sp.start:sp.end]
		turns := []core.SpeakerTurn{{End: dur(len(piece))}}
		if cfg.Speakers != nil {
			name, score := cfg.Speakers.Label(piece)
			turns[0].Name, turns[0].Score = name, score
			if ts, ok := cfg.Speakers.(core.TurnSplitter); ok {
				if t := ts.Turns(piece); len(t) > 1 {
					turns = t
				}
			}
		}
		for _, tn := range turns {
			a, b := samples(tn.Start), min(samples(tn.End), len(piece))
			if b-a < SampleRate/2 {
				continue
			}
			text, err := ref.Refine(ctx, piece[a:b])
			if err != nil {
				return out, fmt.Errorf("refine %v: %w", dur(sp.start+a), err)
			}
			text = strings.TrimSpace(cfg.Corrector.Apply(text))
			if text == "" {
				continue
			}
			out = append(out, core.Segment{
				Start: dur(sp.start + a), End: dur(sp.start + b), Text: text, Speaker: tn.Name,
			})
		}
		if cfg.Progress != nil {
			cfg.Progress(dur(sp.end), total)
		}
	}
	return out, nil
}

// detectSpans finds stretches of speech: pauses shorter than gap are bridged,
// pad is added on both sides and stretches longer than maxLen are cut at the
// quietest moment near the limit.
func detectSpans(pcm []int16, vad core.VADProvider, gap, pad, maxLen int) []span {
	const chunk = 480 // 30 ms, like the capture callback
	vad.Reset()
	var raw []span
	cur := span{-1, 0}
	lastSpeech := -1
	for i := 0; i+chunk <= len(pcm); i += chunk {
		if vad.IsSpeech(pcm[i : i+chunk]) {
			if cur.start < 0 {
				cur.start = i
			}
			lastSpeech = i + chunk
		} else if cur.start >= 0 && i-lastSpeech >= gap {
			cur.end = lastSpeech
			raw = append(raw, cur)
			cur = span{-1, 0}
		}
	}
	if cur.start >= 0 {
		cur.end = lastSpeech
		raw = append(raw, cur)
	}

	var out []span
	for _, s := range raw {
		s.start = max(0, s.start-pad)
		s.end = min(len(pcm), s.end+pad)
		if n := len(out); n > 0 && s.start <= out[n-1].end { // padding made them touch
			out[n-1].end = s.end
			continue
		}
		out = append(out, s)
	}
	// Cut long stretches.
	var final []span
	for _, s := range out {
		for s.end-s.start > maxLen {
			cut := quietest(pcm, s.start+maxLen-6*SampleRate, s.start+maxLen)
			final = append(final, span{s.start, cut})
			s.start = cut
		}
		if s.end-s.start >= SampleRate/4 {
			final = append(final, s)
		}
	}
	return final
}

// quietest returns the centre of the lowest-energy 20 ms frame in [lo, hi).
func quietest(pcm []int16, lo, hi int) int {
	const frame = SampleRate / 50
	lo = max(0, lo)
	hi = min(len(pcm)-frame, hi)
	best, bestE := hi, math.MaxFloat64
	for s := lo; s <= hi; s += frame / 2 {
		var e float64
		for _, v := range pcm[s : s+frame] {
			e += float64(v) * float64(v)
		}
		if e < bestE {
			bestE, best = e, s+frame/2
		}
	}
	return best
}
