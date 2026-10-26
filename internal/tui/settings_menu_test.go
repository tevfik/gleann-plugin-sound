package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
)

func pressSettings(m tea.Model, keys ...tea.KeyMsg) SettingsModel {
	for _, k := range keys {
		m, _ = m.Update(k)
	}
	return m.(SettingsModel)
}

func settingsAt(t *testing.T, cfg *config.Config, key string) SettingsModel {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	m := NewSettingsModel()
	for i, k := range m.rows() {
		if k == key {
			m.cursor = i
			return m
		}
	}
	t.Fatalf("no row %q in %v", key, m.rows())
	return m
}

func TestSettingsMenuShowsEveryVisibleField(t *testing.T) {
	m := settingsAt(t, &config.Config{Completed: true}, "backend")
	rows := m.rows()
	if rows[len(rows)-1] != "" {
		t.Error("the last row must be Back")
	}
	for _, k := range rows {
		if k == "dictate_speaker_threshold" {
			t.Error("the dictate threshold is shown while only-my-voice is off")
		}
	}
	v := m.View()
	for _, want := range []string{"RECOGNITION", "Backend", "whisper.cpp", "(default)", "Speaker match threshold"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q", want)
		}
	}
}

func TestSettingsMenuArrowsChangeAndSave(t *testing.T) {
	m := settingsAt(t, &config.Config{Completed: true, InstallPath: "/keep"}, "backend")
	m = pressSettings(m, keyRight)
	if c := config.Load(); c.Backend != "onnx" || c.InstallPath != "/keep" {
		t.Fatalf("→ on backend saved %+v", c)
	}
	if m.notice == "" || m.err != nil {
		t.Errorf("notice %q err %v", m.notice, m.err)
	}
	m = pressSettings(m, tea.KeyMsg{Type: tea.KeyDelete})
	if c := config.Load(); c.Backend != "" {
		t.Errorf("del must reset to the default, got %q", c.Backend)
	}

	// Turning on only-my-voice reveals its threshold row right below it.
	m = settingsAt(t, &config.Config{Completed: true}, "dictate_speaker_match")
	m = pressSettings(m, keyEnter, keyDown, keyRight)
	if c := config.Load(); !c.DictateSpeakerMatch || c.DictateThreshold() != 0.5 {
		t.Errorf("match/threshold: %+v", c)
	}
}

func TestSettingsMenuTypedValueIsValidated(t *testing.T) {
	m := settingsAt(t, &config.Config{Completed: true, Hotkey: "f9"}, "hotkey")
	m = pressSettings(m, keyEnter)
	if !m.editing || m.input.Value() != "f9" {
		t.Fatalf("editing %v value %q", m.editing, m.input.Value())
	}
	m = pressSettings(m, tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace}, runes("ctrl + !"), keyEnter)
	if !m.editing || m.err == nil || config.Load().Hotkey != "f9" {
		t.Fatalf("an invalid hotkey was accepted: err %v, saved %q", m.err, config.Load().Hotkey)
	}
	m.input.SetValue("Ctrl+Alt+K")
	m = pressSettings(m, keyEnter)
	if m.editing || config.Load().Hotkey != "ctrl+alt+k" {
		t.Errorf("hotkey not saved: %q", config.Load().Hotkey)
	}

	// A required model cannot be reset away, and a path must exist.
	model := filepath.Join(t.TempDir(), "m.bin")
	_ = os.WriteFile(model, []byte("x"), 0o644)
	m = settingsAt(t, &config.Config{Completed: true, DefaultModel: model}, "default_model")
	m = pressSettings(m, tea.KeyMsg{Type: tea.KeyDelete})
	if m.err == nil || config.Load().DefaultModel != model {
		t.Errorf("the model was cleared: %v", m.err)
	}
	m = pressSettings(m, keyEnter)
	m.input.SetValue("/no/such/model")
	m = pressSettings(m, keyEnter)
	if m.err == nil || config.Load().DefaultModel != model {
		t.Errorf("a missing path was accepted")
	}
	m = pressSettings(m, keyEsc)
	if m.editing {
		t.Error("esc must cancel editing")
	}
}
