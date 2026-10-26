package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/speaker"
)

// A value added to Config must be given a settings field (so the web form and
// the TUI can edit it) or be listed as internal on purpose.
func TestEveryConfigValueIsASettingOrInternal(t *testing.T) {
	if missing := config.Settings().Uncovered(); len(missing) > 0 {
		t.Errorf("config keys without a settings field: %v (add a field to Settings or list them in Internal)", missing)
	}
}

func TestSpeakerThresholdDefaultMatchesLabeler(t *testing.T) {
	if config.DefaultSpeakerThreshold != speaker.DefaultThreshold {
		t.Errorf("config says %v, the labeler uses %v", config.DefaultSpeakerThreshold, speaker.DefaultThreshold)
	}
}

func TestDictateThresholdStepsThroughTheRegistry(t *testing.T) {
	r, c := config.Settings(), &config.Config{}
	if r.Visible(c, "dictate_speaker_threshold") {
		t.Error("threshold shown while only-my-voice is off")
	}
	_ = r.Step(c, "dictate_speaker_match", 1)
	if !c.DictateSpeakerMatch || !r.Visible(c, "dictate_speaker_threshold") {
		t.Fatal("toggle")
	}
	_ = r.Step(c, "dictate_speaker_threshold", 1)
	if c.DictateThreshold() != 0.5 {
		t.Errorf("step up: %v", c.DictateThreshold())
	}
	_ = r.Step(c, "dictate_speaker_threshold", -1)
	if c.DictateSpeakerThreshold != 0 {
		t.Errorf("back on the default must store 0, got %v", c.DictateSpeakerThreshold)
	}
}

func TestModelFieldsOfferDownloadedModels(t *testing.T) {
	dir := t.TempDir()
	small, qwen := filepath.Join(dir, "ggml-small.bin"), filepath.Join(dir, "qwen.gguf")
	for _, p := range []string{small, qwen} {
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	c := &config.Config{DefaultModel: small, Models: []config.ModelEntry{
		{Name: "small", Path: small, Size: "466 MB"},
		{Name: config.RecommendedRefineModel, Path: qwen, Size: "2.2 GB"},
	}}
	form := config.Settings().Describe(c)
	opts := map[string][]string{}
	for _, f := range form.Fields {
		for _, o := range f.Options {
			opts[f.Key] = append(opts[f.Key], o.Value)
		}
	}
	if !reflect.DeepEqual(opts["default_model"], []string{small}) {
		t.Errorf("model choices: %v", opts["default_model"])
	}
	if !reflect.DeepEqual(opts["refine_model"], []string{"", qwen}) {
		t.Errorf("refine choices: %v", opts["refine_model"])
	}
	r := config.Settings()
	if err := r.Step(c, "refine_model", 1); err != nil || c.RefineModel != qwen {
		t.Errorf("→ on refine model: %v %q", err, c.RefineModel)
	}
	if err := r.Set(c, "default_model", "/no/such/model"); err == nil {
		t.Error("a custom model path that does not exist was accepted")
	}
}
