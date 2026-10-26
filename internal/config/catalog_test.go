package config

import (
	"os"
	"testing"
)

func TestCatalogListsEveryModelAndRegistersDownloads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = os.MkdirAll(ModelsDir(), 0o755)
	_ = os.WriteFile(ModelPath("ggml-tiny-q5_1.bin"), []byte("x"), 0o644)

	byID := map[string]bool{}
	groups := map[string]bool{}
	for _, m := range Catalog().List() {
		byID[m.ID] = m.Installed
		groups[m.Group] = true
	}
	if len(byID) < len(AllAvailableModels()) {
		t.Fatalf("catalog has %d models, want all %d", len(byID), len(AllAvailableModels()))
	}
	if !byID["tiny-q5_1"] || byID["small"] {
		t.Errorf("installed flags: tiny-q5_1=%v small=%v", byID["tiny-q5_1"], byID["small"])
	}
	for _, g := range []string{"Whisper (whisper.cpp)", "Refiner (checks each utterance)", "Nemotron (live)", "Voice activity detection"} {
		if !groups[g] {
			t.Errorf("no group %q in %v", g, groups)
		}
	}

	m := FindModel("tiny-q5_1")
	if err := RegisterModel(*m); err != nil {
		t.Fatal(err)
	}
	_ = RegisterModel(*m) // twice: listed once
	_ = RegisterModel(SileroVADModel())
	if c := Load(); len(c.Models) != 1 || c.Models[0].Path != ModelPath("ggml-tiny-q5_1.bin") {
		t.Errorf("models after registering: %+v", c.Models)
	}
}
