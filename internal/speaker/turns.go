package speaker

import (
	"math"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// Window length matters more than step: measured on a labelled mix with a TV
// at 10 dB, 2 s windows identified 63 % of speech correctly (36 % unknown),
// 4 s windows 81 % (16 % unknown, 2 % wrong); longer windows blur the boundaries.
const (
	turnWindow   = 4 * sampleRate      // embedding window
	turnHop      = sampleRate / 2      // 0.5 s step
	minTurn      = 3 * sampleRate / 2  // shorter runs are absorbed by a neighbour
	snapRadius   = sampleRate * 4 / 10 // look ±0.4 s for a pause to cut at
	minSplitable = 5 * sampleRate      // do not bother with shorter utterances
)

var _ core.TurnSplitter = (*Labeler)(nil)

// Turns finds where the speaker changes inside one utterance. It only works
// with enrolled voices (closed-set labels are stable; clustering strangers
// window by window is not) and returns nil when the utterance is short, has a
// single speaker, or no voices are enrolled.
func (l *Labeler) Turns(pcm []int16) []core.SpeakerTurn {
	if l.Reg == nil || l.Reg.Len() == 0 || len(pcm) < minSplitable {
		return nil
	}
	th := l.Threshold
	if th <= 0 {
		th = DefaultThreshold
	}

	// 1. Label every 4 s window (step 0.5 s); the label belongs to its centre.
	type lab struct {
		centre int
		name   string
		score  float64
	}
	var labs []lab
	for off := 0; off+turnWindow <= len(pcm); off += turnHop {
		v, err := l.Emb.Embed(pcm[off : off+turnWindow])
		if err != nil {
			return nil
		}
		name, score := l.Reg.Match(v)
		if score < th {
			name = UnknownName
		}
		labs = append(labs, lab{off + turnWindow/2, name, score})
	}
	if len(labs) < 3 {
		return nil
	}

	// 2. Majority filter over three neighbours removes single-window flips.
	sm := make([]string, len(labs))
	for i := range labs {
		cnt := map[string]int{}
		for j := max(0, i-1); j <= min(len(labs)-1, i+1); j++ {
			cnt[labs[j].name]++
		}
		best, bc := labs[i].name, 0
		for n, c := range cnt {
			if c > bc || (c == bc && n == labs[i].name) {
				best, bc = n, c
			}
		}
		sm[i] = best
	}

	// 3. Runs of equal labels, boundaries halfway between window centres.
	type run struct {
		name       string
		start, end int
		score      float64
		n          int
	}
	var runs []run
	for i, name := range sm {
		if len(runs) > 0 && runs[len(runs)-1].name == name {
			r := &runs[len(runs)-1]
			r.score += labs[i].score
			r.n++
			continue
		}
		start := 0
		if i > 0 {
			start = (labs[i-1].centre + labs[i].centre) / 2
		}
		if len(runs) > 0 {
			runs[len(runs)-1].end = start
		}
		runs = append(runs, run{name: name, start: start, score: labs[i].score, n: 1})
	}
	runs[len(runs)-1].end = len(pcm)

	// 4. Absorb short runs into the neighbour they resemble most (the longer one).
	for changed := true; changed && len(runs) > 1; {
		changed = false
		for i := range runs {
			if runs[i].end-runs[i].start >= minTurn {
				continue
			}
			j := i - 1
			if j < 0 || (i+1 < len(runs) && runs[i+1].end-runs[i+1].start > runs[j].end-runs[j].start) {
				j = i + 1
			}
			a, b := min(i, j), max(i, j)
			merged := run{name: runs[j].name, start: runs[a].start, end: runs[b].end,
				score: runs[j].score, n: runs[j].n}
			runs = append(runs[:a], append([]run{merged}, runs[b+1:]...)...)
			changed = true
			break
		}
	}
	// Equal neighbours created by merging collapse into one.
	for i := 1; i < len(runs); {
		if runs[i].name == runs[i-1].name {
			runs[i-1].end = runs[i].end
			runs[i-1].score += runs[i].score
			runs[i-1].n += runs[i].n
			runs = append(runs[:i], runs[i+1:]...)
			continue
		}
		i++
	}
	if len(runs) < 2 {
		return nil
	}

	// 5. Cut at the quietest moment near each boundary so words stay whole.
	for i := 1; i < len(runs); i++ {
		b := snapToPause(pcm, runs[i].start)
		runs[i].start, runs[i-1].end = b, b
	}

	turns := make([]core.SpeakerTurn, len(runs))
	for i, r := range runs {
		turns[i] = core.SpeakerTurn{
			Start: time.Duration(r.start) * time.Second / sampleRate,
			End:   time.Duration(r.end) * time.Second / sampleRate,
			Name:  r.name,
			Score: r.score / float64(max(r.n, 1)),
		}
	}
	return turns
}

// snapToPause returns the sample near pos (±snapRadius) with the lowest
// short-term energy.
func snapToPause(pcm []int16, pos int) int {
	const frame = sampleRate / 50 // 20 ms
	best, bestE := pos, math.MaxFloat64
	lo, hi := max(0, pos-snapRadius), min(len(pcm)-frame, pos+snapRadius)
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
