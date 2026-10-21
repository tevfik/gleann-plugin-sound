package main

import (
	"strings"
	"testing"

	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
)

func TestLevelMeterSummary(t *testing.T) {
	loud := make([]int16, 16000)
	for i := range loud {
		loud[i] = int16(8000 * (i%2*2 - 1))
	}
	quiet := make([]int16, 16000)
	for i := range quiet {
		quiet[i] = int16(i % 3)
	}
	tests := []struct {
		name  string
		pcm   []int16
		stats pipeline.LiveStats
		typed bool
		want  string
	}{
		{"no audio", nil, pipeline.LiveStats{}, false, "no audio arrived"},
		{"silent", make([]int16, 16000), pipeline.LiveStats{Chunks: 10}, false, "completely silent"},
		{"very quiet", quiet, pipeline.LiveStats{Chunks: 10}, false, "very quiet"},
		{"no speech", loud, pipeline.LiveStats{Chunks: 10}, false, "no speech detected"},
		{"no words", loud, pipeline.LiveStats{Chunks: 10, SpeechChunks: 5}, false, "no words were recognised"},
		{"ok", loud, pipeline.LiveStats{Chunks: 10, SpeechChunks: 5, Utterances: 1}, true, "1 utterance(s)"},
	}
	for _, tt := range tests {
		var m levelMeter
		if tt.pcm != nil {
			m.Add(tt.pcm)
		}
		got := m.Summary(tt.stats, tt.typed)
		if !strings.Contains(got, tt.want) {
			t.Errorf("%s: %q lacks %q", tt.name, got, tt.want)
		}
		if tt.typed && strings.Contains(got, " — ") {
			t.Errorf("%s: a working session should carry no warning: %q", tt.name, got)
		}
	}
}
