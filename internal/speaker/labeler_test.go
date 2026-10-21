package speaker

import (
	"math"
	"path/filepath"
	"testing"
)

// fakeEmbed maps a tag stored in the first sample to a fixed direction, so the
// labelling logic can be tested without a model.
type fakeEmbed struct{ dirs map[int16][]float32 }

func (f fakeEmbed) Embed(pcm []int16) ([]float32, error) { return f.dirs[pcm[0]], nil }

func unitv(xs ...float64) []float32 {
	var n float64
	for _, x := range xs {
		n += x * x
	}
	n = math.Sqrt(n)
	out := make([]float32, len(xs))
	for i, x := range xs {
		out[i] = float32(x / n)
	}
	return out
}

func clip(tag int16) []int16 {
	p := make([]int16, 3*sampleRate)
	p[0] = tag
	return p
}

func TestLabelerWithEnrolledVoices(t *testing.T) {
	emb := fakeEmbed{dirs: map[int16][]float32{
		1: unitv(1, 0.1, 0), // Ayşe, slightly off her profile
		2: unitv(0, 1, 0.1), // Mehmet
		3: unitv(0, 0, 1),   // stranger / TV
	}}
	reg := &Registry{path: filepath.Join(t.TempDir(), "s.json")}
	if err := reg.Enroll("Ayşe", [][]float32{unitv(1, 0, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Enroll("Mehmet", [][]float32{unitv(0, 1, 0)}); err != nil {
		t.Fatal(err)
	}
	l := &Labeler{Emb: emb, Reg: reg}
	for tag, want := range map[int16]string{1: "Ayşe", 2: "Mehmet", 3: UnknownName} {
		if got, _ := l.Label(clip(tag)); got != want {
			t.Errorf("tag %d labelled %q, want %q", tag, got, want)
		}
	}
	if name, _ := l.Label(make([]int16, sampleRate)); name != "" {
		t.Errorf("1 s of audio must not be labelled, got %q", name)
	}
}

func TestLabelerFallbackNumbersVoices(t *testing.T) {
	emb := fakeEmbed{dirs: map[int16][]float32{1: unitv(1, 0), 2: unitv(0, 1), 3: unitv(1, 0.1)}}
	l := &Labeler{Emb: emb, Reg: &Registry{}}
	var got []string
	for _, tag := range []int16{1, 2, 3, 2} {
		n, _ := l.Label(clip(tag))
		got = append(got, n)
	}
	want := []string{"Speaker 1", "Speaker 2", "Speaker 1", "Speaker 2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("labels %v, want %v", got, want)
		}
	}
}

func TestRegistryPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "speakers.json")
	r, err := LoadRegistry(path)
	if err != nil || r.Len() != 0 {
		t.Fatalf("empty registry: %v %d", err, r.Len())
	}
	_ = r.Enroll("Ali", [][]float32{unitv(1, 0), unitv(0.9, 0.1)})
	_ = r.Enroll("ali", [][]float32{unitv(0, 1)}) // same name (case-insensitive) replaces
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	r2, err := LoadRegistry(path)
	if err != nil || r2.Len() != 1 {
		t.Fatalf("reload: %v len=%d", err, r2.Len())
	}
	if name, score := r2.Match(unitv(0, 1)); name != "ali" || score < 0.99 {
		t.Errorf("match = %q %.2f", name, score)
	}
	if !r2.Remove("ALI") || r2.Remove("ali") {
		t.Error("Remove should succeed once, case-insensitively")
	}
}

// contentEmbed maps the value of the sample in the middle of the window to a
// voice, so synthetic "speakers" can occupy stretches of a buffer.
type contentEmbed struct{ dirs map[int16][]float32 }

func (c contentEmbed) Embed(pcm []int16) ([]float32, error) { return c.dirs[pcm[len(pcm)/2]], nil }

func stretch(v int16, sec float64) []int16 {
	p := make([]int16, int(sec*sampleRate))
	for i := range p {
		p[i] = v
	}
	return p
}

func TestTurnsFindsSpeakerChange(t *testing.T) {
	emb := contentEmbed{dirs: map[int16][]float32{1: unitv(1, 0, 0), 2: unitv(0, 1, 0), 3: unitv(0, 0, 1)}}
	reg := &Registry{}
	_ = reg.Enroll("Ayşe", [][]float32{unitv(1, 0, 0)})
	_ = reg.Enroll("Mehmet", [][]float32{unitv(0, 1, 0)})
	l := &Labeler{Emb: emb, Reg: reg}

	var pcm []int16
	pcm = append(pcm, stretch(1, 6)...) // Ayşe 0-6 s
	pcm = append(pcm, stretch(2, 5)...) // Mehmet 6-11 s
	turns := l.Turns(pcm)
	if len(turns) != 2 {
		t.Fatalf("want 2 turns, got %+v", turns)
	}
	if turns[0].Name != "Ayşe" || turns[1].Name != "Mehmet" {
		t.Errorf("names: %+v", turns)
	}
	if b := turns[0].End.Seconds(); b < 5.0 || b > 7.0 {
		t.Errorf("boundary at %.2f s, want about 6 s", b)
	}
	if turns[0].End != turns[1].Start || turns[1].End.Seconds() < 10.9 {
		t.Errorf("turns must tile the utterance: %+v", turns)
	}

	// One speaker, a brief interjection, and short audio: no split.
	if got := l.Turns(stretch(1, 9)); got != nil {
		t.Errorf("single speaker must not split: %+v", got)
	}
	blip := append(append(stretch(1, 4), stretch(2, 1)...), stretch(1, 4)...)
	if got := l.Turns(blip); got != nil {
		t.Errorf("a 1 s interjection should be absorbed: %+v", got)
	}
	if got := l.Turns(stretch(2, 3)); got != nil {
		t.Errorf("short audio must not split: %+v", got)
	}
	// Without enrolled voices there are no stable labels to split on.
	if got := (&Labeler{Emb: emb, Reg: &Registry{}}).Turns(pcm); got != nil {
		t.Errorf("no enrolled voices: %+v", got)
	}
}
