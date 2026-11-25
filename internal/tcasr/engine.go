package tcasr

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

func init() {
	core.RegisterBackend("tc", func(model string) (core.Transcriber, error) { return NewEngine(model) })
}

// Locale maps a short code ("tr") to the locale tag Nemotron expects.
func Locale(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" || strings.EqualFold(lang, "auto") {
		return "auto"
	}
	if strings.Contains(lang, "-") {
		return lang
	}
	table := map[string]string{
		"tr": "tr-TR", "en": "en-US", "de": "de-DE", "fr": "fr-FR", "es": "es-ES", "it": "it-IT",
		"pt": "pt-BR", "ru": "ru-RU", "nl": "nl-NL", "pl": "pl-PL", "uk": "uk-UA", "ja": "ja-JP",
		"ko": "ko-KR", "zh": "zh-CN", "ar": "ar-AR", "hi": "hi-IN", "cs": "cs-CZ", "sv": "sv-SE",
	}
	if l, ok := table[strings.ToLower(lang)]; ok {
		return l
	}
	return lang
}

// Engine is the "tc" backend: a Nemotron GGUF model behind the sidecar.
type Engine struct {
	model    string
	attRight int // lookahead menu entry {0,3,6,13} = 0/240/480/1040 ms

	// batch supervises the helper used for whole-file (batch) passes. It is
	// reaped and restarted when it dies, and a pass that runs into the death
	// comes back as core.ErrRefinerUnavailable rather than a dead pipe: 'R',
	// the 'A' chunks and 'F' only make sense as one run on one helper, so the
	// pass is handed back and re-run on the replacement. The pointer never
	// changes, which is what lets a reader hold on to it across a death.
	batch *supervisor
	// mu is that supervisor's own lock. Language and supervision state form a
	// single consistency domain, so a reader that takes e.mu sees exactly what
	// the supervision sees. No Engine method may take it around a supervisor
	// call - they are the same lock.
	mu   *sync.Mutex
	lang string
}

var (
	_ core.Transcriber             = (*Engine)(nil)
	_ core.LanguageLiveTranscriber = (*Engine)(nil)
)

// NewEngine checks the model and the helper. $GLEANN_ASR_ATT_RIGHT picks the
// latency setting (default 6, i.e. ~0.56 s chunks).
func NewEngine(model string) (*Engine, error) {
	if _, err := os.Stat(model); err != nil {
		return nil, fmt.Errorf("tc backend: model %s: %w", model, err)
	}
	if _, err := FindHelper(); err != nil {
		return nil, err
	}
	att := 6
	if v := os.Getenv("GLEANN_ASR_ATT_RIGHT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			att = n
		}
	}
	e := &Engine{
		model:    model,
		attRight: att,
		lang:     "auto",
		batch:    &supervisor{mode: "stream", model: model, lang: "auto", attRight: att},
	}
	e.mu = &e.batch.mu
	return e, nil
}

func (e *Engine) SetLanguage(lang string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lang = Locale(lang)
	// The batch helper picks the language up on its next start; one already
	// running keeps the language it loaded with, as it did before.
	e.batch.lang = e.lang
}

func (e *Engine) language() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lang
}

func (e *Engine) NewLiveStream() (core.LiveStream, error) {
	return e.NewLiveStreamLang(e.language())
}

// NewLiveStreamLang starts a session in its own helper process, so sessions
// are independent (and each holds a copy of the model in memory).
func (e *Engine) NewLiveStreamLang(lang string) (core.LiveStream, error) {
	h, err := startHelper("stream", e.model, Locale(lang), e.attRight)
	if err != nil {
		return nil, err
	}
	return &liveStream{h: h}, nil
}

// liveStream adapts the helper's cumulative transcript to word-safe updates.
type liveStream struct {
	h     *helper
	gate  core.WordGate
	audio time.Duration
}

func (s *liveStream) Accept(ctx context.Context, pcm []int16) (core.LiveUpdate, error) {
	if err := ctx.Err(); err != nil {
		return core.LiveUpdate{}, err
	}
	if len(pcm) == 0 {
		return core.LiveUpdate{Audio: s.audio}, nil
	}
	r, err := s.h.call('A', pcm)
	if err != nil {
		return core.LiveUpdate{}, err
	}
	s.audio += time.Duration(len(pcm)) * time.Second / 16000
	delta, pending := s.gate.Update(r.Committed, false)
	if t := strings.TrimSpace(r.Tentative); t != "" {
		pending = strings.TrimSpace(pending + " " + t)
	}
	return core.LiveUpdate{Delta: delta, Pending: pending, Audio: s.audio}, nil
}

func (s *liveStream) Flush(ctx context.Context) (core.LiveUpdate, error) {
	r, err := s.h.call('F', nil)
	if err != nil {
		return core.LiveUpdate{}, err
	}
	delta, _ := s.gate.Update(r.Text, true)
	// 'F' already restarted the utterance inside the helper.
	s.gate.Reset()
	return core.LiveUpdate{Delta: delta, Audio: s.audio}, nil
}

func (s *liveStream) Reset() {
	_, _ = s.h.call('R', nil)
	s.gate.Reset()
}

func (s *liveStream) Close() error { return s.h.close() }

// TranscribeStreamSegments transcribes a whole recording through the streaming
// path. The sidecar gives no per-word times, so the result is one segment.
func (e *Engine) TranscribeStreamSegments(ctx context.Context, pcm []int16) ([]core.Segment, error) {
	if len(pcm) == 0 {
		return nil, nil
	}
	// The pass is one unit on one helper: callNow never waits out a restart,
	// so a death inside it returns the sentinel at once and the caller re-runs
	// the whole pass on the replacement instead of getting half a transcript.
	if _, err := e.batch.callNow(ctx, 'R', nil); err != nil {
		return nil, err
	}
	const chunk = 16000 // 1 s
	for i := 0; i < len(pcm); i += chunk {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := e.batch.callNow(ctx, 'A', pcm[i:min(i+chunk, len(pcm))]); err != nil {
			return nil, err
		}
	}
	r, err := e.batch.callNow(ctx, 'F', nil)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return nil, nil
	}
	return []core.Segment{{End: time.Duration(len(pcm)) * time.Second / 16000, Text: text}}, nil
}

func (e *Engine) TranscribeStream(ctx context.Context, pcm []int16) (string, error) {
	segs, err := e.TranscribeStreamSegments(ctx, pcm)
	if err != nil || len(segs) == 0 {
		return "", err
	}
	return segs[0].Text, nil
}

func (e *Engine) TranscribeFile(ctx context.Context, path string) ([]core.Segment, error) {
	pcm, err := core.DecodeFile(ctx, path)
	if err != nil {
		return nil, err
	}
	return e.TranscribeStreamSegments(ctx, pcm)
}

// Close stops the supervision and shuts the batch helper down. It does not
// take e.mu: that is the supervisor's own lock, and stopping wants it back.
func (e *Engine) Close() error {
	return e.batch.close()
}
