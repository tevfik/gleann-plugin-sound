package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/speaker"
	"github.com/tevfik/gleann/modules/settings"
)

type speakersState int

const (
	spList          speakersState = iota
	spNaming                      // typing the name of a new voice
	spVoice                       // actions for one voice
	spConfirmDelete               // "forget this voice?"
)

// speakersAction is what the screen asks its caller to do after it quits.
type speakersAction int

const (
	spActionNone speakersAction = iota
	// spActionRecord: run `speakers record <name>` (the terminal goes to it).
	spActionRecord
)

// spRowKind is one kind of row in the speakers menu.
type spRowKind int

const (
	rowVoice spRowKind = iota
	rowAdd
	rowMatch
	rowThreshold
	rowBack
)

type spRow struct {
	kind  spRowKind
	voice int // index into the registry for rowVoice
}

// SpeakersModel manages enrolled voices and the dictate voice match: a menu
// moved through with the arrow keys, like the home screen. Recording is done
// by `speakers record`.
type SpeakersModel struct {
	reg    *speaker.Registry
	path   string
	state  speakersState
	cursor int // row in the main menu
	sub    int // row in the voice and confirm menus
	voice  int // voice the sub-menu is about
	input  string
	notice string
	err    error

	action speakersAction
	name   string // for spActionRecord
	lang   string // language of the text read aloud (tr, en)
	done   bool

	// The dictate voice-match settings, edited through config.Settings like
	// every other setting.
	prefs *settings.Registry[config.Config]
	cfg   *config.Config
}

const (
	keyMatch     = "dictate_speaker_match"
	keyThreshold = "dictate_speaker_threshold"
)

// matchOn reports whether dictation types only enrolled voices.
func (m SpeakersModel) matchOn() bool { return m.prefs.Value(m.cfg, keyMatch) == true }

// matchTh is the voice threshold dictation uses.
func (m SpeakersModel) matchTh() float64 { return m.cfg.DictateThreshold() }

// NewSpeakersModel loads the registry at path.
func NewSpeakersModel(path string) SpeakersModel {
	prefs := config.Settings()
	cfg, _ := prefs.Load()
	m := SpeakersModel{path: path, lang: speaker.NormalizeLanguage(cfg.Language), prefs: prefs, cfg: cfg}
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
	if n := len(m.rows()); m.cursor >= n {
		m.cursor = n - 1
	}
}

// rows lists the main menu: the voices, then the actions and settings.
func (m SpeakersModel) rows() []spRow {
	var rows []spRow
	for i := range m.reg.Profiles {
		rows = append(rows, spRow{kind: rowVoice, voice: i})
	}
	rows = append(rows, spRow{kind: rowAdd}, spRow{kind: rowMatch})
	if m.prefs.Visible(m.cfg, keyThreshold) {
		rows = append(rows, spRow{kind: rowThreshold})
	}
	return append(rows, spRow{kind: rowBack})
}

func (m SpeakersModel) Init() tea.Cmd { return nil }

// Action reports what to do after the screen has quit.
func (m SpeakersModel) Action() (speakersAction, string) { return m.action, m.name }

// Lang is the language of the reading text the user chose.
func (m SpeakersModel) Lang() string { return m.lang }

// Done reports that the user left the screen.
func (m SpeakersModel) Done() bool { return m.done }

// voiceActions are the rows of the per-voice menu.
var voiceActions = []string{"Re-record", "Forget this voice", "Back"}

// confirmActions are the rows of the "forget?" menu.
var confirmActions = []string{"Cancel", "Yes, forget"}

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
	case spVoice:
		return m.updateVoice(key)
	case spConfirmDelete:
		return m.updateConfirm(key)
	}

	rows := m.rows()
	switch key.String() {
	case "esc", "q":
		m.done = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(rows)-1 {
			m.cursor++
		}
	case "left", "right":
		if rows[m.cursor].kind == rowThreshold {
			dir := 1
			if key.String() == "left" {
				dir = -1
			}
			m.updateMatch(func(c *config.Config) error { return m.prefs.Step(c, keyThreshold, dir) })
		}
	case "enter", " ":
		switch r := rows[m.cursor]; r.kind {
		case rowVoice:
			m.state, m.voice, m.sub, m.notice, m.err = spVoice, r.voice, 0, "", nil
		case rowAdd:
			m.state, m.input, m.notice, m.err = spNaming, "", "", nil
		case rowMatch:
			m.updateMatch(func(c *config.Config) error { return m.prefs.Step(c, keyMatch, 1) })
		case rowThreshold:
			m.updateMatch(func(c *config.Config) error { return m.prefs.Reset(c, keyThreshold) })
		case rowBack:
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// moveSub moves the cursor of a sub-menu with n rows.
func (m *SpeakersModel) moveSub(key string, n int) {
	switch key {
	case "up", "k":
		if m.sub > 0 {
			m.sub--
		}
	case "down", "j":
		if m.sub < n-1 {
			m.sub++
		}
	}
}

func (m SpeakersModel) updateVoice(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.moveSub(key.String(), len(voiceActions))
	switch key.String() {
	case "esc", "left":
		m.state = spList
	case "enter", " ":
		switch m.sub {
		case 0:
			m.action, m.name = spActionRecord, m.reg.Profiles[m.voice].Name
			return m, tea.Quit
		case 1:
			m.state, m.sub = spConfirmDelete, 0
		default:
			m.state = spList
		}
	}
	return m, nil
}

func (m SpeakersModel) updateConfirm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.moveSub(key.String(), len(confirmActions))
	switch key.String() {
	case "esc", "left":
		m.state, m.sub = spVoice, 1
	case "enter", " ":
		if m.sub == 1 && m.voice < m.reg.Len() {
			name := m.reg.Profiles[m.voice].Name
			m.reg.Remove(name)
			if err := m.reg.Save(); err != nil {
				m.err = err
			} else {
				m.notice = fmt.Sprintf("Forgot %s.", name)
			}
			m.reload()
			m.state = spList
			return m, nil
		}
		m.state, m.sub = spVoice, 1
	}
	return m, nil
}

// restartDictate is replaced in tests so they never touch the real daemon.
var restartDictate = RestartDaemon

// updateMatch changes the dictate voice-match settings, saves them and
// restarts a running dictate daemon so they take effect.
func (m *SpeakersModel) updateMatch(change func(*config.Config) error) {
	cfg := config.Load()
	if cfg == nil {
		m.err = fmt.Errorf("no configuration yet: run Setup first")
		return
	}
	if err := change(cfg); err != nil {
		m.err = err
		return
	}
	if err := config.Save(cfg); err != nil {
		m.err = err
		return
	}
	m.err, m.cfg = nil, cfg
	switch restarted, err := restartDictate(); {
	case err != nil:
		m.notice = fmt.Sprintf("Saved, but restarting the dictate daemon failed (%v): restart it from the Daemon menu.", err)
	case restarted:
		m.notice = "Saved. The dictate daemon was restarted and uses the new setting."
	default:
		m.notice = "Saved. It applies the next time dictate starts."
	}
}

func (m SpeakersModel) updateNaming(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.state = spList
	case "tab", "left", "right":
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
		switch key.Type {
		case tea.KeyRunes:
			m.input += string(key.Runes)
		case tea.KeySpace: // its Runes already hold the space
			m.input += " "
		}
	}
	return m, nil
}

func (m SpeakersModel) View() string {
	var b strings.Builder
	b.WriteString("\n" + TitleStyle.Render("  🗣  Speakers") + "\n")
	b.WriteString(SubtitleStyle.Render("  Voices that dictation, 'listen' and 'plugin-serve' recognise by name") + "\n\n")

	switch m.state {
	case spNaming:
		b.WriteString(lipgloss.NewStyle().Foreground(ColorAccent).Render("  Name: "+m.input+"█") + "\n\n")
		b.WriteString(DescStyle.Render("  You will read this aloud ("+speaker.LanguageName(m.lang)+"):") + "\n")
		b.WriteString(lipgloss.NewStyle().Foreground(ColorFg).Width(76).PaddingLeft(2).Render(speaker.ReadingText(m.lang)) + "\n\n")
		b.WriteString(DescStyle.Render("  Record in the room where you will use it, with no one else talking.") + "\n\n")
		b.WriteString(StatusBarStyle.Render("  type the name • ←/→ or tab language • enter record 25 s • esc cancel") + "\n")
		return b.String()
	case spVoice, spConfirmDelete:
		p := m.reg.Profiles[m.voice]
		items, title := voiceActions, fmt.Sprintf("  %s  (%d samples)", p.Name, p.Samples)
		if m.state == spConfirmDelete {
			items, title = confirmActions, fmt.Sprintf("  Forget %s? Dictation and transcripts will no longer recognise this voice.", p.Name)
		}
		b.WriteString(lipgloss.NewStyle().Foreground(ColorAccent).Render(title) + "\n\n")
		for i, it := range items {
			if i == m.sub {
				b.WriteString(ActiveItemStyle.Render("▸ "+it) + "\n")
			} else {
				b.WriteString(NormalItemStyle.Render("  "+it) + "\n")
			}
		}
		b.WriteString("\n" + StatusBarStyle.Render("  ↑/↓ navigate • enter select • esc back") + "\n")
		return b.String()
	}

	for i, r := range m.rows() {
		icon, title, desc := m.rowText(r)
		if i == m.cursor {
			b.WriteString(ActiveItemStyle.Render(fmt.Sprintf("▸ %s %s", icon, title)) + "\n")
			b.WriteString(ActiveDescStyle.Render("  "+desc) + "\n")
		} else {
			b.WriteString(NormalItemStyle.Render(fmt.Sprintf("  %s %s", icon, title)) + "\n")
			b.WriteString(DescStyle.Render(desc) + "\n")
		}
	}
	b.WriteString("\n")
	if m.notice != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(ColorSuccess).Render("  "+m.notice) + "\n")
	}
	if m.err != nil {
		b.WriteString(ErrorBadge.Render("  "+m.err.Error()) + "\n")
	}
	hint := "  ↑/↓ navigate • enter select • esc back"
	if rows := m.rows(); m.cursor < len(rows) && rows[m.cursor].kind == rowThreshold {
		hint = "  ←/→ change threshold • enter reset to default • ↑/↓ navigate • esc back"
	}
	b.WriteString(StatusBarStyle.Render(hint) + "\n")
	return b.String()
}

// rowText is the icon, title and description of one menu row.
func (m SpeakersModel) rowText(r spRow) (icon, title, desc string) {
	switch r.kind {
	case rowVoice:
		p := m.reg.Profiles[r.voice]
		return "👤", p.Name, fmt.Sprintf("%d samples • re-record or forget", p.Samples)
	case rowAdd:
		desc := "Record 25 s of a new voice from the microphone"
		if m.reg.Len() == 0 {
			desc = "No voices enrolled yet: record the first one (25 s from the microphone)"
		}
		return "➕", "Add a voice", desc
	case rowMatch:
		if !m.matchOn() {
			return "🔓", "Dictate: only my voice  [off]", "Dictation types every voice it hears. Enter to type only enrolled voices."
		}
		desc := "Dictation types only enrolled voices, after each pause. Enter to turn off."
		if m.reg.Len() == 0 {
			desc = "No voice enrolled yet: nothing is filtered until you add one."
		}
		return "🔒", "Dictate: only my voice  [on]", desc
	case rowThreshold:
		val := fmt.Sprintf("◂ %.2f ▸", m.matchTh())
		if m.prefs.IsDefault(m.cfg, keyThreshold) {
			val += "  (default)"
		}
		return "🎚 ", "Match threshold  " + val,
			"Others still typed: raise it. Your own words dropped: lower it. The dictate log shows each score."
	default:
		return "← ", "Back", "Return to the main menu"
	}
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return 0
}
