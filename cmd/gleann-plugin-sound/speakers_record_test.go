package main

import (
	"math"
	"strings"
	"testing"

	"github.com/tevfik/gleann-plugin-sound/internal/speaker"
)

type fakeEmbed struct{}

func (fakeEmbed) Embed([]int16) ([]float32, error) { return []float32{1, 0, 0}, nil }
func (fakeEmbed) Close() error                     { return nil }

func tone(seconds int, amp float64) []int16 {
	pcm := make([]int16, seconds*16000)
	for i := range pcm {
		pcm[i] = int16(amp * 32767 * math.Sin(2*math.Pi*220*float64(i)/16000))
	}
	return pcm
}

func TestEnrollClipsStoresVoice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := enrollClips(fakeEmbed{}, "Ayse", [][]int16{tone(20, 0.3)}, 12); err != nil {
		t.Fatal(err)
	}
	reg, err := speaker.LoadRegistry(speaker.DefaultPath())
	if err != nil || reg.Len() != 1 || reg.Profiles[0].Name != "Ayse" {
		t.Fatalf("registry = %+v, %v", reg, err)
	}
	// Recording again under the same name replaces the voice, not duplicates it.
	if err := enrollClips(fakeEmbed{}, "ayse", [][]int16{tone(20, 0.3)}, 12); err != nil {
		t.Fatal(err)
	}
	reg, _ = speaker.LoadRegistry(speaker.DefaultPath())
	if reg.Len() != 1 {
		t.Fatalf("re-record created %d profiles", reg.Len())
	}
}

func TestEnrollClipsRejectsSilenceAndShortClips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for name, clip := range map[string][]int16{"silence": tone(30, 0), "too short": tone(4, 0.3)} {
		err := enrollClips(fakeEmbed{}, "X", [][]int16{clip}, 12)
		if err == nil || !strings.Contains(err.Error(), "usable") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if reg, _ := speaker.LoadRegistry(speaker.DefaultPath()); reg.Len() != 0 {
		t.Error("a failed enrolment must not store anything")
	}
}

func TestDBBar(t *testing.T) {
	if got := dbBar(0, 10); got != "["+strings.Repeat("·", 10)+"]" {
		t.Errorf("silent bar = %q", got)
	}
	if got := dbBar(1, 10); got != "["+strings.Repeat("█", 10)+"]" {
		t.Errorf("full bar = %q", got)
	}
}

func TestWrapText(t *testing.T) {
	lines := wrapText(speaker.ReadingText("tr"), 72)
	if len(lines) < 3 {
		t.Fatalf("expected the text to wrap, got %d line(s)", len(lines))
	}
	for _, l := range lines {
		if n := len([]rune(l)); n > 72 {
			t.Errorf("line of %d runes exceeds 72: %q", n, l)
		}
	}
	if got := strings.Join(lines, " "); got != speaker.ReadingText("tr") {
		t.Errorf("wrapping changed the text: %q", got)
	}
}
