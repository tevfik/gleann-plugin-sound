//go:build onnx
// +build onnx

package nemotron

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/audio"
	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
)

// Silence, an English phrase, a pause, a Turkish phrase, silence — captured in
// 30 ms buffers through the real VAD and pipeline.
func TestLive_EndToEndWithVAD(t *testing.T) {
	e := newTestEngine(t, "auto")
	jfk := loadWav(t, "jfk.wav")
	tr := loadWav(t, "tr_test.wav")

	silence := func(sec float64) []int16 { return make([]int16, int(sec*16000)) }
	var pcm []int16
	for _, part := range [][]int16{silence(1), jfk, silence(2), tr, silence(1.5)} {
		pcm = append(pcm, part...)
	}

	stream, err := e.NewLiveStream()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	ch := make(chan []int16, len(pcm)/480+1)
	for i := 0; i < len(pcm); i += 480 {
		ch <- pcm[i:min(i+480, len(pcm))]
	}
	close(ch)

	var finals []pipeline.LiveEvent
	partials := 0
	start := time.Now()
	err = pipeline.RunLive(context.Background(), stream, audio.DefaultVAD(), ch, pipeline.DefaultLiveConfig(),
		func(ev pipeline.LiveEvent) {
			if ev.Final {
				finals = append(finals, ev)
				t.Logf("final #%d [%v-%v]: %q", ev.Seq, ev.Start.Round(10*time.Millisecond), ev.End.Round(10*time.Millisecond), ev.Text)
			} else if ev.Delta != "" {
				partials++
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	audioLen := time.Duration(len(pcm)) * time.Second / 16000
	t.Logf("%d finals, %d incremental releases; %.1fs of audio processed in %.1fs",
		len(finals), partials, audioLen.Seconds(), time.Since(start).Seconds())

	if len(finals) != 2 {
		t.Fatalf("want 2 utterances, got %d", len(finals))
	}
	if !strings.Contains(strings.ToLower(finals[0].Text), "country") {
		t.Errorf("utterance 0: %q", finals[0].Text)
	}
	if !strings.Contains(strings.ToLower(finals[1].Text), "klavye") {
		t.Errorf("utterance 1: %q", finals[1].Text)
	}
	if finals[0].Start < 500*time.Millisecond || finals[0].Start > 1200*time.Millisecond {
		t.Errorf("utterance 0 starts at %v; speech begins at 1 s", finals[0].Start)
	}
	if partials < 5 {
		t.Errorf("only %d incremental releases: text is not streaming", partials)
	}
}
