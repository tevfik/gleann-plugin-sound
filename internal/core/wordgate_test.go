package core

import "testing"

func TestWordGate(t *testing.T) {
	var g WordGate
	steps := []struct {
		full           string
		final          bool
		delta, pending string
	}{
		{"", false, "", ""},
		{"Gün", false, "", "Gün"},
		{"Günde sa", false, "Günde", "sa"},
		{"Günde sadece on", false, " sadece", "on"},
		{"Günde sadece on seki", false, " on", "seki"},
		{"Günde sadece on sekiz.", true, " sekiz.", ""},
	}
	var joined string
	for i, s := range steps {
		d, p := g.Update(s.full, s.final)
		joined += d
		if d != s.delta || p != s.pending {
			t.Errorf("step %d: got (%q,%q), want (%q,%q)", i, d, p, s.delta, s.pending)
		}
	}
	if joined != "Günde sadece on sekiz." {
		t.Errorf("deltas add up to %q", joined)
	}
	g.Reset()
	if d, _ := g.Update("Yeni", true); d != "Yeni" {
		t.Errorf("after Reset got %q", d)
	}
}
