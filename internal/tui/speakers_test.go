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

var (
	keyUp    = tea.KeyMsg{Type: tea.KeyUp}
	keyDown  = tea.KeyMsg{Type: tea.KeyDown}
	keyLeft  = tea.KeyMsg{Type: tea.KeyLeft}
	keyRight = tea.KeyMsg{Type: tea.KeyRight}
	keyEnter = tea.KeyMsg{Type: tea.KeyEnter}
	keyEsc   = tea.KeyMsg{Type: tea.KeyEsc}
)

// moveTo puts the cursor on the first row of kind.
func moveTo(t *testing.T, m SpeakersModel, kind spRowKind) SpeakersModel {
	t.Helper()
	for i, r := range m.rows() {
		if r.kind == kind {
			m.cursor = i
			return m
		}
	}
	t.Fatalf("no row of kind %d in %+v", kind, m.rows())
	return m
}

func TestSpeakersAddAsksForRecording(t *testing.T) {
	m := moveTo(t, NewSpeakersModel(seeded(t)), rowAdd)
	m = press(m, keyEnter, runes("A"), runes("y"), runes("s"), runes("e"),
		tea.KeyMsg{Type: tea.KeyBackspace}, runes("e"), keyEnter)
	if a, name := m.Action(); a != spActionRecord || name != "Ayse" {
		t.Fatalf("action = %v %q, want record Ayse", a, name)
	}
}

func TestSpeakersEmptyNameDoesNothing(t *testing.T) {
	m := moveTo(t, NewSpeakersModel(seeded(t)), rowAdd)
	m = press(m, keyEnter, keyEnter)
	if a, _ := m.Action(); a != spActionNone {
		t.Fatal("empty name must not start a recording")
	}
}

func TestSpeakersArrowsReachEveryRow(t *testing.T) {
	m := NewSpeakersModel(seeded(t, "Ali"))
	n := len(m.rows())
	for i := 0; i < n+3; i++ {
		m = press(m, keyDown)
	}
	if m.cursor != n-1 || m.rows()[m.cursor].kind != rowBack {
		t.Fatalf("down stops at %d of %d", m.cursor, n)
	}
	m = press(m, keyEnter)
	if !m.Done() {
		t.Fatal("Back must leave the screen")
	}
}

func TestSpeakersReRecordSelected(t *testing.T) {
	m := press(NewSpeakersModel(seeded(t, "Ali", "Veli")), keyDown, keyEnter, keyEnter)
	if a, name := m.Action(); a != spActionRecord || name != "Veli" {
		t.Fatalf("action = %v %q", a, name)
	}
}

func TestSpeakersForgetNeedsConfirmation(t *testing.T) {
	path := seeded(t, "Ali", "Veli")
	// Ali → Forget → Cancel (the default row) keeps him.
	m := press(NewSpeakersModel(path), keyEnter, keyDown, keyEnter, keyEnter)
	if m.reg.Len() != 2 || m.state != spVoice {
		t.Fatalf("cancel must keep the voice and return to its menu: %d voices, state %d", m.reg.Len(), m.state)
	}
	m = press(m, keyEsc)
	m = press(m, keyEnter, keyDown, keyEnter, keyDown, keyEnter)
	if m.reg.Len() != 1 || m.reg.Profiles[0].Name != "Veli" || m.state != spList {
		t.Fatalf("profiles after forget: %+v", m.reg.Profiles)
	}
	again, _ := speaker.LoadRegistry(path)
	if again.Len() != 1 {
		t.Fatalf("forget was not saved: %d", again.Len())
	}
}

func TestSpeakersViewListsVoices(t *testing.T) {
	v := NewSpeakersModel(seeded(t, "Ali")).View()
	if !contains(v, "Ali") || !contains(v, "Add a voice") || !contains(v, "↑/↓ navigate") {
		t.Errorf("view missing content:\n%s", v)
	}
	if !contains(NewSpeakersModel(seeded(t)).View(), "No voices enrolled") {
		t.Error("empty view should explain how to start")
	}
}

func TestSpeakersNamingShowsReadingTextAndTabSwitchesLanguage(t *testing.T) {
	m := NewSpeakersModel(seeded(t))
	m.lang = "en"
	m = press(moveTo(t, m, rowAdd), keyEnter)
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
	m = press(m, keyRight)
	if m.Lang() != "en" {
		t.Fatalf("tab should cycle back, got %q", m.Lang())
	}
}

func TestSpeakersNameKeepsSingleSpaces(t *testing.T) {
	m := moveTo(t, NewSpeakersModel(seeded(t)), rowAdd)
	m = press(m, keyEnter, runes("Test"), tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}, runes("Kişi"))
	if m.input != "Test Kişi" {
		t.Errorf("name = %q", m.input)
	}
}
