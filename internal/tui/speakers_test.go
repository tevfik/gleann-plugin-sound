package tui

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tevfik/gleann-plugin-sound/internal/speaker"
)

func press(m tea.Model, keys ...tea.KeyMsg) SpeakersModel {
	for _, k := range keys {
		m, _ = m.Update(k)
	}
	return m.(SpeakersModel)
}

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func seeded(t *testing.T, names ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "speakers.json")
	reg, _ := speaker.LoadRegistry(path)
	for _, n := range names {
		if err := reg.Enroll(n, [][]float32{{1, 0}, {1, 0}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSpeakersAddAsksForRecording(t *testing.T) {
	m := press(NewSpeakersModel(seeded(t)), runes("a"), runes("A"), runes("y"), runes("s"), runes("e"),
		tea.KeyMsg{Type: tea.KeyBackspace}, runes("e"), tea.KeyMsg{Type: tea.KeyEnter})
	if a, name := m.Action(); a != spActionRecord || name != "Ayse" {
		t.Fatalf("action = %v %q, want record Ayse", a, name)
	}
}

func TestSpeakersEmptyNameDoesNothing(t *testing.T) {
	m := press(NewSpeakersModel(seeded(t)), runes("a"), tea.KeyMsg{Type: tea.KeyEnter})
	if a, _ := m.Action(); a != spActionNone {
		t.Fatal("empty name must not start a recording")
	}
}

func TestSpeakersReRecordSelected(t *testing.T) {
	m := press(NewSpeakersModel(seeded(t, "Ali", "Veli")), tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter})
	if a, name := m.Action(); a != spActionRecord || name != "Veli" {
		t.Fatalf("action = %v %q", a, name)
	}
}

func TestSpeakersForgetNeedsConfirmation(t *testing.T) {
	path := seeded(t, "Ali", "Veli")
	m := press(NewSpeakersModel(path), runes("d"), runes("n"))
	if m.reg.Len() != 2 {
		t.Fatal("n must keep the voice")
	}
	m = press(m, runes("d"), runes("y"))
	if m.reg.Len() != 1 || m.reg.Profiles[0].Name != "Veli" {
		t.Fatalf("profiles after forget: %+v", m.reg.Profiles)
	}
	again, _ := speaker.LoadRegistry(path)
	if again.Len() != 1 {
		t.Fatalf("forget was not saved: %d", again.Len())
	}
}

func TestSpeakersViewListsVoices(t *testing.T) {
	v := NewSpeakersModel(seeded(t, "Ali")).View()
	if !contains(v, "Ali") || !contains(v, "a add voice") {
		t.Errorf("view missing content:\n%s", v)
	}
	if !contains(NewSpeakersModel(seeded(t)).View(), "No voices enrolled") {
		t.Error("empty view should explain how to start")
	}
}

func TestSpeakersNamingShowsReadingTextAndTabSwitchesLanguage(t *testing.T) {
	m := NewSpeakersModel(seeded(t))
	m.lang = "en"
	m = press(m, runes("a"))
	if v := m.View(); !contains(v, "weather is lovely") || !contains(v, "English") {
		t.Fatalf("english text missing:\n%s", v)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if v := m.View(); !contains(v, "Bugün hava çok güzel") || !contains(v, "Türkçe") {
		t.Fatalf("turkish text missing:\n%s", v)
	}
	if m.Lang() != "tr" {
		t.Fatalf("lang = %q", m.Lang())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.Lang() != "en" {
		t.Fatalf("tab should cycle back, got %q", m.Lang())
	}
}
