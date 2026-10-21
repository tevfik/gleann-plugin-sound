// Package main is the entry point for the gleann-plugin-sound CLI.
//
// gleann-plugin-sound is a companion daemon/plugin for the gleann vector database
// that handles heavy audio processing, CGO integrations, and OS-level hooks.
// It runs entirely locally and supports these execution modes:
//
//  1. transcribe   — On-demand file transcription
//  2. listen       — Live CLI streaming transcription
//  3. dictate      — Push-to-talk voice dictation with keystroke injection
//  4. plugin-serve — HTTP server for gleann (/convert) plus a live WebSocket (/live)
//
// plus helpers: speakers, daemon, install, devices, test, benchmark, evaluate,
// live-client and the interactive tui.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/httpserver"
	"github.com/tevfik/gleann-plugin-sound/internal/tui"

	// Register backends — import for side-effect init().
	_ "github.com/tevfik/gleann-plugin-sound/internal/nemotron"
	_ "github.com/tevfik/gleann-plugin-sound/internal/onnx"
	_ "github.com/tevfik/gleann-plugin-sound/internal/tcasr"
	_ "github.com/tevfik/gleann-plugin-sound/internal/whisper"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	// gleann's plugin installer registers a freshly downloaded plugin with
	// `<binary> --install`; here that is the `install` command.
	if len(os.Args) > 1 && os.Args[1] == "--install" {
		os.Args[1] = "install"
	}
	if version != "dev" {
		httpserver.PluginVersion = strings.TrimPrefix(version, "v")
	}
	root := &cobra.Command{
		Use:   "gleann-plugin-sound",
		Short: "Audio processing companion for the gleann RAG engine",
		Long: `gleann-plugin-sound captures audio, runs local speech recognition, and
delivers transcriptions as CLI output, as injected keystrokes (voice
dictation), or over HTTP/WebSocket to gleann and other clients.

All audio is processed locally — no cloud APIs required. Backends:
  whisper    whisper.cpp (GGML models, CPU)
  onnx       Whisper on ONNX Runtime (CPU/GPU)
  nemotron   Nemotron 3.5 streaming, int4 ONNX (real time)
  tc         Nemotron 3.5 streaming, GGUF Q8 via the gleann-asr-helper
             sidecar (real time, most accurate live backend)

Settings are read from ~/.gleann/sound.json; flags override them.

Common tasks:
  gleann-plugin-sound tui                        interactive setup
  gleann-plugin-sound dictate                    push-to-talk typing
  gleann-plugin-sound listen --language tr       live transcription
  gleann-plugin-sound plugin-serve               server for gleann and /live clients
  gleann-plugin-sound speakers record NAME      record your voice to name speakers
  gleann-plugin-sound update                    update from the latest release`,
		Version: version,
		// Runtime failures are not usage mistakes, and main prints the error once.
		SilenceUsage:  true,
		SilenceErrors: true,
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
		"Path to the model: a Whisper GGML file (whisper), an ONNX model directory (onnx, nemotron) or a GGUF file (tc)")
	root.PersistentFlags().String("backend", defaultBackend,
		"Transcription backend: whisper, onnx, nemotron or tc (default: backend in sound.json, else whisper)")
	root.PersistentFlags().String("provider", defaultProvider,
		"ONNX execution provider for onnx and nemotron: auto (CUDA when available), cuda, cpu")
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

	// Shell completions installed by the TUI come from cobra, so they always
	// match the real command tree.
	tui.CompletionScript = func(shell string) (string, error) {
		var buf bytes.Buffer
		var err error
		switch shell {
		case "bash":
			err = root.GenBashCompletionV2(&buf, true)
		case "zsh":
			err = root.GenZshCompletion(&buf)
		case "fish":
			err = root.GenFishCompletion(&buf, true)
		default:
			err = fmt.Errorf("unsupported shell %q", shell)
		}
		return buf.String(), err
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
		newDaemonCmd(),
		newSpeakersCmd(),
		newLiveClientCmd(),
		newVersionCmd(),
		newUpdateCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
