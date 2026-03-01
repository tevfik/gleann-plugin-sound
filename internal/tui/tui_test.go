package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tevfik/gleann-sound/internal/config"
)

// ── Home Model Tests ───────────────────────────────────────────

func TestHomeModelInit(t *testing.T) {
	m := NewHomeModel()
	if m.Quitting() {
		t.Error("new HomeModel should not be quitting")
	}
	if m.Chosen() != ScreenHome {
		t.Errorf("initial screen = %v, want ScreenHome", m.Chosen())
	}
}

func TestHomeModelNavigation(t *testing.T) {
	m := NewHomeModel()

	// First item selected by default.
	view := m.View()
	if view == "" {
		t.Error("View() returned empty string")
	}

	// Navigate down.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(HomeModel)

	// Navigate up.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(HomeModel)

	// Quit with q.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(HomeModel)
	if !m.Quitting() {
		t.Error("pressing q should set quitting")
	}
	if cmd == nil {
		t.Error("quit should return a command")
	}
}

func TestHomeModelSelectSetup(t *testing.T) {
	// Use explicit nil config so menu is pre-setup (Setup + Quit).
	m := NewHomeModelWithConfig(nil)
	// Press enter on first item (Setup).
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(HomeModel)
	if m.Chosen() != ScreenSetup {
		t.Errorf("chosen = %v, want ScreenSetup", m.Chosen())
	}
	if cmd == nil {
		t.Error("enter should return a quit command")
	}
}

// ── Setup Model Tests ──────────────────────────────────────────

func TestSetupModelInit(t *testing.T) {
	m := NewSetupModel(nil)
	if m.Cancelled() {
		t.Error("new SetupModel should not be cancelled")
	}
	if m.Done() {
		t.Error("new SetupModel should not be done")
	}
	if m.phase != phaseModelSelect {
		t.Errorf("initial phase = %v, want phaseModelSelect", m.phase)
	}
}

func TestSetupModelWithExistingConfig(t *testing.T) {
	cfg := &config.Config{
		Language: "tr",
		Hotkey:   "ctrl+alt+space",
	}
	m := NewSetupModel(cfg)

	// Language cursor should match "tr".
	if m.langCursor == 0 {
		t.Error("langCursor should not be 0 when config has language=tr")
	}

	// Hotkey should be pre-filled from config.
	if m.hotkeyPresets[m.hotkeyCursor].value != "ctrl+alt+space" {
		t.Errorf("hotkey preset value = %q, want ctrl+alt+space", m.hotkeyPresets[m.hotkeyCursor].value)
	}
}

func TestSetupModelCancel(t *testing.T) {
	m := NewSetupModel(nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	sm := updated.(SetupModel)
	if !sm.Cancelled() {
		t.Error("ctrl+c should cancel setup")
	}
}

func TestSetupModelNavigation(t *testing.T) {
	m := NewSetupModel(nil)

	// Navigate model list.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	sm := updated.(SetupModel)
	if sm.modelCursor != 1 {
		t.Errorf("modelCursor = %d, want 1", sm.modelCursor)
	}

	updated, _ = sm.Update(tea.KeyMsg{Type: tea.KeyUp})
	sm = updated.(SetupModel)
	if sm.modelCursor != 0 {
		t.Errorf("modelCursor = %d, want 0", sm.modelCursor)
	}
}

func TestSetupModelToggle(t *testing.T) {
	m := NewSetupModel(nil)

	// Toggle first model.
	wasSel := m.selected[0]
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	sm := updated.(SetupModel)
	if sm.selected[0] == wasSel {
		t.Error("space should toggle model selection")
	}
}

func TestSetupModelView(t *testing.T) {
	m := NewSetupModel(nil)
	v := m.View()
	if v == "" {
		t.Error("View() returned empty")
	}
}

// ── Install Model Tests ────────────────────────────────────────

func TestInstallModelInit(t *testing.T) {
	m := NewInstallModel()
	if m.Done() {
		t.Error("new InstallModel should not be done")
	}
	if m.Cancelled() {
		t.Error("new InstallModel should not be cancelled")
	}
	if m.uninstall {
		t.Error("NewInstallModel should not be uninstall")
	}
	if len(m.options) != 4 {
		t.Errorf("install options = %d, want 4", len(m.options))
	}
}

func TestUninstallModelInit(t *testing.T) {
	m := NewUninstallModel()
	if !m.uninstall {
		t.Error("NewUninstallModel should be uninstall")
	}
	if len(m.options) != 5 {
		t.Errorf("uninstall options = %d, want 5", len(m.options))
	}
}

func TestInstallModelNavigation(t *testing.T) {
	m := NewInstallModel()

	// Navigate down.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	im := updated.(InstallModel)
	if im.cursor != 1 {
		t.Errorf("cursor = %d, want 1", im.cursor)
	}

	// Toggle.
	was := im.options[1].selected
	updated, _ = im.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	im = updated.(InstallModel)
	if im.options[1].selected == was {
		t.Error("space should toggle option")
	}
}

func TestInstallModelCancel(t *testing.T) {
	m := NewInstallModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	im := updated.(InstallModel)
	if !im.Cancelled() {
		t.Error("esc should cancel install")
	}
}

func TestInstallModelView(t *testing.T) {
	m := NewInstallModel()
	v := m.View()
	if v == "" {
		t.Error("View() returned empty")
	}
}

// ── Shell Completion Tests ─────────────────────────────────────

func TestBashCompletion(t *testing.T) {
	c := bashCompletion()
	if c == "" {
		t.Error("bash completion is empty")
	}
	for _, word := range []string{"gleann-sound", "transcribe", "listen", "serve", "dictate", "tui"} {
		if !contains(c, word) {
			t.Errorf("bash completion missing %q", word)
		}
	}
}

func TestZshCompletion(t *testing.T) {
	c := zshCompletion()
	if c == "" {
		t.Error("zsh completion is empty")
	}
	for _, word := range []string{"gleann-sound", "transcribe", "listen", "serve", "dictate", "tui"} {
		if !contains(c, word) {
			t.Errorf("zsh completion missing %q", word)
		}
	}
}

func TestFishCompletion(t *testing.T) {
	c := fishCompletion()
	if c == "" {
		t.Error("fish completion is empty")
	}
	for _, word := range []string{"gleann-sound", "transcribe", "listen", "serve", "dictate", "tui"} {
		if !contains(c, word) {
			t.Errorf("fish completion missing %q", word)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ── Home Model Config-Aware Tests ──────────────────────────────

func TestHomeModelPreSetup(t *testing.T) {
	// Without completed config, should show only Setup + Quit.
	m := NewHomeModelWithConfig(nil)
	if len(m.items) != 2 {
		t.Errorf("pre-setup menu items = %d, want 2", len(m.items))
	}
	if m.items[0].title != "Setup" {
		t.Errorf("first item = %q, want Setup", m.items[0].title)
	}
	if m.items[1].title != "Quit" {
		t.Errorf("second item = %q, want Quit", m.items[1].title)
	}
}

func TestHomeModelPostSetup(t *testing.T) {
	// With completed config, should show full menu.
	cfg := &config.Config{Completed: true, DefaultModel: "test", Language: "tr", Hotkey: "ctrl+space"}
	m := NewHomeModelWithConfig(cfg)
	if len(m.items) != 8 {
		t.Errorf("post-setup menu items = %d, want 8", len(m.items))
	}
	// First should be Dictate.
	if m.items[0].title != "Dictate" {
		t.Errorf("first item = %q, want Dictate", m.items[0].title)
	}
	// Config should be accessible.
	if m.Config() == nil {
		t.Error("Config() should not be nil")
	}
	if m.Config().Language != "tr" {
		t.Errorf("Config().Language = %q, want tr", m.Config().Language)
	}
}

func TestHomeModelPostSetupSelectDictate(t *testing.T) {
	cfg := &config.Config{Completed: true}
	m := NewHomeModelWithConfig(cfg)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h := updated.(HomeModel)
	if h.Chosen() != ScreenDictate {
		t.Errorf("chosen = %v, want ScreenDictate", h.Chosen())
	}
	if cmd == nil {
		t.Error("enter should return a command")
	}
}

func TestHomeModelStatusLine(t *testing.T) {
	cfg := &config.Config{Completed: true, Language: "tr", Hotkey: "ctrl+space",
		Models: []config.ModelEntry{{Name: "small"}}}
	m := NewHomeModelWithConfig(cfg)
	v := m.View()
	if !containsStr(v, "small") {
		t.Error("View should show model name in status line")
	}
	if !containsStr(v, "tr") {
		t.Error("View should show language in status line")
	}
	if !containsStr(v, "ctrl+space") {
		t.Error("View should show hotkey in status line")
	}
}

// ── Hotkey Preset Tests ────────────────────────────────────────

func TestHotkeyPresetSelection(t *testing.T) {
	m := NewSetupModel(nil)
	m.phase = phaseHotkey

	// Default cursor should be at first preset (ctrl+shift+space).
	if m.hotkeyCursor != 0 {
		t.Errorf("hotkeyCursor = %d, want 0", m.hotkeyCursor)
	}

	// Navigate down to second preset.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	sm := updated.(SetupModel)
	if sm.hotkeyCursor != 1 {
		t.Errorf("hotkeyCursor = %d, want 1", sm.hotkeyCursor)
	}

	// Select it (enter) — should advance to gRPC phase.
	updated, _ = sm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sm = updated.(SetupModel)
	if sm.phase != phaseGRPC {
		t.Errorf("phase = %d, want phaseGRPC", sm.phase)
	}
	if sm.selectedHotkey() != "ctrl+alt+space" {
		t.Errorf("selectedHotkey = %q, want ctrl+alt+space", sm.selectedHotkey())
	}
}

func TestHotkeyCustomInput(t *testing.T) {
	m := NewSetupModel(nil)
	m.phase = phaseHotkey

	// Navigate to last item (Custom…).
	for i := 0; i < len(defaultHotkeyPresets)-1; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(SetupModel)
	}

	// Select Custom.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sm := updated.(SetupModel)
	if !sm.hotkeyCustom {
		t.Error("selecting Custom should enable hotkeyCustom")
	}

	// Type "f5".
	updated, _ = sm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	sm = updated.(SetupModel)
	updated, _ = sm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	sm = updated.(SetupModel)
	if sm.hotkeyInput != "f5" {
		t.Errorf("hotkeyInput = %q, want f5", sm.hotkeyInput)
	}

	// Backspace should remove last char.
	updated, _ = sm.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	sm = updated.(SetupModel)
	if sm.hotkeyInput != "f" {
		t.Errorf("hotkeyInput after backspace = %q, want f", sm.hotkeyInput)
	}

	// Backspace on empty should go back to list.
	updated, _ = sm.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	sm = updated.(SetupModel)
	updated, _ = sm.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	sm = updated.(SetupModel)
	if sm.hotkeyCustom {
		t.Error("backspace on empty input should exit custom mode")
	}
}

func TestHotkeyViewPresets(t *testing.T) {
	m := NewSetupModel(nil)
	m.phase = phaseHotkey
	v := m.View()
	if !containsStr(v, "Ctrl+Shift+Space") {
		t.Error("hotkey view should show preset options")
	}
	if !containsStr(v, "Custom") {
		t.Error("hotkey view should show Custom option")
	}
}

func TestHotkeyViewCustomInput(t *testing.T) {
	m := NewSetupModel(nil)
	m.phase = phaseHotkey
	m.hotkeyCustom = true
	m.hotkeyInput = "ctrl+f5"
	v := m.View()
	if !containsStr(v, "ctrl+f5") {
		t.Error("hotkey view should show custom input")
	}
}

// ── Style Tests ────────────────────────────────────────────────

func TestLogo(t *testing.T) {
	logo := Logo()
	if logo == "" {
		t.Error("Logo() returned empty")
	}
}
