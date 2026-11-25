package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// fakeStream "recognises" one word per 0.5 s of audio it is given.
type fakeStream struct {
	fed, ever, resets, flushes int
	words                      int
	held                       bool // a word is held back until the next one
}

func (f *fakeStream) Accept(_ context.Context, pcm []int16) (core.LiveUpdate, error) {
	before := f.fed / 8000
	f.ever += len(pcm)
	f.fed += len(pcm)
	var u core.LiveUpdate
	for w := before; w < f.fed/8000; w++ {
		f.words++
		if f.held {
			u.Delta += fmt.Sprintf(" w%d", f.words-1)
		}
		f.held = true
	}
	return u, nil
}

func (f *fakeStream) Flush(context.Context) (core.LiveUpdate, error) {
	f.flushes++
	if f.held {
		f.held = false
		return core.LiveUpdate{Delta: fmt.Sprintf(" w%d", f.words)}, nil
	}
	return core.LiveUpdate{}, nil
}
func (f *fakeStream) Reset()       { f.resets++; f.fed = 0; f.words = 0; f.held = false }
func (f *fakeStream) Close() error { return nil }

type loudVAD struct{}

func (loudVAD) IsSpeech(p []int16) bool {
	for _, s := range p {
		if s > 1000 || s < -1000 {
			return true
		}
	}
	return false
}
func (loudVAD) Reset() {}

func tone(ms int) []int16  { return fill(ms, 5000) }
func quiet(ms int) []int16 { return fill(ms, 0) }
func fill(ms int, v int16) []int16 {
	b := make([]int16, ms*SampleRate/1000)
	for i := range b {
		b[i] = v
	}
	return b
}

// run feeds 30 ms buffers and collects events.
func run(t *testing.T, st core.LiveStream, cfg LiveConfig, audio ...[]int16) []LiveEvent {
	t.Helper()
	ch := make(chan []int16, 4096)
	for _, a := range audio {
		for i := 0; i < len(a); i += 480 {
			ch <- a[i:min(i+480, len(a))]
		}
	}
	close(ch)
	var evs []LiveEvent
	if err := RunLive(context.Background(), st, loudVAD{}, ch, cfg, func(e LiveEvent) { evs = append(evs, e) }); err != nil {
		t.Fatal(err)
	}
	return evs
}

func finals(evs []LiveEvent) (out []LiveEvent) {
	for _, e := range evs {
		if e.Final {
			out = append(out, e)
		}
	}
	return
}

func TestRunLive_SilenceDoesNothing(t *testing.T) {
	st := &fakeStream{}
	evs := run(t, st, DefaultLiveConfig(), quiet(5000))
	if len(evs) != 0 || st.fed != 0 {
		t.Fatalf("silence reached the model: %d events, %d samples", len(evs), st.fed)
	}
}

func TestRunLive_SplitsUtterancesAtPauses(t *testing.T) {
	st := &fakeStream{}
	evs := run(t, st, DefaultLiveConfig(), quiet(500), tone(2000), quiet(1500), tone(1000), quiet(300))
	fin := finals(evs)
	if len(fin) != 2 {
		t.Fatalf("want 2 utterances, got %d: %+v", len(fin), evs)
	}
	if fin[0].Seq != 0 || fin[1].Seq != 1 {
		t.Errorf("sequence numbers: %d, %d", fin[0].Seq, fin[1].Seq)
	}
	if st.resets < 2 {
		t.Errorf("stream was reset %d times, want one per utterance", st.resets)
	}
	// Deltas must add up to the final text: nothing lost, nothing repeated.
	var joined string
	for _, e := range evs {
		if e.Seq == 0 {
			joined += e.Delta
		}
	}
	if strings.TrimSpace(joined) != fin[0].Text {
		t.Errorf("deltas %q do not add up to final %q", joined, fin[0].Text)
	}
	if fin[0].Start > 600*time.Millisecond || fin[0].Start < 100*time.Millisecond {
		t.Errorf("utterance start %v should be ~200 ms (speech at 500 ms minus pre-roll)", fin[0].Start)
	}
}

func TestRunLive_PreRollIsFed(t *testing.T) {
	st := &fakeStream{}
	run(t, st, LiveConfig{PreRoll: 300 * time.Millisecond, EndpointSilence: 400 * time.Millisecond},
		quiet(1000), tone(1000))
	if want := 1300 * SampleRate / 1000; st.ever < want {
		t.Fatalf("model got %d samples, want at least %d (300 ms pre-roll + 1 s speech)", st.ever, want)
	}
}

func TestRunLive_FlushesOnCancel(t *testing.T) {
	st := &fakeStream{}
	ch := make(chan []int16, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	var evs []LiveEvent
	done := make(chan error, 1)
	go func() {
		done <- RunLive(ctx, st, loudVAD{}, ch, DefaultLiveConfig(), func(e LiveEvent) { evs = append(evs, e) })
	}()
	for _, c := range split(tone(1500)) {
		ch <- c
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
	if len(finals(evs)) != 1 {
		t.Fatalf("speech in progress was not flushed on cancel: %+v", evs)
	}
}

func split(a []int16) (out [][]int16) {
	for i := 0; i < len(a); i += 480 {
		out = append(out, a[i:min(i+480, len(a))])
	}
	return
}

func TestRunLive_AppliesCorrections(t *testing.T) {
	st := &fakeStream{}
	cfg := DefaultLiveConfig()
	cfg.Corrector = core.NewCorrector(map[string]string{"w1": "ONE"})
	evs := run(t, st, cfg, tone(2000), quiet(1000))
	fin := finals(evs)
	if len(fin) != 1 || !strings.Contains(fin[0].Text, "ONE") || strings.Contains(fin[0].Text, "w1") {
		t.Fatalf("corrections not applied: %+v", fin)
	}
}

func TestRunLive_MaxUtteranceForcesEndpoint(t *testing.T) {
	st := &fakeStream{}
	cfg := DefaultLiveConfig()
	cfg.MaxUtterance = 2 * time.Second
	evs := run(t, st, cfg, tone(5000))
	if n := len(finals(evs)); n < 2 {
		t.Fatalf("continuous speech produced %d utterances; expected forced endpoints", n)
	}
}

type fakeRefiner struct {
	mu    sync.Mutex
	calls []int // samples per call
	delay time.Duration
}

func (f *fakeRefiner) Refine(_ context.Context, pcm []int16) (string, error) {
	time.Sleep(f.delay)
	f.mu.Lock()
	f.calls = append(f.calls, len(pcm))
	f.mu.Unlock()
	return fmt.Sprintf("refined-%d", len(f.calls)), nil
}
func (f *fakeRefiner) Close() error { return nil }

func TestRunLive_RefinesEachUtterance(t *testing.T) {
	st := &fakeStream{}
	ref := &fakeRefiner{delay: 20 * time.Millisecond}
	cfg := DefaultLiveConfig()
	cfg.Refiner = ref
	evs := run(t, st, cfg, quiet(300), tone(2000), quiet(1500), tone(1500), quiet(1000))

	var refined []LiveEvent
	for _, e := range evs {
		if e.Refined {
			refined = append(refined, e)
		}
	}
	if len(refined) != 2 {
		t.Fatalf("want 2 refined events (RunLive must wait for the queue), got %d: %+v", len(refined), evs)
	}
	if refined[0].Seq != 0 || refined[1].Seq != 1 || !strings.HasPrefix(refined[0].Text, "refined-") {
		t.Errorf("refined events: %+v", refined)
	}
	// The refiner got the utterance audio (speech plus pre-roll and trailing silence).
	if len(ref.calls) != 2 || ref.calls[0] < 2000*SampleRate/1000 {
		t.Errorf("refiner audio sizes: %v", ref.calls)
	}
	// Live finals are still delivered, unchanged and first.
	if n := len(finals(evs)); n != 2 {
		t.Errorf("want 2 live finals, got %d", n)
	}
}

// silentStream never recognises a word, like the live model on some short
// utterances.
type silentStream struct{}

func (silentStream) Accept(context.Context, []int16) (core.LiveUpdate, error) {
	return core.LiveUpdate{}, nil
}
func (silentStream) Flush(context.Context) (core.LiveUpdate, error) { return core.LiveUpdate{}, nil }
func (silentStream) Reset()                                         {}
func (silentStream) Close() error                                   { return nil }

func TestRunLive_RefinesWhenLiveModelHeardNothing(t *testing.T) {
	ref := &fakeRefiner{}
	cfg := DefaultLiveConfig()
	cfg.Refiner = ref
	cfg.Speakers = &fakeLabeler{}
	evs := run(t, silentStream{}, cfg, quiet(300), tone(2500), quiet(1000))
	var refined []LiveEvent
	for _, e := range evs {
		if e.Refined {
			refined = append(refined, e)
		}
	}
	if len(refined) != 1 || refined[0].Text != "refined-1" {
		t.Fatalf("speech the live model missed was not refined: %+v", evs)
	}
	// The final still comes first and carries the speaker, so voice matching
	// can judge the utterance.
	if fin := finals(evs); len(fin) != 1 || fin[0].Text != "" || fin[0].Speaker == "" {
		t.Errorf("final: %+v", fin)
	}

	// Without a refiner an empty utterance stays silent.
	if evs := run(t, silentStream{}, DefaultLiveConfig(), tone(2500), quiet(1000)); len(finals(evs)) != 0 {
		t.Errorf("empty final without a refiner: %+v", evs)
	}
}

type fakeLabeler struct{ calls int }

func (f *fakeLabeler) Label(pcm []int16) (string, float64) {
	f.calls++
	if len(pcm) < 2*SampleRate {
		return "", 0
	}
	return fmt.Sprintf("Kişi %d", f.calls), 0.7
}

func TestRunLive_LabelsSpeakers(t *testing.T) {
	st := &fakeStream{}
	cfg := DefaultLiveConfig()
	cfg.Speakers = &fakeLabeler{}
	cfg.Refiner = &fakeRefiner{}
	evs := run(t, st, cfg, tone(3000), quiet(1500), tone(3000), quiet(1000))

	got := map[string]string{}
	for _, e := range evs {
		if e.Final {
			got[fmt.Sprintf("final%d", e.Seq)] = e.Speaker
		}
		if e.Refined {
			got[fmt.Sprintf("refined%d", e.Seq)] = e.Speaker
		}
	}
	want := map[string]string{"final0": "Kişi 1", "final1": "Kişi 2", "refined0": "Kişi 1", "refined1": "Kişi 2"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s speaker = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

// splitLabeler pretends the utterance holds two speakers, switching at 3 s.
type splitLabeler struct{}

func (splitLabeler) Label([]int16) (string, float64) { return "Ayşe", 0.8 }
func (splitLabeler) Turns(pcm []int16) []core.SpeakerTurn {
	d := time.Duration(len(pcm)) * time.Second / SampleRate
	return []core.SpeakerTurn{{End: 3 * time.Second, Name: "Ayşe", Score: 0.8}, {Start: 3 * time.Second, End: d, Name: "Mehmet", Score: 0.7}}
}

func TestRunLive_RefinesPerSpeakerTurn(t *testing.T) {
	cfg := DefaultLiveConfig()
	cfg.Speakers = splitLabeler{}
	ref := &fakeRefiner{}
	cfg.Refiner = ref
	evs := run(t, &fakeStream{}, cfg, tone(7000), quiet(1500))

	var parts []LiveEvent
	for _, e := range evs {
		if e.Refined {
			parts = append(parts, e)
		}
	}
	if len(parts) != 2 || parts[0].Speaker != "Ayşe" || parts[1].Speaker != "Mehmet" {
		t.Fatalf("want two refined turns Ayşe/Mehmet, got %+v", parts)
	}
	if parts[0].Part != 0 || parts[1].Part != 1 || parts[1].Parts != 2 {
		t.Errorf("part numbering: %+v", parts)
	}
	if parts[1].Start <= parts[0].Start || parts[0].End > parts[1].Start+time.Millisecond {
		t.Errorf("turn times overlap: %v-%v then %v-%v", parts[0].Start, parts[0].End, parts[1].Start, parts[1].End)
	}
	if len(ref.calls) != 2 {
		t.Errorf("refiner should have seen each turn separately: %v", ref.calls)
	}
}

// dyingRefiner answers its first `ok` calls and fails every later one, like a
// refiner sidecar that was killed mid-session.
type dyingRefiner struct {
	mu    sync.Mutex
	n, ok int
}

func (d *dyingRefiner) Refine(context.Context, []int16) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n++
	if d.n > d.ok {
		return "", fmt.Errorf("helper process exited")
	}
	return fmt.Sprintf("refined-%d", d.n), nil
}
func (d *dyingRefiner) Close() error { return nil }

// A refiner that dies mid-session must not leave holes: every utterance has
// either a refined event or an explicit unrefined one carrying the live text.
func TestRunLive_RefineFailureIsExplicit(t *testing.T) {
	st := &fakeStream{}
	cfg := DefaultLiveConfig()
	cfg.Refiner = &dyingRefiner{ok: 1}
	evs := run(t, st, cfg, quiet(300), tone(2000), quiet(1500), tone(2000), quiet(1500), tone(2000), quiet(1500))

	fin := finals(evs)
	if len(fin) != 3 {
		t.Fatalf("want 3 utterances, got %d", len(fin))
	}
	refined, raw := map[int]LiveEvent{}, map[int]LiveEvent{}
	for _, e := range evs {
		switch {
		case e.Refined:
			refined[e.Seq] = e
		case e.Unrefined:
			raw[e.Seq] = e
		}
	}
	for _, f := range fin {
		_, r := refined[f.Seq]
		u, w := raw[f.Seq]
		if !r && !w {
			t.Errorf("utterance %d has neither a refined nor an unrefined event", f.Seq)
		}
		if w && (u.Text != f.Text || u.Text == "") {
			t.Errorf("utterance %d unrefined text %q, want the live text %q", f.Seq, u.Text, f.Text)
		}
	}
	if len(refined) != 1 || len(raw) != 2 {
		t.Errorf("refined=%d unrefined=%d, want 1 and 2", len(refined), len(raw))
	}
}

type threeTurns struct{ splitLabeler }

func (threeTurns) Turns(pcm []int16) []core.SpeakerTurn {
	d := time.Duration(len(pcm)) * time.Second / SampleRate
	return []core.SpeakerTurn{
		{End: 2 * time.Second, Name: "A"},
		{Start: 2 * time.Second, End: 4 * time.Second, Name: "B"},
		{Start: 4 * time.Second, End: d, Name: "C"},
	}
}

// failMiddle fails its second call.
type failMiddle struct{ n int }

func (f *failMiddle) Refine(context.Context, []int16) (string, error) {
	f.n++
	if f.n == 2 {
		return "", fmt.Errorf("helper process exited")
	}
	return fmt.Sprintf("refined-%d", f.n), nil
}
func (f *failMiddle) Close() error { return nil }

// One failed turn in a multi-turn utterance: no per-turn refined parts at all,
// one whole-utterance unrefined event with the full live text, so every word
// reaches the client exactly once.
func TestRunLive_MultiTurnFailureEmitsWholeUtteranceOnce(t *testing.T) {
	cfg := DefaultLiveConfig()
	cfg.Speakers = threeTurns{}
	cfg.Refiner = &failMiddle{}
	evs := run(t, &fakeStream{}, cfg, tone(7000), quiet(1500))
	var fin LiveEvent
	var out []LiveEvent
	for _, e := range evs {
		if e.Final {
			fin = e
		}
		if e.Refined || e.Unrefined {
			out = append(out, e)
		}
	}
	if len(out) != 1 || out[0].Refined || !out[0].Unrefined {
		t.Fatalf("want exactly one unrefined whole-utterance event, got %+v", out)
	}
	if out[0].Text != fin.Text || out[0].Text == "" {
		t.Errorf("text %q, want the full live text %q", out[0].Text, fin.Text)
	}
	if out[0].Part != 0 || out[0].Parts != 1 {
		t.Errorf("part/parts = %d/%d, want 0/1", out[0].Part, out[0].Parts)
	}
}

// slowFirst delays its first call, so later utterances would overtake it.
type slowFirst struct {
	mu sync.Mutex
	n  int
}

func (f *slowFirst) Refine(context.Context, []int16) (string, error) {
	f.mu.Lock()
	f.n++
	n := f.n
	f.mu.Unlock()
	if n == 1 {
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Sprintf("refined-%d", n), nil
}
func (f *slowFirst) Close() error { return nil }

// Refined and unrefined events leave in utterance order, even when a later
// utterance is turned away from a full queue while an earlier one is slow.
func TestRunLive_RefinementEventsKeepUtteranceOrder(t *testing.T) {
	cfg := DefaultLiveConfig()
	cfg.Refiner = &slowFirst{}
	cfg.RefineQueue = 1
	var audio [][]int16
	audio = append(audio, quiet(300))
	for i := 0; i < 5; i++ {
		audio = append(audio, tone(1500), quiet(1500))
	}
	evs := run(t, &fakeStream{}, cfg, audio...)
	last, n := -1, 0
	for _, e := range evs {
		if !e.Refined && !e.Unrefined {
			continue
		}
		n++
		if e.Seq < last {
			t.Errorf("utterance %d emitted after %d", e.Seq, last)
		}
		last = e.Seq
	}
	if n != 5 {
		t.Errorf("%d refinement events, want one per utterance (5)", n)
	}
}

type shortMiddle struct{ splitLabeler }

func (shortMiddle) Turns(pcm []int16) []core.SpeakerTurn {
	d := time.Duration(len(pcm)) * time.Second / SampleRate
	return []core.SpeakerTurn{
		{End: 2 * time.Second, Name: "A"},
		{Start: 2 * time.Second, End: 2300 * time.Millisecond, Name: "B"},
		{Start: 2300 * time.Millisecond, End: d, Name: "C"},
	}
}

// A speaker turn under 0.5 s is skipped on purpose; that is not a failure.
// Its audio joins the neighbouring turn, so its words come from the refiner.
func TestRunLive_ShortTurnIsNotAFailure(t *testing.T) {
	cfg := DefaultLiveConfig()
	cfg.Speakers = shortMiddle{}
	ref := &fakeRefiner{}
	cfg.Refiner = ref
	evs := run(t, &fakeStream{}, cfg, tone(7000), quiet(1500))
	var parts []LiveEvent
	for _, e := range evs {
		if e.Unrefined {
			t.Fatalf("unrefined event for an utterance whose only issue is a short turn: %+v", e)
		}
		if e.Refined {
			parts = append(parts, e)
		}
	}
	if len(parts) != 2 || parts[0].Parts != 2 || parts[1].Part != 1 {
		t.Fatalf("want two refined parts, got %+v", parts)
	}
	if parts[0].End != parts[1].Start && parts[1].Start-parts[0].End > time.Millisecond {
		t.Errorf("gap between parts: %v then %v", parts[0].End, parts[1].Start)
	}
	if len(ref.calls) != 2 {
		t.Errorf("refiner calls = %d, want 2", len(ref.calls))
	}
}

// stuckFirst blocks its first call until released; later calls answer.
type stuckFirst struct {
	mu      sync.Mutex
	n       int
	release chan struct{}
}

func (f *stuckFirst) Refine(context.Context, []int16) (string, error) {
	f.mu.Lock()
	f.n++
	n := f.n
	f.mu.Unlock()
	if n == 1 {
		<-f.release
	}
	return fmt.Sprintf("refined-%d", n), nil
}
func (f *stuckFirst) Close() error { return nil }

// One refine call that never returns must not hold back later utterances:
// all five get exactly one event, in order, the stuck one unrefined.
func TestRunLive_StuckRefineDoesNotBlockLaterUtterances(t *testing.T) {
	ref := &stuckFirst{release: make(chan struct{})}
	cfg := DefaultLiveConfig()
	cfg.Refiner = ref
	cfg.RefineTimeout = 150 * time.Millisecond
	cfg.RefineDrain = 3 * time.Second
	audio := [][]int16{quiet(300)}
	for i := 0; i < 5; i++ {
		audio = append(audio, tone(1500), quiet(1500))
	}
	done := make(chan []LiveEvent, 1)
	go func() { done <- run(t, &fakeStream{}, cfg, audio...) }()
	time.AfterFunc(1500*time.Millisecond, func() { close(ref.release) }) // the late result
	var evs []LiveEvent
	select {
	case evs = <-done:
	case <-time.After(8 * time.Second):
		close(ref.release)
		t.Fatal("RunLive did not finish")
	}
	var got []LiveEvent
	for _, e := range evs {
		if e.Refined || e.Unrefined {
			got = append(got, e)
		}
	}
	if len(got) != 5 {
		t.Fatalf("refinement events = %d, want 5 (one per utterance): %+v", len(got), got)
	}
	for i, e := range got {
		if e.Seq != i {
			t.Errorf("event %d is for utterance %d", i, e.Seq)
		}
	}
	if !got[0].Unrefined || got[0].Text == "" {
		t.Errorf("the stuck utterance must be unrefined with its live text: %+v", got[0])
	}
}
