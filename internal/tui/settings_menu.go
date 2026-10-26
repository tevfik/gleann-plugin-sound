package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann/modules/settings"
)

// SettingsModel edits one setting at a time. Its rows come from
// config.Settings, the list the gleann web form is built from, so both show
// the same fields with the same defaults and checks.
type SettingsModel struct {
	reg     *settings.Registry[config.Config]
	cfg     *config.Config
	cursor  int
	editing bool
	input   textinput.Model
	notice  string
	err     error
	done    bool
}

// NewSettingsModel loads the current settings.
func NewSettingsModel() SettingsModel {
	reg := config.Settings()
	cfg, _ := reg.Load()
	in := textinput.New()
	in.CharLimit = 512
	in.Width = 60
	return SettingsModel{reg: reg, cfg: cfg, input: in}
}

// rows are the keys of the visible fields; the last row is Back ("").
func (m SettingsModel) rows() []string {
	var keys []string
	for _, f := range m.reg.Fields {
		if m.reg.Visible(m.cfg, f.Key) {
			keys = append(keys, f.Key)
		}
	}
	return append(keys, "")
}

// Done reports that the user left the screen.
func (m SettingsModel) Done() bool { return m.done }

func (m SettingsModel) Init() tea.Cmd { return nil }

func (m SettingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		if m.editing {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	if key.String() == "ctrl+c" {
		m.done = true
		return m, tea.Quit
	}
	if m.editing {
		return m.updateEditing(key)
	}

	rows := m.rows()
	cur := rows[m.cursor]
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
	case "left", "h", "right", "l":
		if cur != "" && steppable(m.field(cur)) {
			dir := 1
			if s := key.String(); s == "left" || s == "h" {
				dir = -1
			}
			m.change(cur, func(c *config.Config) error { return m.reg.Step(c, cur, dir) })
		}
	case "delete", "backspace":
		if cur != "" {
			m.change(cur, func(c *config.Config) error { return m.reg.Reset(c, cur) })
		}
	case "enter", " ":
		if cur == "" {
			m.done = true
			return m, tea.Quit
		}
		f := m.field(cur)
		if typed(f) {
			m.editing, m.notice, m.err = true, "", nil
			m.input.SetValue("")
			if f.Type != settings.Secret {
				m.input.SetValue(fmt.Sprint(m.reg.Value(m.cfg, cur)))
			}
			m.input.CursorEnd()
			return m, m.input.Focus()
		}
		m.change(cur, func(c *config.Config) error { return m.reg.Step(c, cur, 1) })
	}
	return m, nil
}

func (m SettingsModel) updateEditing(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	cur := m.rows()[m.cursor]
	switch key.String() {
	case "esc":
		m.editing = false
		m.input.Blur()
		return m, nil
	case "enter":
		v := m.input.Value()
		m.change(cur, func(c *config.Config) error { return m.reg.Set(c, cur, v) })
		if m.err == nil {
			m.editing = false
			m.input.Blur()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

func (m SettingsModel) field(key string) *settings.Field[config.Config] {
	f, _ := m.reg.Field(key)
	return f
}

// steppable fields change with ←/→; the others are typed.
func steppable(f *settings.Field[config.Config]) bool {
	switch f.Type {
	case settings.Bool, settings.Select:
		return true
	case settings.Number:
		return f.Step != nil || len(f.Options) > 0
	}
	return false
}

// typed fields open a text input on enter.
func typed(f *settings.Field[config.Config]) bool {
	switch f.Type {
	case settings.Text, settings.Path, settings.Secret:
		return true
	case settings.Select:
		return f.AllowCustom
	case settings.Number:
		return true
	}
	return false
}

// change applies one edit to the config on disk (re-read first, so edits made
// meanwhile from the web are kept) and saves it.
func (m *SettingsModel) change(key string, edit func(*config.Config) error) {
	cfg, err := m.reg.Load()
	if err == nil {
		err = edit(cfg)
	}
	if err == nil && m.reg.Validate != nil {
		err = m.reg.Validate(cfg)
	}
	if err == nil {
		err = m.reg.Save(cfg)
	}
	if err != nil {
		m.err, m.notice = fmt.Errorf("%s: %w", m.field(key).Label, err), ""
		return
	}
	m.cfg, m.err, m.notice = cfg, nil, "Saved."
	if m.reg.NeedsRestart([]string{key}) {
		m.notice = m.reg.Note
	}
	if n := len(m.rows()); m.cursor >= n {
		m.cursor = n - 1
	}
}

func (m SettingsModel) View() string {
	var b strings.Builder
	b.WriteString("\n" + TitleStyle.Render("  ⚙  Settings") + "\n")
	b.WriteString(SubtitleStyle.Render("  Each change is saved at once • the same settings as the gleann web UI") + "\n\n")

	rows, group := m.rows(), ""
	for i, key := range rows {
		if key == "" {
			b.WriteString("\n")
			if i == m.cursor {
				b.WriteString(ActiveItemStyle.Render("▸ ← Back") + "\n")
			} else {
				b.WriteString(NormalItemStyle.Render("  ← Back") + "\n")
			}
			continue
		}
		f := m.field(key)
		if f.Group != group {
			group = f.Group
			b.WriteString(DescStyle.Render(strings.ToUpper(group)) + "\n")
		}
		val := m.reg.Display(m.cfg, key)
		if steppable(f) && i == m.cursor {
			val = "◂ " + val + " ▸"
		}
		if m.reg.IsDefault(m.cfg, key) && f.Default != nil {
			val += "  (default)"
		}
		line := fmt.Sprintf("%-26s %s", f.Label, val)
		if i != m.cursor {
			b.WriteString(NormalItemStyle.Render("  "+line) + "\n")
			continue
		}
		if m.editing {
			b.WriteString(ActiveItemStyle.Render(fmt.Sprintf("▸ %-26s ", f.Label)) + m.input.View() + "\n")
		} else {
			b.WriteString(ActiveItemStyle.Render("▸ "+line) + "\n")
		}
		if f.Help != "" {
			b.WriteString(ActiveDescStyle.Render("  "+f.Help) + "\n")
		}
	}

	b.WriteString("\n")
	if m.notice != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(ColorSuccess).Render("  "+m.notice) + "\n")
	}
	if m.err != nil {
		b.WriteString(ErrorBadge.Render("  "+m.err.Error()) + "\n")
	}
	b.WriteString(StatusBarStyle.Render(m.hint(rows[m.cursor])) + "\n")
	return b.String()
}

func (m SettingsModel) hint(key string) string {
	switch {
	case m.editing:
		return "  enter save • esc cancel"
	case key == "":
		return "  ↑/↓ navigate • enter back • esc back"
	}
	f := m.field(key)
	h := "  ↑/↓ navigate"
	if steppable(f) {
		h += " • ←/→ change"
	}
	if typed(f) {
		h += " • enter type a value"
	} else {
		h += " • enter next"
	}
	if f.Default != nil || f.Type != settings.Bool {
		h += " • del reset"
	}
	return h + " • esc back"
}
