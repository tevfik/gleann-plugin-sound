// Package main is the entry point for the gleann-plugin-sound CLI.
//
// gleann-plugin-sound is a companion daemon/plugin for the gleann vector database
// that handles heavy audio processing, CGO integrations, and OS-level hooks.
// It supports four execution modes:
//
//  1. transcribe — On-demand file transcription
//  2. listen     — Live CLI streaming transcription
//  3. dictate    — Push-to-talk voice dictation with keystroke injection
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/config"

	// Register backends — import for side-effect init().
	_ "github.com/tevfik/gleann-plugin-sound/internal/nemotron"
	_ "github.com/tevfik/gleann-plugin-sound/internal/onnx"
	_ "github.com/tevfik/gleann-plugin-sound/internal/whisper"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	root := &cobra.Command{
		Use:   "gleann-plugin-sound",
		Short: "Audio processing companion for the gleann RAG engine",
		Long: `gleann-plugin-sound captures audio, runs local Whisper inference, and
delivers transcriptions — either as CLI output or as injected keystrokes
for voice dictation.

All audio is processed locally using whisper.cpp — no cloud APIs required.

Run 'gleann-plugin-sound tui' for interactive setup and configuration.`,
		Version: version,
	}

	// Load saved config for defaults (fall back to hardcoded defaults if absent).
	defaultModel := "models/ggml-base.en.bin"
	if cfg := config.Load(); cfg != nil && cfg.Completed {
		if cfg.DefaultModel != "" {
			defaultModel = cfg.DefaultModel
		}
	}

	// Load execution provider preference from config.
	defaultBackend := "whisper"
	defaultProvider := "auto"
	if cfg := config.Load(); cfg != nil && cfg.Completed {
		if cfg.Backend != "" {
			defaultBackend = cfg.Backend
		}
		if cfg.ExecutionProvider != "" {
			defaultProvider = cfg.ExecutionProvider
		}
	}

	// Persistent flags shared by all subcommands.
	root.PersistentFlags().String("model", defaultModel,
		"Path to the Whisper GGML model file")
	root.PersistentFlags().String("backend", defaultBackend,
		"Transcription backend: whisper (default), onnx or nemotron")
	root.PersistentFlags().String("provider", defaultProvider,
		"ONNX execution provider: auto (default), cuda, cpu")
	root.PersistentFlags().Bool("verbose", false,
		"Enable verbose / debug logging")

	// Apply execution provider before any subcommand runs.
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		provider, _ := cmd.Flags().GetString("provider")
		if provider != "" {
			// Import is already done via init(); set the provider for ONNX engine.
			setONNXProvider(provider)
		}
	}

	// Register execution modes, TUI, and plugin integration.
	root.AddCommand(
		newTranscribeCmd(),
		newListenCmd(),
		newDictateCmd(),
		newTUICmd(),
		newTestCmd(),
		newBenchmarkCmd(),
		newEvaluateCmd(),
		newDevicesCmd(),
		newPluginServeCmd(),
		newInstallPluginCmd(),
		newVersionCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
