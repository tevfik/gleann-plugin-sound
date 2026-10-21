package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/tui"
)

// newDaemonCmd creates "daemon", the non-interactive way to run push-to-talk
// dictation in the background from login (systemd user service on Linux,
// launchd on macOS, a scheduled task on Windows). The TUI's Install screen
// does the same.
func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run push-to-talk dictation in the background (install|remove|status)",
		Long: `Manages the background dictation service. Once installed, hold the hotkey
anywhere to dictate: words are typed into the focused window as you speak.

The service runs this binary ('dictate') with the settings in ~/.gleann/sound.json
(model, language, hotkey) and starts at login.`,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Install and start the dictation service",
			Long: `Writes the service definition for the binary that is running now and starts it.
Run it from the installed copy (e.g. ~/.local/bin/gleann-plugin-sound), not from a build directory.`,
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := tui.InstallDaemon(); err != nil {
					return err
				}
				fmt.Println("Dictation daemon installed and started.")
				fmt.Println("Logs: journalctl --user -u gleann-plugin-sound-dictate -f")
				return nil
			},
		},
		&cobra.Command{
			Use:   "remove",
			Short: "Stop and remove the dictation service",
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := tui.RemoveDaemon(); err != nil {
					return err
				}
				fmt.Println("Dictation daemon removed.")
				return nil
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show whether the dictation service is running",
			RunE: func(cmd *cobra.Command, args []string) error {
				if tui.DaemonRunning() {
					fmt.Println("running")
					return nil
				}
				return fmt.Errorf("not running")
			},
		},
	)
	return cmd
}
