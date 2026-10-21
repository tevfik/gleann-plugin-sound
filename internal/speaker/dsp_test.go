package speaker

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// golden values come from the NeMo-style numpy/librosa reference
// (testdata/fbank_golden.json: 20000 samples of a three-tone signal).
func TestFeaturesMatchReference(t *testing.T) {
	raw, err := os.ReadFile("testdata/fbank_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Frames int `json:"frames"`
		Values []struct {
			Mel   int     `json:"mel"`
			Frame int     `json:"frame"`
			V     float64 `json:"v"`
		} `json:"values"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	x := make([]float32, 20000)
	for i := range x {
		ti := float64(i)
		x[i] = float32(0.3*math.Sin(2*math.Pi*220*ti/16000) +
			0.2*math.Sin(2*math.Pi*1375*ti/16000+1) +
			0.1*math.Sin(2*math.Pi*3100*ti/16000))
	}
	f := Features(x)
	if len(f) != numMels || len(f[0]) != g.Frames {
		t.Fatalf("shape %dx%d, want %dx%d", len(f), len(f[0]), numMels, g.Frames)
	}
	for _, v := range g.Values {
		if d := math.Abs(float64(f[v.Mel][v.Frame]) - v.V); d > 2e-3 {
			t.Errorf("mel %d frame %d: got %.5f want %.5f (diff %.5f)", v.Mel, v.Frame, f[v.Mel][v.Frame], v.V, d)
		}
	}
}

func TestFeaturesTooShort(t *testing.T) {
	if Features(make([]float32, 100)) != nil {
		t.Error("expected nil for audio shorter than one window")
	}
}
