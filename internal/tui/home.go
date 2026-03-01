package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tevfik/gleann-sound/internal/config"
)

// Screen represents the active screen.
type Screen int

const (
	ScreenHome Screen = iota
	ScreenSetup
	ScreenDictate
	ScreenListen
	ScreenServe
	ScreenInstall
	ScreenUninstall
	ScreenTest
)

type menuItem struct {
	title  string
	desc   string
	icon   string
	screen Screen
}

// Menu items shown before onboarding is completed.
var preSetupMenuItems = []menuItem{
	{title: "Setup", desc: "Configure models, language & hotkey", icon: "⚙ ", screen: ScreenSetup},
	{title: "Quit", desc: "Exit", icon: "👋", screen: ScreenHome},
}

// Menu items shown after onboarding is completed.
var postSetupMenuItems = []menuItem{
	{title: "Dictate", desc: "Push-to-talk voice dictation", icon: "🎙 ", screen: ScreenDictate},
	{title: "Listen", desc: "Continuous real-time transcription", icon: "👂", screen: ScreenListen},
	{title: "Serve", desc: "gRPC daemon for gleann integration", icon: "🔌", screen: ScreenServe},
	{title: "Test", desc: "Diagnose mic, hotkey, whisper & keyboard", icon: "🔬", screen: ScreenTest},
	{title: "Setup", desc: "Reconfigure models, language & hotkey", icon: "⚙ ", screen: ScreenSetup},
	{title: "Install", desc: "Install binary & shell completions system-wide", icon: "📦", screen: ScreenInstall},
	{title: "Uninstall", desc: "Remove gleann-sound from system", icon: "🗑 ", screen: ScreenUninstall},
	{title: "Quit", desc: "Exit", icon: "👋", screen: ScreenHome},
}

// HomeModel is the main TUI hub.
type HomeModel struct {
	cursor   int
	width    int
	height   int
	quitting bool
	chosen   Screen
	items    []menuItem
	cfg      *config.Config
}

// NewHomeModel creates a home screen, loading config to determine menu.
func NewHomeModel() HomeModel {
	cfg := config.Load()
	return newHomeModelWithConfig(cfg)
}

// NewHomeModelWithConfig creates a home screen with the given config (for testing).
func NewHomeModelWithConfig(cfg *config.Config) HomeModel {
	return newHomeModelWithConfig(cfg)
}

func newHomeModelWithConfig(cfg *config.Config) HomeModel {
	items := preSetupMenuItems
	if cfg != nil && cfg.Completed {
		items = postSetupMenuItems
	}
	return HomeModel{items: items, cfg: cfg}
}

func (m HomeModel) Init() tea.Cmd { return nil }

func (m HomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			if m.cursor < len(m.items) {
				item := m.items[m.cursor]
				if item.title == "Quit" {
					m.quitting = true
				} else {
					m.chosen = item.screen
				}
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m HomeModel) Chosen() Screen { return m.chosen }
func (m HomeModel) Quitting() bool { return m.quitting }
func (m HomeModel) Config() *config.Config { return m.cfg }

func (m HomeModel) View() string {
	if m.quitting {
		return "\n  " + lipgloss.NewStyle().Foreground(ColorMuted).Render("Bye! 👋") + "\n"
	}

	var b strings.Builder
	b.WriteString(Logo())
	b.WriteString("\n")
	b.WriteString(SubtitleStyle.Render("  Local speech-to-text companion for gleann"))
	b.WriteString("\n\n")

	for i, item := range m.items {
		if i == m.cursor {
			b.WriteString(ActiveItemStyle.Render(fmt.Sprintf("▸ %s %s", item.icon, item.title)))
			b.WriteString("\n")
			b.WriteString(ActiveDescStyle.Render("  " + item.desc))
		} else {
			b.WriteString(NormalItemStyle.Render(fmt.Sprintf("  %s %s", item.icon, item.title)))
			b.WriteString("\n")
			b.WriteString(DescStyle.Render(item.desc))
		}
		b.WriteString("\n")
	}

	// Show config status.
	if m.cfg != nil && m.cfg.Completed {
		b.WriteString("\n")
		info := []string{}
		for _, model := range m.cfg.Models {
			info = append(info, model.Name)
		}
		modelStr := strings.Join(info, ", ")
		if modelStr == "" {
			modelStr = "none"
		}
		langStr := m.cfg.Language
		if langStr == "" {
			langStr = "auto"
		}
		hotkeyStr := m.cfg.Hotkey
		if hotkeyStr == "" {
			hotkeyStr = "not set"
		}
		statusLine := fmt.Sprintf("  models: %s │ lang: %s │ hotkey: %s",
			modelStr, langStr, hotkeyStr)
		backendStr := m.cfg.Backend
		if backendStr == "" {
			backendStr = "whisper"
		}
		statusLine += fmt.Sprintf(" │ backend: %s", backendStr)
		if m.cfg.GRPCAddr != "" {
			statusLine += fmt.Sprintf(" │ gRPC: %s", m.cfg.GRPCAddr)
		}
		b.WriteString(lipgloss.NewStyle().Foreground(ColorDimFg).Render(statusLine))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(StatusBarStyle.Render("  ↑/↓ navigate • enter select • q quit"))
	return b.String()
}
