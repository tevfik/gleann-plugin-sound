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
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-sound/internal/audio"
	"github.com/tevfik/gleann-sound/internal/config"
	"github.com/tevfik/gleann-sound/internal/core"
)

// ── Listen-mode constants ──────────────────────────────────────

const (
	// sampleRate is Whisper's required sample rate.
	sampleRate = 16000

	// prePadSamples — how many samples of silence/context to keep BEFORE
	// speech onset so whisper has leading context.  0.3 s.
	prePadSamples = sampleRate * 3 / 10

	// trailingSilenceSamples — how many consecutive silence samples after
	// the last speech frame before we consider the utterance finished.  0.8 s.
	trailingSilenceSamples = sampleRate * 8 / 10

	// maxUtteranceSamples — hard cap on a single utterance buffer to avoid
	// unbounded growth.  We auto-flush at this length.  30 s.
	maxUtteranceSamples = sampleRate * 30

	// minUtteranceSamples — minimum utterance length to bother sending to
	// whisper.  Below this it's usually just a click/cough.  0.6 s.
	minUtteranceSamples = sampleRate * 6 / 10
)

// newListenCmd creates the "listen" subcommand (Mode 2: Live CLI Stream).
func newListenCmd() *cobra.Command {
	var outputFile string

	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Live microphone transcription streamed as JSON to stdout",
		Long: `Mode 2 — Live CLI Streaming.

Captures audio from the default input device, uses energy-based Voice Activity
Detection to detect utterance boundaries, and sends each complete utterance
through Whisper.  Results are written to stdout as JSON objects.
Optionally, output can also be written to a file with --output.

Press Ctrl+C to stop.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modelPath, _ := cmd.Flags().GetString("model")
			lang, _ := cmd.Flags().GetString("language")

			log.Println("[listen] initialising...")

			// ── Initialise components ──────────────────────────────
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

			capturer := audio.NewMalgoCapturer()
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

			// ── Utterance detection state ──────────────────────────
			// Instead of a fixed-interval timer, we detect complete
			// utterances: speech-start → speech-end (trailing silence).
			// This gives whisper coherent audio with natural boundaries.
			var (
				mu             sync.Mutex
				ringBuf        = make([]int16, 0, sampleRate*2)  // pre-speech ring buffer
				utterance      []int16                           // current utterance being built
				speaking       bool                              // currently inside an utterance
				silenceCount   int                               // consecutive silence samples after speech
				utteranceReady chan []int16                       // completed utterances to transcribe
			)
			utteranceReady = make(chan []int16, 4)

			// Start capturing — all audio goes through utterance detector.
			err = capturer.Start(ctx, func(pcmData []int16) {
				isSpeech := vad.IsSpeech(pcmData)

				mu.Lock()
				defer mu.Unlock()

				if !speaking {
					// ── Not in utterance ──
					// Keep a rolling pre-pad buffer for leading context.
					ringBuf = append(ringBuf, pcmData...)
					if len(ringBuf) > prePadSamples {
						ringBuf = ringBuf[len(ringBuf)-prePadSamples:]
					}

					if isSpeech {
						// Speech onset — start a new utterance with pre-pad.
						speaking = true
						silenceCount = 0
						utterance = make([]int16, 0, sampleRate*5)
						utterance = append(utterance, ringBuf...)
						utterance = append(utterance, pcmData...)
						ringBuf = ringBuf[:0]
						log.Println("[listen] 🎙  speech detected — recording utterance")
					}
				} else {
					// ── Inside utterance ──
					utterance = append(utterance, pcmData...)

					if isSpeech {
						silenceCount = 0
					} else {
						silenceCount += len(pcmData)
					}

					// Check: utterance complete (trailing silence) or hit max length?
					flushNow := false
					if silenceCount >= trailingSilenceSamples {
						// Trailing silence — utterance is complete.
						log.Printf("[listen] ⏹  end of utterance (%.1fs)",
							float64(len(utterance))/float64(sampleRate))
						flushNow = true
					} else if len(utterance) >= maxUtteranceSamples {
						// Hard cap — auto-flush to prevent unbounded growth.
						log.Printf("[listen] ⏹  max utterance length reached (%.0fs), auto-flushing",
							float64(len(utterance))/float64(sampleRate))
						flushNow = true
					}

					if flushNow {
						if len(utterance) >= minUtteranceSamples {
							// Send copy to transcription channel.
							chunk := make([]int16, len(utterance))
							copy(chunk, utterance)
							select {
							case utteranceReady <- chunk:
							default:
								log.Println("[listen] ⚠ transcription busy — dropping utterance")
							}
						} else {
							log.Println("[listen] utterance too short — skipping")
						}
						utterance = utterance[:0]
						speaking = false
						silenceCount = 0
					}
				}
			})
			if err != nil {
				return fmt.Errorf("failed to start audio capture: %w", err)
			}
			defer capturer.Stop()

			log.Println("[listen] listening… (Ctrl+C to stop)")

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

			writeSegment := func(seg core.Segment) {
				js := newJSONSegment(seg)
				_ = stdoutEnc.Encode(js)
				if outFile != nil {
					if outIsTxt {
						fmt.Fprintf(outFile, "%s\n", strings.TrimSpace(seg.Text))
					} else {
						json.NewEncoder(outFile).Encode(js)
					}
				}
			}

			transcribe := func(pcm []int16) {
				segments, err := engine.TranscribeStreamSegments(ctx, pcm)
				if err != nil {
					log.Printf("[listen] transcription error: %v", err)
					return
				}
				for _, seg := range segments {
					if seg.Text != "" {
						writeSegment(seg)
					}
				}
			}

			// ── Main loop: consume completed utterances ────────────
			for {
				select {
				case <-ctx.Done():
					// Flush any in-progress utterance.
					mu.Lock()
					remaining := utterance
					utterance = nil
					speaking = false
					mu.Unlock()

					if len(remaining) >= minUtteranceSamples {
						transcribe(remaining)
					}
					if outFile != nil {
						outFile.Sync()
					}
					return nil

				case chunk := <-utteranceReady:
					transcribe(chunk)
				}
			}
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "",
		"Write transcription output to this file (in addition to stdout)")
	cmd.Flags().String("language", "",
		"Language code for transcription (e.g. tr, en, de). Empty = auto-detect")

	return cmd
}

// expandOutputPath expands ~ and resolves the output path.
func expandOutputPath(p string) string {
	return config.ExpandPath(p)
}
