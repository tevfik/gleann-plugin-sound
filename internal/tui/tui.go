// Package tui provides the interactive terminal user interface for gleann-sound.
package tui

import (
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tevfik/gleann-sound/internal/config"
)

// Run starts the interactive TUI application loop.
// It shows the home screen and routes to sub-screens.
// If onboarding (setup) is not completed, only Setup + Quit are shown.
// After setup is done, the full menu with Dictate/Listen/Transcribe appears.
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
			runCLIMode(h.Config(), "serve")
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

// runCLIMode launches a gleann-sound subcommand (dictate, listen, transcribe)
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
		if (mode == "dictate" || mode == "serve") && cfg.GRPCAddr != "" {
			args = append(args, "--addr", cfg.GRPCAddr)
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
	}

	cmd := exec.Command(exe, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	// Wait for user to acknowledge before returning to TUI.
	fmt.Println("\nPress enter to return to menu...")
	buf := make([]byte, 1)
	os.Stdin.Read(buf)
}
