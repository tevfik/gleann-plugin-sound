package tui

import "github.com/charmbracelet/lipgloss"

// Color palette — matches gleann's violet theme.
var (
	ColorPrimary   = lipgloss.Color("#7C3AED") // violet
	ColorSecondary = lipgloss.Color("#06B6D4") // cyan
	ColorAccent    = lipgloss.Color("#F59E0B") // amber
	ColorSuccess   = lipgloss.Color("#10B981") // emerald
	ColorError     = lipgloss.Color("#EF4444") // red
	ColorMuted     = lipgloss.Color("#6B7280") // gray
	ColorFg        = lipgloss.Color("#E2E8F0") // light slate
	ColorDimFg     = lipgloss.Color("#94A3B8") // dim slate
)

var (
	LogoStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			MarginBottom(1)

	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorFg).
			Background(ColorPrimary).
			Padding(0, 2).
			MarginBottom(1)

	SubtitleStyle = lipgloss.NewStyle().
			Foreground(ColorDimFg).
			Italic(true).
			MarginBottom(1)

	ActiveItemStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true).
			PaddingLeft(2)

	NormalItemStyle = lipgloss.NewStyle().
			Foreground(ColorFg).
			PaddingLeft(4)

	DescStyle = lipgloss.NewStyle().
			Foreground(ColorDimFg).
			PaddingLeft(4).
			Italic(true)

	ActiveDescStyle = lipgloss.NewStyle().
			Foreground(ColorSecondary).
			PaddingLeft(2).
			Italic(true)

	SuccessBadge = lipgloss.NewStyle().
			Foreground(ColorSuccess).
			Bold(true)

	ErrorBadge = lipgloss.NewStyle().
			Foreground(ColorError).
			Bold(true)

	StatusBarStyle = lipgloss.NewStyle().
			Foreground(ColorDimFg).
			MarginTop(1)

	CheckboxChecked = lipgloss.NewStyle().
			Foreground(ColorSuccess).
			Bold(true)

	CheckboxUnchecked = lipgloss.NewStyle().
				Foreground(ColorMuted)

	ProgressStyle = lipgloss.NewStyle().
			Foreground(ColorSecondary)
)

// Logo returns the ASCII art logo for gleann-sound.
func Logo() string {
	logo := `
   ╔═╗┬  ┌─┐┌─┐┌┐┌┌┐┌   ╔═╗┌─┐┬ ┬┌┐┌┌┬┐
   ║ ╦│  ├┤ ├─┤│││││││───╚═╗│ ││ ││││ ││ 
   ╚═╝┴─┘└─┘┴ ┴┘└┘┘└┘   ╚═╝└─┘└─┘┘└┘─┴┘ `
	return LogoStyle.Render(logo)
}
