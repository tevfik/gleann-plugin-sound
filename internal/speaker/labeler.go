package speaker

import (
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
)

// Embed produces a unit-length voice embedding; *Embedder implements it.
type Embed interface {
	Embed(pcm []int16) ([]float32, error)
}

// DefaultThreshold is the cosine a voice must reach to count as enrolled.
// Enrolled speakers scored a median 0.67 in clean audio and 0.47 under a TV at
// 10 dB; strangers and TV speech stayed below ~0.34 (95th percentile).
const DefaultThreshold = 0.35

// UnknownName labels voices that match nobody enrolled.
const UnknownName = "?"

// Labeler names the speaker of each finished utterance.
//
// With enrolled voices it does closed-set identification with rejection: a
// stranger (or the TV) stays UnknownName. With none enrolled it falls back to
// numbering voices as they appear ("Speaker 1", "Speaker 2"), which works for
// a quiet room but degrades quickly with background speech.
type Labeler struct {
	Emb       Embed
	Reg       *Registry
	Threshold float64
	// ClusterThreshold merges unenrolled voices in fallback mode (default 0.5).
	ClusterThreshold float64
	// Prefix names fallback speakers (default "Speaker").
	Prefix string

	mu      sync.Mutex
	centres [][]float64
	counts  []int
}

// Label implements core.SpeakerLabeler.
func (l *Labeler) Label(pcm []int16) (string, float64) {
	if len(pcm) < 2*sampleRate { // under ~2 s a voice print is a guess
		return "", 0
	}
	v, err := l.Emb.Embed(pcm)
	if err != nil {
		log.Printf("[speaker] %v", err)
		return "", 0
	}
	th := l.Threshold
	if th <= 0 {
		th = DefaultThreshold
	}
	if l.Reg != nil && l.Reg.Len() > 0 {
		name, score := l.Reg.Match(v)
		if score >= th {
			return name, score
		}
		return UnknownName, score
	}
	return l.cluster(v)
}

func (l *Labeler) cluster(v []float32) (string, float64) {
	ct := l.ClusterThreshold
	if ct <= 0 {
		ct = 0.5
	}
	prefix := l.Prefix
	if prefix == "" {
		prefix = "Speaker"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	best, bestScore := -1, -2.0
	for i, c := range l.centres {
		var dot, n float64
		for j := range v {
			dot += float64(v[j]) * c[j]
			n += c[j] * c[j]
		}
		if n > 0 {
			dot /= math.Sqrt(n)
		}
		if dot > bestScore {
			best, bestScore = i, dot
		}
	}
	if best >= 0 && bestScore >= ct {
		for j := range v {
			l.centres[best][j] += float64(v[j])
		}
		l.counts[best]++
		return fmt.Sprintf("%s %d", prefix, best+1), bestScore
	}
	c := make([]float64, len(v))
	for j := range v {
		c[j] = float64(v[j])
	}
	l.centres = append(l.centres, c)
	l.counts = append(l.counts, 1)
	return fmt.Sprintf("%s %d", prefix, len(l.centres)), 1
}

// Describe summarises the registry for logs.
func (r *Registry) Describe() string {
	var names []string
	for _, p := range r.Profiles {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}
