package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

func cat(parts ...[]int16) []int16 {
	var out []int16
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestTranscribeFile_FindsSpeechAndTimestamps(t *testing.T) {
	pcm := cat(quiet(1000), tone(2000), quiet(300), tone(1000), quiet(2000), tone(1500), quiet(500))
	ref := &fakeRefiner{}
	segs, err := TranscribeFile(context.Background(), pcm, loudVAD{}, ref, FileConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// The 0.3 s pause is bridged; the 2 s pause splits.
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %+v", segs)
	}
	if segs[0].Start < 700*time.Millisecond || segs[0].Start > 1100*time.Millisecond {
		t.Errorf("first segment starts at %v, speech begins at 1 s", segs[0].Start)
	}
	if d := segs[0].End - segs[0].Start; d < 3300*time.Millisecond || d > 4000*time.Millisecond {
		t.Errorf("first segment lasts %v, want ~3.3 s plus padding", d)
	}
	if segs[1].Start < segs[0].End {
		t.Errorf("segments overlap: %+v", segs)
	}
	if !strings.HasPrefix(segs[0].Text, "refined-") {
		t.Errorf("text: %+v", segs[0])
	}
}

func TestTranscribeFile_SplitsLongStretches(t *testing.T) {
	pcm := cat(quiet(300), tone(40000), quiet(300))
	ref := &fakeRefiner{}
	segs, err := TranscribeFile(context.Background(), pcm, loudVAD{}, ref, FileConfig{MaxSegment: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) < 3 {
		t.Fatalf("40 s of speech with a 15 s cap should give >=3 segments, got %d", len(segs))
	}
	for _, s := range segs {
		if s.End-s.Start > 16*time.Second {
			t.Errorf("segment of %v exceeds the cap", s.End-s.Start)
		}
	}
}

func TestTranscribeFile_Speakers(t *testing.T) {
	pcm := cat(quiet(500), tone(7000), quiet(1500))
	segs, err := TranscribeFile(context.Background(), pcm, loudVAD{}, &fakeRefiner{}, FileConfig{Speakers: splitLabeler{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 || segs[0].Speaker != "Ayşe" || segs[1].Speaker != "Mehmet" {
		t.Fatalf("segments: %+v", segs)
	}
	if segs[0].End > segs[1].Start+time.Millisecond {
		t.Errorf("turns overlap: %+v", segs)
	}
}

func TestTranscribeFile_NeedsRefiner(t *testing.T) {
	if _, err := TranscribeFile(context.Background(), quiet(1000), loudVAD{}, nil, FileConfig{}); err == nil {
		t.Error("expected an error without a refiner")
	}
}

var _ core.Refiner = (*fakeRefiner)(nil)
