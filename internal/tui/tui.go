// Package tui provides the interactive terminal user interface for gleann-plugin-sound.
package tui

import (
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/speaker"
)

// Run starts the interactive TUI application loop.
// It shows the home screen and routes to sub-screens.
// If onboarding (setup) is not completed, only Setup + Quit are shown.
// After setup is done, the full menu (Dictate, Listen, Serve, Test, ...) appears.
func Run() error {
	for {
		// ── Home screen ──
		home := NewHomeModel()
		p := tea.NewProgram(home, tea.WithAltScreen())
		result, err := p.Run()
		if err != nil {
			return fmt.Errorf("home screen: %w", err)
		}
		h := result.(HomeModel)
		if h.Quitting() {
			return nil
		}

		switch h.Chosen() {
		case ScreenSetup:
			if err := runSetup(); err != nil {
				return err
			}
		case ScreenDictate:
			runCLIMode(h.Config(), "dictate")
		case ScreenListen:
			runCLIMode(h.Config(), "listen")
		case ScreenServe:
			runServeToggle(h.Config())
		case ScreenDaemonToggle:
			runDaemonToggle()
		case ScreenInstall:
			if err := runInstall(); err != nil {
				return err
			}
		case ScreenUninstall:
			if err := runUninstall(); err != nil {
				return err
			}
		case ScreenTest:
			runCLIMode(h.Config(), "test")
		case ScreenSpeakers:
			runSpeakers()
		case ScreenUpdate:
			runSubcommand("update")
		}
	}
}

// RunSetup runs the setup wizard standalone and returns the config.
func RunSetup() (*config.Config, error) {
	cfg := config.Load()
	m := NewSetupModel(cfg)
	p := tea.NewProgram(m, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("setup: %w", err)
	}
	sm := result.(SetupModel)
	if sm.Cancelled() {
		return nil, nil
	}
	return sm.Result(), nil
}

// ── Internal screen runners ────────────────────────────────────

func runSetup() error {
	cfg := config.Load()
	m := NewSetupModel(cfg)
	p := tea.NewProgram(m, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	sm := result.(SetupModel)
	if sm.Cancelled() {
		return nil // go back to home
	}

	if r := sm.Result(); r != nil {
		if err := config.Save(r); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save config: %v\n", err)
		}
	}
	return nil
}

func runInstall() error {
	m := NewInstallModel()
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	return nil
}

func runUninstall() error {
	m := NewUninstallModel()
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	if err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	return nil
}

// runSpeakers shows the voice manager; adding or re-recording a voice hands the
// terminal to `speakers record <name>` and comes back to the list.
func runSpeakers() {
	for {
		p := tea.NewProgram(NewSpeakersModel(speaker.DefaultPath()), tea.WithAltScreen())
		result, err := p.Run()
		if err != nil {
			fmt.Fprintf(os.Stderr, "speakers: %v\n", err)
			return
		}
		m := result.(SpeakersModel)
		if action, name := m.Action(); action == spActionRecord {
			runSubcommand("speakers", "record", name, "--lang", m.Lang())
			continue
		}
		return
	}
}

// runSubcommand runs another gleann-plugin-sound command in this terminal and
// waits for enter before the TUI redraws.
func runSubcommand(args ...string) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n%v\n", err)
	}
	fmt.Println("\nPress enter to return to menu...")
	buf := make([]byte, 1)
	_, _ = os.Stdin.Read(buf)
}

// runCLIMode launches a gleann-plugin-sound subcommand (dictate, listen, plugin-serve, test)
// in the current terminal with config-derived flags, then returns to the TUI.
func runCLIMode(cfg *config.Config, mode string) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}

	args := []string{mode}
	if cfg != nil {
		if cfg.DefaultModel != "" {
			args = append(args, "--model", cfg.DefaultModel)
		}
		if cfg.Language != "" {
			args = append(args, "--language", cfg.Language)
		}
		if cfg.Backend != "" && cfg.Backend != "whisper" {
			args = append(args, "--backend", cfg.Backend)
		}
		if mode == "dictate" && cfg.Hotkey != "" {
			args = append(args, "--key", cfg.Hotkey)
		}
		if (mode == "listen" || mode == "transcribe") && cfg.OutputDir != "" {
			args = append(args, "--output", cfg.OutputDir)
		}
		if mode == "listen" && cfg.AudioSource != "" && cfg.AudioSource != "mic" {
			args = append(args, "--source", cfg.AudioSource)
		}
		if mode == "test" && cfg.Hotkey != "" {
			args = append(args, "--key", cfg.Hotkey)
		}
		if cfg.ExecutionProvider != "" && cfg.ExecutionProvider != "auto" {
			args = append(args, "--provider", cfg.ExecutionProvider)
		}
	}

	cmd := exec.Command(exe, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	// Wait for user to acknowledge before returning to TUI.
	fmt.Println("\nPress enter to return to menu...")
	buf := make([]byte, 1)
	_, _ = os.Stdin.Read(buf)
}

func runDaemonToggle() {
	fmt.Println()
	if isDaemonRunning() {
		fmt.Println("⏹  Stopping and removing dictate background daemon...")
		if err := removeDaemon(); err != nil {
			fmt.Printf("✗ Error: %v\n", err)
		} else {
			fmt.Println("✓ Dictate background daemon stopped and disabled.")
		}
	} else {
		fmt.Println("🚀 Installing and starting dictate background daemon...")
		if err := installDaemon(); err != nil {
			fmt.Printf("✗ Error: %v\n", err)
		} else {
			fmt.Println("✓ Dictate background daemon installed and started.")
			fmt.Println("  Push-to-talk hotkey is now active system-wide at login.")
			fmt.Println("  Logs: journalctl --user -u gleann-plugin-sound-dictate -f")
		}
	}
	fmt.Println("\nPress enter to return to menu...")
	buf := make([]byte, 1)
	_, _ = os.Stdin.Read(buf)
}
