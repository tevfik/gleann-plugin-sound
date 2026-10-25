package main

import (
	"testing"

	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
)

func TestVoiceAccepted(t *testing.T) {
	if !voiceAccepted("Tevfik") {
		t.Error("an enrolled voice must be accepted")
	}
	if voiceAccepted("?") || voiceAccepted("") {
		t.Error("unrecognised and too-short-to-judge voices must be rejected")
	}
}

func TestVoiceGate(t *testing.T) {
	g := &voiceGate{}
	if got := g.Handle(pipeline.LiveEvent{Delta: "mer", Text: "mer"}); got != "" {
		t.Errorf("partial words must not be typed, got %q", got)
	}
	if got := g.Handle(pipeline.LiveEvent{Final: true, Text: "merhaba dünya", Speaker: "Tevfik"}); got != "merhaba dünya" {
		t.Errorf("first utterance = %q", got)
	}
	if got := g.Handle(pipeline.LiveEvent{Final: true, Text: "tv haberleri", Speaker: "?"}); got != "" {
		t.Errorf("unknown voice typed %q", got)
	}
	if got := g.Handle(pipeline.LiveEvent{Final: true, Text: "kısa", Speaker: ""}); got != "" {
		t.Errorf("too-short voice typed %q", got)
	}
	if got := g.Handle(pipeline.LiveEvent{Final: true, Text: "devam", Speaker: "Tevfik"}); got != " devam" {
		t.Errorf("second utterance = %q, want leading space", got)
	}
	g.reset()
	if got := g.Handle(pipeline.LiveEvent{Final: true, Text: "yeni", Speaker: "Tevfik"}); got != "yeni" {
		t.Errorf("after reset = %q", got)
	}
}
