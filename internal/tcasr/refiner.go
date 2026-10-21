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
type Refiner struct {
	h *helper
	// serialised by helper.mu; closed guards Close.
	once sync.Once
}

var _ core.Refiner = (*Refiner)(nil)

// NewRefiner loads an offline GGUF model (e.g. Qwen3-ASR-1.7B-Q8_0.gguf).
// lang is a code like "tr" or "auto".
func NewRefiner(model, lang string) (*Refiner, error) {
	if _, err := os.Stat(model); err != nil {
		return nil, fmt.Errorf("refiner model %s: %w", model, err)
	}
	h, err := startHelper("offline", model, offlineLang(lang), -1)
	if err != nil {
		return nil, err
	}
	return &Refiner{h: h}, nil
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

func (r *Refiner) Refine(ctx context.Context, pcm []int16) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(pcm) < 1600 { // < 0.1 s
		return "", nil
	}
	rep, err := r.h.call('T', pcm)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(rep.Text), nil
}

func (r *Refiner) Close() error {
	var err error
	r.once.Do(func() { err = r.h.close() })
	return err
}
