package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-sound/internal/audio"
)

// newDevicesCmd creates the "devices" subcommand for listing audio devices.
func newDevicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "List available audio capture and playback devices",
		Long: `Lists all audio input (capture) and output (playback) devices
detected by the system. Useful for diagnosing audio source issues.

On Linux with PulseAudio/PipeWire, monitor sources (loopback) appear
as capture devices with "Monitor" in the name.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("=== Capture (Input) Devices ===")
			captureDevs, err := audio.ListCaptureDevices()
			if err != nil {
				fmt.Printf("  Error: %v\n", err)
			} else if len(captureDevs) == 0 {
				fmt.Println("  (none found)")
			} else {
				for i, d := range captureDevs {
					def := ""
					if d.IsDefault {
						def = " [DEFAULT]"
					}
					monitor := ""
					if isMonitor(d.Name) {
						monitor = " [MONITOR/LOOPBACK]"
					}
					fmt.Printf("  %d. %s%s%s\n", i+1, d.Name, def, monitor)
				}
			}

			fmt.Println()
			fmt.Println("=== Playback (Output) Devices ===")
			playbackDevs, err := audio.ListPlaybackDevices()
			if err != nil {
				fmt.Printf("  Error: %v\n", err)
			} else if len(playbackDevs) == 0 {
				fmt.Println("  (none found)")
			} else {
				for i, d := range playbackDevs {
					def := ""
					if d.IsDefault {
						def = " [DEFAULT]"
					}
					fmt.Printf("  %d. %s%s\n", i+1, d.Name, def)
				}
			}

			fmt.Println()
			fmt.Println("Tip: Use --source speaker to capture system audio (loopback).")
			fmt.Println("     Use --source both to capture mic + speaker simultaneously.")
			return nil
		},
	}
}

func isMonitor(name string) bool {
	for _, kw := range []string{"Monitor", "monitor", "MONITOR"} {
		if len(name) >= len(kw) {
			for i := 0; i <= len(name)-len(kw); i++ {
				if name[i:i+len(kw)] == kw {
					return true
				}
			}
		}
	}
	return false
}
