package tcasr

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// Refiner re-transcribes finished utterances with an offline model (Qwen3-ASR),
// which is markedly more accurate than the streaming model but cannot run live.
//
// It is one supervised helper process: a death is reaped and restarted, and
// while that is happening callers get core.ErrRefinerUnavailable instead of a
// dead pipe, /health reports "restarting" or "down", and /convert answers 503
// with a Retry-After. The supervision state is what makes "is the refiner
// usable?" answerable without asking it to transcribe something.
type Refiner struct {
	supervisor
	// sup is an alias for the embedded supervisor, so a caller that holds a
	// *supervisor and one that holds the Refiner see the same state.
	sup  *supervisor
	once sync.Once // guards Close
}

var (
	_ core.Refiner       = (*Refiner)(nil)
	_ core.RefinerHealth = (*Refiner)(nil)
)

// NewRefiner loads an offline GGUF model (e.g. Qwen3-ASR-1.7B-Q8_0.gguf).
// lang is a code like "tr" or "auto". The helper is started here, so a model
// that cannot be loaded is reported to whoever asked for the refiner, rather
// than surfacing on the first utterance that needs one.
func NewRefiner(model, lang string) (*Refiner, error) {
	if _, err := os.Stat(model); err != nil {
		return nil, fmt.Errorf("refiner model %s: %w", model, err)
	}
	r := new(Refiner)
	r.sup = &r.supervisor
	r.mode, r.model, r.lang, r.attRight = "offline", model, offlineLang(lang), -1
	if err := r.sup.start(); err != nil {
		return nil, err
	}
	return r, nil
}

// offlineLang: Qwen3-ASR takes plain codes ("tr"), not locale tags.
func offlineLang(lang string) string {
	lang = strings.TrimSpace(lang)
	if i := strings.IndexByte(lang, '-'); i > 0 {
		lang = lang[:i]
	}
	if strings.EqualFold(lang, "auto") {
		return ""
	}
	return strings.ToLower(lang)
}

// Refine re-transcribes a finished utterance. A helper that died is restarted
// by the supervision; the call that ran into the death waits its retry window
// and is retried once on the replacement, and if there is no replacement by
// then the answer is ErrRefinerUnavailable.
//
// There is no minimum length here on purpose: the pipeline only refines turns
// of half a second or more (pipeline guards it), and what an utterance is too
// short to be worth re-hearing is the model's call, not a sample count baked
// next to the transport.
func (r *Refiner) Refine(ctx context.Context, pcm []int16) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	rep, err := r.sup.call(ctx, 'T', pcm)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(rep.Text), nil
}

// Status is "off", "up", "restarting" or "down" for /health.
func (r *Refiner) Status() string { return r.sup.status() }

// RetryAfter is the seconds a refused /convert should tell the caller to
// wait; it is the supervision's own back-off, not a made-up number.
func (r *Refiner) RetryAfter() int { return r.sup.retryAfter() }

// Alive reports whether a healthy sidecar is up right now. It never waits on
// the helper's mutex during a long Refine (it reads the supervisor's state).
func (r *Refiner) Alive() bool { return r.sup.status() == stateUp }

func (r *Refiner) Close() error {
	var err error
	r.once.Do(func() { err = r.sup.close() })
	return err
}
