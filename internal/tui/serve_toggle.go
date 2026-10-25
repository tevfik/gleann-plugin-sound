package tui

import (
	"fmt"
	"os"

	"github.com/tevfik/gleann-plugin-sound/internal/config"
)

// DefaultServePort is the port gleann's plugins.json entry points at.
const DefaultServePort = 8767

// ServeArgs are the command-line arguments of the background plugin server,
// derived from the saved configuration (same flags the TUI passes elsewhere).
func ServeArgs(cfg *config.Config) []string {
	args := []string{"plugin-serve", "--port", fmt.Sprint(DefaultServePort)}
	if cfg == nil {
		return args
	}
	if cfg.DefaultModel != "" {
		args = append(args, "--model", cfg.DefaultModel)
	}
	if cfg.Language != "" {
		args = append(args, "--language", cfg.Language)
	}
	if cfg.Backend != "" && cfg.Backend != "whisper" {
		args = append(args, "--backend", cfg.Backend)
	}
	if cfg.ExecutionProvider != "" && cfg.ExecutionProvider != "auto" {
		args = append(args, "--provider", cfg.ExecutionProvider)
	}
	return args
}

// serveMenuItem is the Serve menu entry with its live on/off state.
func serveMenuItem() menuItem {
	if ServeEnabled() {
		return menuItem{title: "Serve (Disable)", desc: "Plugin server is running and starts at login — select to stop & disable", icon: "⏹ ", screen: ScreenServe}
	}
	return menuItem{title: "Serve (Enable)", desc: "Run the gleann plugin server in the background, starting at login", icon: "🔌", screen: ScreenServe}
}

// runServeToggle enables or disables the background server, then waits for enter.
func runServeToggle(cfg *config.Config) {
	fmt.Println()
	if ServeEnabled() {
		fmt.Println("⏹  Stopping and disabling the plugin server...")
		if err := DisableServe(); err != nil {
			fmt.Printf("✗ Error: %v\n", err)
		} else {
			fmt.Println("✓ Plugin server stopped; it will no longer start at login.")
		}
	} else {
		fmt.Println("🔌 Enabling the plugin server in the background...")
		if err := EnableServe(ServeArgs(cfg)); err != nil {
			fmt.Printf("✗ Error: %v\n", err)
		} else {
			fmt.Printf("✓ Plugin server running on :%d and will start at login.\n", DefaultServePort)
			fmt.Println("  Linux logs: journalctl --user -u gleann-plugin-sound-serve -f")
		}
	}
	fmt.Println("\nPress enter to return to menu...")
	buf := make([]byte, 1)
	_, _ = os.Stdin.Read(buf)
}
