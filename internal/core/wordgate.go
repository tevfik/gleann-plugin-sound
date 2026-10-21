package core

import "strings"

// WordGate turns a growing, append-only transcript into word-safe updates: a
// word is released only once the next one has begun (or the utterance has
// ended), because the model may still extend it ("sek" → "sekiz").
type WordGate struct {
	emitted int // bytes of the transcript already released
}

// Reset starts a new utterance.
func (g *WordGate) Reset() { g.emitted = 0 }

// Update takes the full append-only text so far. It returns the newly
// released text (with a leading space when it continues the utterance) and the
// unfinished tail, which is only meant for previews.
func (g *WordGate) Update(full string, final bool) (delta, pending string) {
	full = strings.TrimSpace(full)
	stable := full
	if !final {
		if i := strings.LastIndexByte(full, ' '); i >= 0 {
			stable = full[:i]
		} else {
			stable = ""
		}
	}
	if len(stable) > g.emitted {
		delta = stable[g.emitted:]
		if g.emitted > 0 && !strings.HasPrefix(delta, " ") {
			delta = " " + delta
		}
		g.emitted = len(stable)
	}
	if !final && len(full) > len(stable) {
		pending = strings.TrimSpace(full[len(stable):])
	}
	return delta, pending
}
