package main

import (
	"log"
	"strings"

	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
	"github.com/tevfik/gleann-plugin-sound/internal/speaker"
)

// voiceAccepted reports whether an utterance labelled name may be typed when
// dictation is limited to enrolled voices: only a voice that matched someone
// enrolled. "?" (nobody) and "" (too short to judge: under dictateMinVoice,
// which with the pause and pre-roll around it is a word of well under a
// second) are refused; letting them through typed other people's words.
func voiceAccepted(name string) bool { return name != "" && name != speaker.UnknownName }

// dictateMinVoice is the shortest utterance audio judged in dictation. The
// enrolled user still scored 0.47 on 1 s pieces, others at most 0.38.
const dictateMinVoice = pipeline.SampleRate

// logVoice records the decision on one utterance, with its score, so the
// threshold can be tuned (Speakers menu, + and -).
func logVoice(ev pipeline.LiveEvent, accepted bool) {
	switch {
	case accepted:
		log.Printf("[dictate] voice: %s (score %.2f)", ev.Speaker, ev.Score)
	case ev.Speaker == "":
		log.Println("[dictate] ignored an utterance too short to check the voice")
	default:
		log.Printf("[dictate] ignored an utterance from an unrecognised voice (score %.2f)", ev.Score)
	}
}

// voiceGate buffers live words per utterance and releases them only when the
// utterance has ended and its voice is accepted.
type voiceGate struct {
	typed bool // something was released this session
}

// Handle consumes one live event and returns the text to type ("" for none).
func (g *voiceGate) Handle(ev pipeline.LiveEvent) string {
	if !ev.Final {
		return ""
	}
	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return ""
	}
	ok := voiceAccepted(ev.Speaker)
	logVoice(ev, ok)
	if !ok {
		return ""
	}
	if g.typed {
		text = " " + text
	}
	g.typed = true
	return text
}

func (g *voiceGate) reset() { g.typed = false }
