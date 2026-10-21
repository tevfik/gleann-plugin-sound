package main

import (
	"testing"

	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
)

func TestRefinedTyper_TypesRefinedTextInOrder(t *testing.T) {
	var out []string
	r := newRefinedTyper(func(s string) error { out = append(out, s); return nil })

	r.Handle(pipeline.LiveEvent{Seq: 0, Final: true, Text: "live zero"})
	r.Handle(pipeline.LiveEvent{Seq: 1, Final: true, Text: "live one"})
	r.Handle(pipeline.LiveEvent{Seq: 0, Refined: true, Text: " Refined zero. "})
	r.Handle(pipeline.LiveEvent{Seq: 1, Refined: true, Text: "Refined one."})
	r.Flush()

	want := []string{"Refined zero.", " Refined one."}
	if len(out) != len(want) {
		t.Fatalf("typed %q, want %q", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("typed[%d] = %q, want %q", i, out[i], want[i])
		}
	}
}

func TestRefinedTyper_FlushFallsBackToLiveText(t *testing.T) {
	var out []string
	r := newRefinedTyper(func(s string) error { out = append(out, s); return nil })

	r.Handle(pipeline.LiveEvent{Seq: 0, Final: true, Text: "kept live"})
	r.Handle(pipeline.LiveEvent{Seq: 1, Final: true, Text: "refined later"})
	r.Handle(pipeline.LiveEvent{Seq: 1, Refined: true, Text: "Refined."})
	r.Flush()

	if len(out) != 2 || out[0] != "Refined." || out[1] != " kept live" {
		t.Errorf("typed %q", out)
	}
}

func TestRefinedTyper_ResetDropsLeadingSpace(t *testing.T) {
	var out []string
	r := newRefinedTyper(func(s string) error { out = append(out, s); return nil })
	r.Handle(pipeline.LiveEvent{Seq: 0, Refined: true, Text: "one"})
	r.reset()
	r.Handle(pipeline.LiveEvent{Seq: 0, Refined: true, Text: "two"})
	if len(out) != 2 || out[1] != "two" {
		t.Errorf("typed %q, want second dictation without leading space", out)
	}
}
