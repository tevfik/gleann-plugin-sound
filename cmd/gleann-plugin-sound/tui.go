package main

import (
	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/tui"
)

func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open interactive setup & configuration UI",
		Long: `Launch the interactive terminal UI for gleann-plugin-sound.

The TUI provides:
  • Setup wizard — select and download models, choose the backend
    (whisper, onnx, nemotron, tc), default language, hotkey, audio source
  • Dictate, Listen, Serve (plugin-serve) and Test — launch those commands
    with your saved settings
  • Install — copy binary to PATH, install shell completions,
    setup input device permissions
  • Uninstall — remove binary, completions, config, and models`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return tui.Run()
		},
	}
}
