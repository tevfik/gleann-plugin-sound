package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/audio"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
)

// newListenCmd creates the "listen" subcommand (Mode 2: Live CLI Stream).
func newListenCmd() *cobra.Command {
	var outputFile string
	var sourceFlag string

	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Live audio transcription streamed as JSON to stdout",
		Long: `Mode 2 — Live CLI Streaming (Sliding Window Pipeline).

Captures audio from the selected source (microphone, speaker/loopback, or both),
uses a sliding-window pipeline with VAD pre-checks and text deduplication to
deliver continuous, real-time transcription.

The pipeline processes 3-second overlapping windows every 2 seconds, with
context carryover between windows for coherent output.  This replaces the
older utterance-based mode and handles continuous speech without gaps.

Audio sources:
  mic      — Default microphone input (default)
  speaker  — System audio output (loopback/desktop audio)
  both     — Microphone and speaker simultaneously

Press Ctrl+C to stop.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modelPath, _ := cmd.Flags().GetString("model")
			lang, _ := cmd.Flags().GetString("language")

			// Load language from config if flag not set.
			if lang == "" {
				if cfg := config.Load(); cfg != nil && cfg.Language != "" {
					lang = cfg.Language
				}
			}

			log.Println("[listen] initialising...")

			// ── Initialise transcription engine ───────────────────
			backend, _ := cmd.Flags().GetString("backend")
			log.Printf("[listen] loading model: %s (backend: %s)", modelPath, backend)
			engine, err := core.NewTranscriber(backend, modelPath)
			if err != nil {
				return fmt.Errorf("failed to load model: %w", err)
			}
			defer engine.Close()

			if lang != "" {
				engine.SetLanguage(lang)
			}

			// Cast to StreamingTranscriber for the pipeline.
			streamEngine, ok := engine.(core.StreamingTranscriber)
			if !ok {
				return fmt.Errorf("backend %q does not support streaming transcription", backend)
			}

			// ── Audio source selection ─────────────────────────────
			src := sourceFlag
			if src == "" {
				if cfg := config.Load(); cfg != nil && cfg.AudioSource != "" {
					src = cfg.AudioSource
				} else {
					src = "mic"
				}
			}
			audioSource, err := audio.ParseAudioSource(src)
			if err != nil {
				return err
			}
			log.Printf("[listen] audio source: %s", audioSource)

			capturer := audio.NewMalgoCapturerWithSource(audioSource)
			vad := audio.DefaultVAD()

			// ── Signal handling ────────────────────────────────────
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				log.Println("[listen] shutting down…")
				cancel()
			}()

			// ── Output setup ───────────────────────────────────────
			stdoutEnc := json.NewEncoder(os.Stdout)
			var outFile *os.File
			var outIsTxt bool
			if outputFile != "" {
				outputFile = expandOutputPath(outputFile)
				isDir := strings.HasSuffix(outputFile, string(os.PathSeparator)) || filepath.Ext(outputFile) == ""
				if info, err := os.Stat(outputFile); err == nil && info.IsDir() {
					isDir = true
				}
				if isDir {
					if err := os.MkdirAll(outputFile, 0o755); err != nil {
						return fmt.Errorf("failed to create output directory: %w", err)
					}
					stamp := time.Now().Format("2006-01-02_15-04-05")
					outputFile = filepath.Join(outputFile, stamp+".txt")
				} else {
					if err := os.MkdirAll(filepath.Dir(outputFile), 0o755); err != nil {
						return fmt.Errorf("failed to create output directory: %w", err)
					}
				}
				f, err := os.Create(outputFile)
				if err != nil {
					return fmt.Errorf("failed to create output file: %w", err)
				}
				defer f.Close()
				outFile = f
				outIsTxt = strings.HasSuffix(outputFile, ".txt")
				log.Printf("[listen] output file: %s", outputFile)
			}

			writeResult := func(result core.StreamResult) {
				seg := core.Segment{
					Start: result.Start,
					End:   result.End,
					Text:  result.Text,
				}
				js := newJSONSegment(seg)
				_ = stdoutEnc.Encode(js)
				if outFile != nil {
					if outIsTxt {
						fmt.Fprintf(outFile, "%s\n", strings.TrimSpace(result.Text))
					} else {
						_ = json.NewEncoder(outFile).Encode(js)
					}
				}
			}

			// ── Start audio capture into channel ───────────────────
			// Buffer ~30 seconds of audio (30s × 16kHz / 480 frames ≈ 1000).
			// This gives the pipeline headroom when inference is slow.
			audioCh := make(chan []int16, 1024)
			var dropCount int64
			var lastDropLog time.Time
			err = capturer.Start(ctx, func(pcmData []int16) {
				// Copy data to avoid malgo buffer reuse issues.
				chunk := make([]int16, len(pcmData))
				copy(chunk, pcmData)
				select {
				case audioCh <- chunk:
				default:
					dropCount++
					// Throttle warning: log at most once per second.
					if now := time.Now(); now.Sub(lastDropLog) > time.Second {
						log.Printf("[listen] WARNING: dropped %d audio frames (pipeline backed up)", dropCount)
						dropCount = 0
						lastDropLog = now
					}
				}
			})
			if err != nil {
				return fmt.Errorf("failed to start audio capture: %w", err)
			}
			defer func() { _ = capturer.Stop() }()

			log.Println("[listen] streaming pipeline active — listening… (Ctrl+C to stop)")

			// ── Run the streaming pipeline ─────────────────────────
			// ONNX CPU inference is much slower — use larger windows.
			pipeCfg := pipeline.DefaultConfig()
			if backend == "onnx" || backend == "nemotron" {
				pipeCfg = pipeline.ONNXConfig()
			}
			pipe := pipeline.NewStreamingPipeline(streamEngine, vad, pipeCfg)
			err = pipe.Run(ctx, audioCh, func(result core.StreamResult) {
				writeResult(result)
			})

			if outFile != nil {
				_ = outFile.Sync()
			}

			// context.Canceled is normal shutdown, not an error.
			if err == context.Canceled {
				return nil
			}
			return err
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "",
		"Write transcription output to this file (in addition to stdout)")
	cmd.Flags().StringVarP(&sourceFlag, "source", "s", "",
		"Audio source: mic (default), speaker (loopback/desktop audio), both")
	cmd.Flags().String("language", "",
		"Language code for transcription (e.g. tr, en, de). Empty = auto-detect")

	return cmd
}

// expandOutputPath expands ~ and resolves the output path.
func expandOutputPath(p string) string {
	return config.ExpandPath(p)
}
