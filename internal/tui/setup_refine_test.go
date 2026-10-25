package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
)

func TestRecommendedPresetPrefillsWizard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LANG", "tr_TR.UTF-8")
	m := NewSetupModel(nil)
	m.presetCursor = 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sm := updated.(SetupModel)

	if got := sm.backendOptions[sm.backendCursor]; got != "tc" {
		t.Errorf("backend = %q, want tc", got)
	}
	if got := sm.languages[sm.langCursor].code; got != "tr" {
		t.Errorf("language = %q, want tr", got)
	}
	want := map[string]bool{"nemotron-3.5-asr-q8-gguf": true, config.RecommendedRefineModel: true, "silero-vad": true}
	for i, a := range sm.available {
		if want[a.Name] && !sm.selected[i] {
			t.Errorf("%s should be preselected", a.Name)
		}
	}
	if sm.refinePath == "" {
		t.Error("the recommended preset must set a refine model")
	}
}

func TestBuildConfigKeepsUneditedSettings(t *testing.T) {
	base := &config.Config{
		Completed:           true,
		Corrections:         map[string]string{"gılan": "gleann"},
		DictateSpeakerMatch: true,
		SpeakerThreshold:    0.4,
		RefineModel:         "/models/q.gguf",
	}
	m := NewSetupModel(base)
	cfg := m.buildConfig()
	if cfg.Corrections["gılan"] != "gleann" || !cfg.DictateSpeakerMatch || cfg.SpeakerThreshold != 0.4 {
		t.Errorf("setup dropped settings it does not edit: %+v", cfg)
	}
	if cfg.RefineModel != "/models/q.gguf" {
		t.Errorf("refine model = %q, want it kept", cfg.RefineModel)
	}
}

func TestRefineStepOffAndSkippedForBatchBackends(t *testing.T) {
	m := NewSetupModel(&config.Config{Completed: true, Backend: "tc", RefineModel: "/x.gguf"})
	m.phase = phaseRefine
	m.refineCursor = len(m.refineOptions()) - 1 // "Off"
	updated, _ := m.updateRefine(tea.KeyMsg{Type: tea.KeyEnter})
	if sm := updated.(SetupModel); sm.refinePath != "" {
		t.Errorf("Off left refinePath = %q", sm.refinePath)
	}

	w := NewSetupModel(nil)
	w.setBackend("whisper")
	w.phase = phaseLanguage
	w.advanceOnboarding()
	if w.phase != phaseHotkey {
		t.Errorf("whisper backend: after language phase = %v, want hotkey (refine skipped)", w.phase)
	}
}
