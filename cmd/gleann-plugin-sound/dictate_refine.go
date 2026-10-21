package main

import (
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
)

// refinedTyper types the refiner's transcript of each utterance instead of the
// live words. The live model is fast but makes many mistakes on conversational
// speech; the refiner is several times more accurate, so dictation waits for it
// (about 0.4 s of compute per second of speech after each pause) and types only
// text that does not need correcting later.
//
// An utterance the refiner did not return (queue full, refiner failed) falls
// back to its live transcript when the session ends, so words are never lost.
type refinedTyper struct {
	inject func(string) error

	mu      sync.Mutex
	pending map[int]string // live text of utterances not refined yet
	typed   bool
}

func newRefinedTyper(inject func(string) error) *refinedTyper {
	return &refinedTyper{inject: inject, pending: map[int]string{}}
}

// Handle consumes one pipeline event; it is the OnLive callback.
func (r *refinedTyper) Handle(ev pipeline.LiveEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case ev.Refined:
		delete(r.pending, ev.Seq)
		r.typeLocked(ev.Text)
	case ev.Final:
		r.pending[ev.Seq] = ev.Text
	}
}

// Flush types the live transcript of every utterance that never got a refined
// one. Call it once the pipeline has returned.
func (r *refinedTyper) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	seqs := make([]int, 0, len(r.pending))
	for s := range r.pending {
		seqs = append(seqs, s)
	}
	sort.Ints(seqs)
	for _, s := range seqs {
		r.typeLocked(r.pending[s])
	}
	r.pending = map[int]string{}
}

func (r *refinedTyper) typeLocked(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if r.typed {
		text = " " + text
	}
	if err := r.inject(text); err != nil {
		log.Printf("[dictate] injection error: %v", err)
		return
	}
	r.typed = true
}

// reset forgets the previous key press so the next dictation starts without a
// leading space.
func (r *refinedTyper) reset() {
	r.mu.Lock()
	r.typed = false
	r.pending = map[int]string{}
	r.mu.Unlock()
}

// didType reports whether any text was typed since the last reset.
func (r *refinedTyper) didType() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.typed
}
