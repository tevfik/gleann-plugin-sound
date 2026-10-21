package core

import "testing"

func TestCorrector(t *testing.T) {
	c := NewCorrector(map[string]string{
		"gılan":      "gleann",
		"kal isiyor": "çalışıyor",
	})
	cases := map[string]string{
		"Merhaba Gılan, sesli":  "Merhaba Gleann, sesli",
		"gılan.":                "gleann.",
		"başarıyla Kal  isiyor": "başarıyla Çalışıyor",
		"hiçbir şey değişmez":   "hiçbir şey değişmez",
		"  boşluklar  korunur ": "  boşluklar  korunur ",
		"":                      "",
		" gılan":                " gleann", // streamed delta
	}
	for in, want := range cases {
		if got := c.Apply(in); got != want {
			t.Errorf("Apply(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCorrector_NilIsNoop(t *testing.T) {
	var c *Corrector
	if got := c.Apply("aynı"); got != "aynı" {
		t.Errorf("nil corrector changed text: %q", got)
	}
	if got := NewCorrector(nil).Apply("aynı"); got != "aynı" {
		t.Errorf("empty corrector changed text: %q", got)
	}
}
