package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-sound/internal/audio"
	"github.com/tevfik/gleann-sound/internal/core"
	"github.com/tevfik/gleann-sound/internal/plugin"
)

// newServeCmd creates the "serve" subcommand (Mode 3: Daemon / Plugin).
//
// Usage:
//
//	gleann-sound serve --model models/ggml-base.en.bin --addr localhost:50051
//
// Runs as a background daemon, exposing a local gRPC port.  The main gleann
// application connects as a go-plugin host to send configurations and receive
// streaming transcription events for embedding into its HNSW graph.
func newServeCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run as a background gRPC daemon for the main gleann application",
		Long: `Mode 3 — Daemon / Plugin.

Starts a local gRPC server that the main gleann application connects to via
HashiCorp go-plugin.  The server captures audio, runs Whisper, and streams
transcription events over the gRPC channel.

Use --addr to configure the listen address.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modelPath, _ := cmd.Flags().GetString("model")
			lang, _ := cmd.Flags().GetString("language")

			log.Println("[serve] initialising...")

			// ── Initialise components ──────────────────────────────
			backend, _ := cmd.Flags().GetString("backend")
			log.Printf("[serve] loading model: %s (backend: %s)", modelPath, backend)
			engine, err := core.NewTranscriber(backend, modelPath)
			if err != nil {
				return fmt.Errorf("failed to load model: %w", err)
			}
			defer engine.Close()

			if lang != "" {
				engine.SetLanguage(lang)
			}

			capturer := audio.NewMalgoCapturer()

			// ── Event handler — logs events to stderr + JSON ──────
			handler := func(event core.TranscriptionEvent) {
				enc := json.NewEncoder(os.Stderr)
				for _, seg := range event.Segments {
					_ = enc.Encode(newJSONSegment(seg))
				}
			}

			// ── Create and start the gRPC server ──────────────────
			srv := plugin.NewGRPCServer(capturer, engine, handler)

			// Graceful shutdown on SIGINT / SIGTERM.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				log.Println("[serve] shutting down…")
				srv.Stop()
			}()

			log.Printf("[serve] starting gRPC server on %s", addr)
			if err := srv.Serve(addr); err != nil {
				return fmt.Errorf("gRPC server error: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051",
		"Address for the gRPC server to listen on")
	cmd.Flags().String("language", "",
		"Language code for transcription (e.g. tr, en, de). Empty = auto-detect")

	return cmd
}
