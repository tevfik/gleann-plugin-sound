// Package main is the entry point for the gleann-sound CLI.
//
// gleann-sound is a companion daemon/plugin for the gleann vector database
// that handles heavy audio processing, CGO integrations, and OS-level hooks.
// It supports four execution modes:
//
//  1. transcribe — On-demand file transcription
//  2. listen     — Live CLI streaming transcription
//  3. serve      — Background gRPC daemon (HashiCorp go-plugin)
//  4. dictate    — Push-to-talk voice dictation with keystroke injection
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-sound/internal/config"

	// Register backends — import for side-effect init().
	_ "github.com/tevfik/gleann-sound/internal/onnx"
	_ "github.com/tevfik/gleann-sound/internal/whisper"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	root := &cobra.Command{
		Use:   "gleann-sound",
		Short: "Audio processing companion for the gleann RAG engine",
		Long: `gleann-sound captures audio, runs local Whisper inference, and
delivers transcriptions — either as CLI output, background gRPC events
for the main gleann application, or as injected keystrokes for voice dictation.

All audio is processed locally using whisper.cpp — no cloud APIs required.

Run 'gleann-sound tui' for interactive setup and configuration.`,
		Version: version,
	}

	// Load saved config for defaults (fall back to hardcoded defaults if absent).
	defaultModel := "models/ggml-base.en.bin"
	if cfg := config.Load(); cfg != nil && cfg.Completed {
		if cfg.DefaultModel != "" {
			defaultModel = cfg.DefaultModel
		}
	}

	// Persistent flags shared by all subcommands.
	root.PersistentFlags().String("model", defaultModel,
		"Path to the Whisper GGML model file")
	root.PersistentFlags().String("backend", "whisper",
		"Transcription backend: whisper (default) or onnx")
	root.PersistentFlags().Bool("verbose", false,
		"Enable verbose / debug logging")

	// Register execution modes and TUI.
	root.AddCommand(
		newTranscribeCmd(),
		newListenCmd(),
		newServeCmd(),
		newDictateCmd(),
		newTUICmd(),
		newTestCmd(),
		newDevicesCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
