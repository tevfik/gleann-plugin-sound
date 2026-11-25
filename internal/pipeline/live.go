package pipeline

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// LiveConfig tunes RunLive.
type LiveConfig struct {
	// EndpointSilence is how long speech must pause before the utterance is
	// closed and a Final event is emitted. Default 700 ms.
	EndpointSilence time.Duration
	// PreRoll is the audio kept from before the VAD fires, so the first
	// syllable is not clipped. Default 300 ms.
	PreRoll time.Duration
	// MaxUtterance forces an endpoint during very long continuous speech.
	// Default 2 minutes.
	MaxUtterance time.Duration
	// Corrector, if set, is applied to every released word.
	Corrector *core.Corrector
	// Refiner, if set, re-transcribes every finished utterance in the
	// background with a more accurate (slower) model. The result arrives later
	// as an event with Refined set; the live text is never retracted.
	Refiner core.Refiner
	// Speakers, if set, names the speaker of every finished utterance (Final and
	// Refined events carry it).
	Speakers core.SpeakerLabeler
	// RefineDrain bounds how long RunLive waits for queued refinements when
	// it ends. Default 30 s.
	RefineDrain time.Duration
	// RefineQueue is how many finished utterances may wait for the refiner.
	// Default 16; one that does not fit gets an Unrefined event.
	RefineQueue int
	// RefineTimeout bounds how long an utterance's refinement may take,
	// counted from when it is queued. After it the utterance is released
	// with an Unrefined event and a late refiner result is dropped, so one
	// stuck call cannot hold back later utterances. Default 10 s.
	RefineTimeout time.Duration
	// Stats, if set, is filled in while RunLive runs (read it after RunLive
	// returns). It tells a silent microphone from a recogniser that heard
	// nothing.
	Stats *LiveStats
}

// LiveStats counts what RunLive saw.
type LiveStats struct {
	Chunks       int // audio chunks received
	SpeechChunks int // chunks the VAD called speech
	Utterances   int // utterances that produced text
}

// DefaultLiveConfig returns settings suited to dictation and live captions.
func DefaultLiveConfig() LiveConfig {
	return LiveConfig{
		EndpointSilence: 700 * time.Millisecond,
		PreRoll:         300 * time.Millisecond,
		MaxUtterance:    2 * time.Minute,
	}
}

// LiveEvent reports progress of one utterance.
type LiveEvent struct {
	Seq     int    // utterance number, from 0
	Delta   string // newly finalised text; append-only, safe to type
	Pending string // unfinished word, for previews only
	Text    string // finalised text of the utterance so far
	Final   bool   // the utterance has ended; Text is complete
	Refined bool   // Text is the refiner's better transcript of utterance Seq (Final is false)
	// Unrefined marks the refiner's slot for utterance Seq as not filled: a
	// refine call failed (for any speaker turn) or the job was dropped. It is
	// one event for the whole utterance (Part 0, Parts 1) and Text is the
	// utterance's live transcript. Refined is false on these events.
	Unrefined bool
	// Part/Parts: when speakers answered each other inside one utterance the
	// refined transcript arrives as several events, one per speaker turn.
	Part, Parts int
	// Speaker names who spoke (Final and Refined events, when Speakers is set;
	// "" when the utterance was too short to tell). Score is the match cosine.
	Speaker string
	Score   float64
	// Start and End are positions on the session's audio clock. End is the
	// audio consumed so far (the utterance end on Final events).
	Start, End time.Duration
}

// OnLive receives LiveEvents. It runs on the pipeline goroutine, so a slow
// handler delays recognition.
type OnLive func(LiveEvent)

// RunLive drives a LiveStream from captured audio. Audio is fed to the model
// continuously while someone is speaking; while it is quiet nothing is
// processed at all, which keeps idle CPU use at the cost of one VAD check per
// buffer. Unlike the sliding-window StreamingPipeline there is no re-decoding
// of overlapping audio and no deduplication: words are released once, a
// fraction of a second after they are spoken.
//
// RunLive returns when ctx is cancelled or audioCh is closed; an utterance in
// progress is flushed first, so the last words are never lost.
func RunLive(ctx context.Context, stream core.LiveStream, vad core.VADProvider,
	audioCh <-chan []int16, cfg LiveConfig, on OnLive) error {

	def := DefaultLiveConfig()
	if cfg.EndpointSilence <= 0 {
		cfg.EndpointSilence = def.EndpointSilence
	}
	if cfg.PreRoll < 0 {
		cfg.PreRoll = 0
	} else if cfg.PreRoll == 0 {
		cfg.PreRoll = def.PreRoll
	}
	if cfg.MaxUtterance <= 0 {
		cfg.MaxUtterance = def.MaxUtterance
	}
	if cfg.RefineQueue <= 0 {
		cfg.RefineQueue = 16
	}
	if cfg.RefineTimeout <= 0 {
		cfg.RefineTimeout = 10 * time.Second
	}
	if cfg.RefineDrain <= 0 {
		cfg.RefineDrain = 30 * time.Second
	}

	samples := func(d time.Duration) int { return int(d * SampleRate / time.Second) }
	dur := func(n int) time.Duration { return time.Duration(n) * time.Second / SampleRate }

	// Events come from this goroutine and from the refinement worker.
	var cbMu sync.Mutex
	userOn := on
	on = func(ev LiveEvent) {
		cbMu.Lock()
		defer cbMu.Unlock()
		userOn(ev)
	}

	// Refinement results leave in utterance order: an utterance's event(s),
	// refined or unrefined, are held until every earlier utterance has
	// released its own. Every utterance releases exactly once (refined,
	// failed, dropped from a full queue or never queued), so nothing waits
	// forever.
	var ordMu sync.Mutex
	nextOut := 0
	held := map[int][]LiveEvent{}
	done := map[int]bool{}           // seq already released: later releases are dropped
	timers := map[int]*time.Timer{}  // refine deadlines of queued utterances
	fallbacks := map[int]LiveEvent{} // what each armed utterance releases on timeout
	releaseLocked := func(seq int, evs ...LiveEvent) {
		if done[seq] {
			return // a late result after the deadline: never emit twice
		}
		done[seq] = true
		if t := timers[seq]; t != nil {
			t.Stop()
		}
		delete(timers, seq)
		delete(fallbacks, seq)
		held[seq] = evs
		for {
			e, ok := held[nextOut]
			if !ok {
				return
			}
			delete(held, nextOut)
			nextOut++
			for _, ev := range e {
				on(ev)
			}
		}
	}
	release := func(seq int, evs ...LiveEvent) {
		ordMu.Lock()
		defer ordMu.Unlock()
		releaseLocked(seq, evs...)
	}
	isDone := func(seq int) bool {
		ordMu.Lock()
		defer ordMu.Unlock()
		return done[seq]
	}
	// arm starts the refine deadline of a queued utterance.
	arm := func(seq int, fb LiveEvent) {
		ordMu.Lock()
		defer ordMu.Unlock()
		fallbacks[seq] = fb
		timers[seq] = time.AfterFunc(cfg.RefineTimeout, func() {
			log.Printf("[live] refine utterance %d timed out after %v; keeping its live transcript", seq, cfg.RefineTimeout)
			release(seq, fb)
		})
	}

	type refineJob struct {
		seq        int
		pcm        []int16
		start, end time.Duration
		speaker    string
		score      float64
		text       string // the utterance's live transcript, the fallback
		fb         LiveEvent
	}
	var jobs chan refineJob
	var workerDone chan struct{}
	if cfg.Refiner != nil {
		jobs = make(chan refineJob, cfg.RefineQueue)
		workerDone = make(chan struct{})
		go func() {
			defer close(workerDone)
			for j := range jobs {
				if isDone(j.seq) {
					continue // its deadline passed while it waited
				}
				// People answering each other without a pause end up in one
				// utterance; split the audio at speaker changes so each
				// speaker's words are refined (and labelled) separately.
				turns := []core.SpeakerTurn{{End: dur(len(j.pcm)), Name: j.speaker, Score: j.score}}
				if ts, ok := cfg.Speakers.(core.TurnSplitter); ok {
					if t := ts.Turns(j.pcm); len(t) > 1 {
						turns = t
					}
				}
				// Parts are held back until every turn is refined: if any turn
				// fails the client gets one whole-utterance Unrefined event
				// with the full live text instead, so each word arrives
				// exactly once (the live text has no per-turn split).
				// A turn under half a second is not worth a call of its own:
				// its audio joins the neighbouring turn, whose refined text
				// then covers its words (not a failure).
				type span struct {
					a, b int
					tn   core.SpeakerTurn
				}
				var spans []span
				carry := -1
				for _, tn := range turns {
					a, b := samples(tn.Start), min(samples(tn.End), len(j.pcm))
					if carry >= 0 {
						a = carry
					}
					if b-a < SampleRate/2 {
						if n := len(spans); n > 0 {
							spans[n-1].b = b
							spans[n-1].tn.End = tn.End
							carry = -1
						} else if carry < 0 {
							carry = a
						}
						continue
					}
					carry = -1
					tn.Start = dur(a)
					spans = append(spans, span{a, b, tn})
				}
				parts := make([]LiveEvent, 0, len(spans))
				ok := len(spans) > 0 // whole utterance under 0.5 s: keep its live text
				for p, sp := range spans {
					text, err := cfg.Refiner.Refine(context.Background(), j.pcm[sp.a:sp.b])
					if err != nil {
						log.Printf("[live] refine utterance %d: %v", j.seq, err)
						ok = false
						break
					}
					if text = strings.TrimSpace(cfg.Corrector.Apply(text)); text == "" {
						ok = false
						break
					}
					parts = append(parts, LiveEvent{
						Seq: j.seq, Text: text, Refined: true, Part: p, Parts: len(spans),
						Start: j.start + sp.tn.Start, End: j.start + sp.tn.End, Speaker: sp.tn.Name, Score: sp.tn.Score,
					})
				}
				if !ok {
					release(j.seq, j.fb)
					continue
				}
				release(j.seq, parts...)
			}
		}()
	}
	defer func() {
		if jobs == nil {
			return
		}
		close(jobs)
		select {
		case <-workerDone:
		case <-time.After(cfg.RefineDrain):
			log.Printf("[live] gave up waiting for queued refinements after %v", cfg.RefineDrain)
		}
		// Whatever is still pending keeps its live transcript, and no timer
		// fires after RunLive has returned.
		ordMu.Lock()
		defer ordMu.Unlock()
		seqs := make([]int, 0, len(fallbacks))
		for seq := range fallbacks {
			seqs = append(seqs, seq)
		}
		sort.Ints(seqs)
		for _, seq := range seqs {
			releaseLocked(seq, fallbacks[seq])
		}
	}()

	var (
		clock       int // samples seen
		preroll     []int16
		speaking    bool
		silence     int // consecutive quiet samples while speaking
		uttSamples  int
		uttStart    int
		uttPCM      []int16 // audio of the utterance in progress, for the refiner
		seq         int
		text        strings.Builder
		lastPending string
	)

	emit := func(u core.LiveUpdate, final bool) {
		delta := cfg.Corrector.Apply(u.Delta)
		text.WriteString(delta)
		if !final && delta == "" && u.Pending == lastPending {
			return
		}
		lastPending = u.Pending
		on(LiveEvent{
			Seq:     seq,
			Delta:   delta,
			Pending: cfg.Corrector.Apply(u.Pending),
			Text:    text.String(),
			Final:   final,
			Start:   dur(uttStart),
			End:     dur(clock),
		})
	}

	feed := func(c context.Context, pcm []int16) error {
		if jobs != nil || cfg.Speakers != nil {
			uttPCM = append(uttPCM, pcm...)
		}
		u, err := stream.Accept(c, pcm)
		if err != nil {
			return err
		}
		emit(u, false)
		return nil
	}

	finish := func(c context.Context) error {
		u, err := stream.Flush(c)
		if err != nil {
			return err
		}
		hasText := strings.TrimSpace(u.Delta) != "" || strings.TrimSpace(text.String()) != ""
		// The live model sometimes returns nothing for a short utterance the
		// refiner hears fine; the VAD heard speech, so let the refiner try.
		refineAnyway := jobs != nil && len(uttPCM) >= SampleRate/2
		if hasText || refineAnyway {
			u.Delta = cfg.Corrector.Apply(u.Delta)
			text.WriteString(u.Delta)
			var who string
			var score float64
			if cfg.Speakers != nil && len(uttPCM) > 0 {
				who, score = cfg.Speakers.Label(uttPCM)
			}
			on(LiveEvent{
				Seq: seq, Delta: u.Delta, Text: strings.TrimSpace(text.String()),
				Final: true, Start: dur(uttStart), End: dur(clock), Speaker: who, Score: score,
			})
			if jobs != nil && len(uttPCM) > 0 {
				live := strings.TrimSpace(text.String())
				fb := LiveEvent{
					Seq: seq, Text: live, Unrefined: true, Parts: 1,
					Start: dur(uttStart), End: dur(clock), Speaker: who, Score: score,
				}
				arm(seq, fb)
				select {
				case jobs <- refineJob{seq: seq, pcm: uttPCM, start: dur(uttStart), end: dur(clock), speaker: who, score: score, text: live, fb: fb}:
					uttPCM = nil
				default:
					log.Printf("[live] refinement queue full; utterance %d keeps its live transcript", seq)
					release(seq, fb)
				}
			} else if jobs != nil {
				release(seq) // nothing to refine; let later utterances through
			}
			seq++
			if cfg.Stats != nil {
				cfg.Stats.Utterances++
			}
		}
		uttPCM = uttPCM[:0]
		stream.Reset()
		text.Reset()
		lastPending = ""
		speaking, silence, uttSamples = false, 0, 0
		return nil
	}

	// A cancelled context must still let the last words out.
	flushCtx := context.WithoutCancel(ctx)

	for {
		select {
		case <-ctx.Done():
			if speaking {
				if err := finish(flushCtx); err != nil {
					return err
				}
			}
			return ctx.Err()

		case chunk, ok := <-audioCh:
			if !ok {
				if speaking {
					return finish(flushCtx)
				}
				return nil
			}
			if len(chunk) == 0 {
				continue
			}
			isSpeech := vad.IsSpeech(chunk)
			if cfg.Stats != nil {
				cfg.Stats.Chunks++
				if isSpeech {
					cfg.Stats.SpeechChunks++
				}
			}

			if !speaking {
				if !isSpeech {
					preroll = append(preroll, chunk...)
					if max := samples(cfg.PreRoll); len(preroll) > max {
						preroll = append(preroll[:0], preroll[len(preroll)-max:]...)
					}
					clock += len(chunk)
					continue
				}
				speaking = true
				uttStart = clock - len(preroll)
				lead := append(preroll, chunk...)
				preroll = nil
				clock += len(chunk)
				uttSamples = len(lead)
				if err := feed(ctx, lead); err != nil {
					return fmt.Errorf("live transcription: %w", err)
				}
				continue
			}

			clock += len(chunk)
			uttSamples += len(chunk)
			if isSpeech {
				silence = 0
			} else {
				silence += len(chunk)
			}
			if err := feed(ctx, chunk); err != nil {
				return fmt.Errorf("live transcription: %w", err)
			}
			if silence >= samples(cfg.EndpointSilence) || dur(uttSamples) >= cfg.MaxUtterance {
				if err := finish(ctx); err != nil {
					return fmt.Errorf("live transcription: %w", err)
				}
			}
		}
	}
}
