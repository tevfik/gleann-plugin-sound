package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/speaker"
)

type speakersState int

const (
	spList speakersState = iota
	spNaming
	spConfirmDelete
)

// speakersAction is what the screen asks its caller to do after it quits.
type speakersAction int

const (
	spActionNone speakersAction = iota
	// spActionRecord: run `speakers record <name>` (the terminal goes to it).
	spActionRecord
)

// SpeakersModel manages enrolled voices: list, add or re-record (the actual
// recording is done by `speakers record`), and forget.
type SpeakersModel struct {
	reg    *speaker.Registry
	path   string
	state  speakersState
	cursor int
	input  string
	notice string
	err    error

	action speakersAction
	name   string // for spActionRecord
	lang   string // language of the text read aloud (tr, en)
	done   bool
}

// NewSpeakersModel loads the registry at path.
func NewSpeakersModel(path string) SpeakersModel {
	lang := ""
	if cfg := config.Load(); cfg != nil {
		lang = cfg.Language
	}
	m := SpeakersModel{path: path, lang: speaker.NormalizeLanguage(lang)}
	m.reload()
	return m
}

func (m *SpeakersModel) reload() {
	reg, err := speaker.LoadRegistry(m.path)
	if err != nil {
		m.err = err
		reg = &speaker.Registry{}
	}
	m.reg = reg
	if m.cursor >= m.reg.Len() {
		m.cursor = max(0, m.reg.Len()-1)
	}
}

func (m SpeakersModel) Init() tea.Cmd { return nil }

// Action reports what to do after the screen has quit.
func (m SpeakersModel) Action() (speakersAction, string) { return m.action, m.name }

// Lang is the language of the reading text the user chose.
func (m SpeakersModel) Lang() string { return m.lang }

// Done reports that the user left the screen.
func (m SpeakersModel) Done() bool { return m.done }

func (m SpeakersModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if key.String() == "ctrl+c" {
		m.done = true
		return m, tea.Quit
	}
	switch m.state {
	case spNaming:
		return m.updateNaming(key)
	case spConfirmDelete:
		if key.String() == "y" {
			if m.cursor < m.reg.Len() {
				name := m.reg.Profiles[m.cursor].Name
				m.reg.Remove(name)
				if err := m.reg.Save(); err != nil {
					m.err = err
				} else {
					m.notice = fmt.Sprintf("Forgot %s.", name)
				}
				m.reload()
			}
		}
		m.state = spList
		return m, nil
	}

	switch key.String() {
	case "esc", "q":
		m.done = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < m.reg.Len()-1 {
			m.cursor++
		}
	case "a":
		m.state, m.input, m.notice, m.err = spNaming, "", "", nil
	case "enter", "r":
		// Re-record the selected voice.
		if m.cursor < m.reg.Len() {
			m.action, m.name = spActionRecord, m.reg.Profiles[m.cursor].Name
			return m, tea.Quit
		}
	case "d", "x":
		if m.cursor < m.reg.Len() {
			m.state = spConfirmDelete
		}
	}
	return m, nil
}

func (m SpeakersModel) updateNaming(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.state = spList
	case "tab":
		m.lang = speaker.ReadingLanguages[(indexOf(speaker.ReadingLanguages, m.lang)+1)%len(speaker.ReadingLanguages)]
	case "enter":
		if name := strings.TrimSpace(m.input); name != "" {
			m.action, m.name = spActionRecord, name
			return m, tea.Quit
		}
	case "backspace":
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	default:
		if key.Type == tea.KeyRunes || key.Type == tea.KeySpace {
			m.input += string(key.Runes)
			if key.Type == tea.KeySpace {
				m.input += " "
			}
		}
	}
	return m, nil
}

func (m SpeakersModel) View() string {
	var b strings.Builder
	b.WriteString("\n" + TitleStyle.Render("  🗣  Speakers") + "\n")
	b.WriteString(SubtitleStyle.Render("  Voices that 'listen' and 'plugin-serve' recognise by name") + "\n\n")

	if m.reg.Len() == 0 {
		b.WriteString(DescStyle.Render("No voices enrolled yet. Press a to record the first one.") + "\n")
	}
	for i, p := range m.reg.Profiles {
		line := fmt.Sprintf("%s  (%d samples)", p.Name, p.Samples)
		if i == m.cursor && m.state == spList {
			b.WriteString(ActiveItemStyle.Render("▸ "+line) + "\n")
		} else {
			b.WriteString(NormalItemStyle.Render("  "+line) + "\n")
		}
	}

	b.WriteString("\n")
	switch m.state {
	case spNaming:
		b.WriteString(lipgloss.NewStyle().Foreground(ColorAccent).Render("  Name: "+m.input+"█") + "\n\n")
		b.WriteString(DescStyle.Render("  You will read this aloud ("+speaker.LanguageName(m.lang)+"):") + "\n")
		b.WriteString(lipgloss.NewStyle().Foreground(ColorFg).Width(76).PaddingLeft(2).Render(speaker.ReadingText(m.lang)) + "\n\n")
		b.WriteString(StatusBarStyle.Render("  tab language • enter record 25 s • esc cancel") + "\n")
	case spConfirmDelete:
		b.WriteString(lipgloss.NewStyle().Foreground(ColorError).Render(
			fmt.Sprintf("  Forget %s? (y/N)", m.reg.Profiles[m.cursor].Name)) + "\n")
	default:
		if m.notice != "" {
			b.WriteString(lipgloss.NewStyle().Foreground(ColorSuccess).Render("  "+m.notice) + "\n")
		}
		if m.err != nil {
			b.WriteString(ErrorBadge.Render("  "+m.err.Error()) + "\n")
		}
		b.WriteString(StatusBarStyle.Render("  a add voice • enter re-record • d forget • q back") + "\n")
		b.WriteString(DescStyle.Render("  Record in the room where you will use it, with no one else talking. You read a short text (Türkçe or English).") + "\n")
	}
	return b.String()
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return 0
}
