package main

import (
	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-sound/internal/tui"
)

func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open interactive setup & configuration UI",
		Long: `Launch the interactive terminal UI for gleann-sound.

The TUI provides:
  • Setup wizard — select and download Whisper models, configure
    default language, hotkey, and save settings
  • Install — copy binary to PATH, install shell completions,
    setup input device permissions
  • Uninstall — remove binary, completions, config, and models`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return tui.Run()
		},
	}
}
