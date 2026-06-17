package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/audio"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/evaluate"
)

func newEvaluateCmd() *cobra.Command {
	var lang string

	cmd := &cobra.Command{
		Use:   "evaluate",
		Short: "Interactive dictation evaluation to measure WER/CER",
		Long: `Interactive dictation evaluation tool.
You will be prompted to type a reference text. 
Then, you press Enter to start recording, and press Enter again to stop.
The tool transcribes your speech and calculates the Word Error Rate (WER) and Character Error Rate (CER).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modelPath, _ := cmd.Flags().GetString("model")
			backend, _ := cmd.Flags().GetString("backend")

			cfg := config.Load()
			if modelPath == "" && cfg != nil && cfg.DefaultModel != "" {
				modelPath = cfg.DefaultModel
			}
			if backend == "" && cfg != nil && cfg.Backend != "" {
				backend = cfg.Backend
			}
			if backend == "" {
				backend = "whisper"
			}
			if lang == "" && cfg != nil && cfg.Language != "" {
				lang = cfg.Language
			}

			if modelPath == "" {
				return fmt.Errorf("model path not specified")
			}

			fmt.Println("=== GLEANN SOUND INTERACTIVE EVALUATION ===")
			fmt.Printf("Loading model: %s (backend: %s)\n", modelPath, backend)
			
			engine, err := core.NewTranscriber(backend, modelPath)
			if err != nil {
				return fmt.Errorf("failed to load model: %w", err)
			}
			defer engine.Close()

			if lang != "" {
				engine.SetLanguage(lang)
			}
			fmt.Println("✓ Model loaded and ready.")

			reader := bufio.NewReader(os.Stdin)

			for {
				fmt.Println("\n---------------------------------------------------------")
				fmt.Print("Enter REFERENCE text (or type 'exit' to quit): ")
				refText, err := reader.ReadString('\n')
				if err != nil {
					return err
				}
				refText = strings.TrimSpace(refText)
				if strings.ToLower(refText) == "exit" || strings.ToLower(refText) == "quit" {
					break
				}
				if refText == "" {
					continue
				}

				fmt.Print("Press [ENTER] to start recording...")
				_, _ = reader.ReadString('\n')

				fmt.Println("🔴 Recording... (Press [ENTER] to stop)")

				capturer := audio.NewMalgoCapturer()
				ctx, cancel := context.WithCancel(context.Background())

				var mu sync.Mutex
				var allPCM []int16

				err = capturer.Start(ctx, func(pcm []int16) {
					mu.Lock()
					allPCM = append(allPCM, pcm...)
					mu.Unlock()
				})
				if err != nil {
					cancel()
					fmt.Printf("Failed to start capture: %v\n", err)
					continue
				}

				// Wait for enter to stop
				_, _ = reader.ReadString('\n')
				cancel()
				_ = capturer.Stop()

				mu.Lock()
				samples := len(allPCM)
				pcmCopy := make([]int16, samples)
				copy(pcmCopy, allPCM)
				mu.Unlock()

				duration := float64(samples) / float64(audio.WhisperSampleRate)
				fmt.Printf("⏹️  Stopped recording. Captured %.2fs (%d samples)\n", duration, samples)

				if samples == 0 {
					fmt.Println("No audio captured.")
					continue
				}

				fmt.Println("Transcribing...")
				startTranscribe := time.Now()
				segments, err := engine.TranscribeStreamSegments(context.Background(), pcmCopy)
				if err != nil {
					fmt.Printf("Transcription error: %v\n", err)
					continue
				}
				transcribeDur := time.Since(startTranscribe)

				var hypText string
				for _, s := range segments {
					hypText += s.Text + " "
				}
				hypText = strings.TrimSpace(hypText)

				wer := evaluate.CalculateWER(refText, hypText)
				cer := evaluate.CalculateCER(refText, hypText)

				fmt.Printf("\n--- RESULTS ---\n")
				fmt.Printf("Reference  : %q\n", refText)
				fmt.Printf("Hypothesis : %q\n", hypText)
				fmt.Printf("Time taken : %v\n", transcribeDur)
				fmt.Printf("WER        : %.2f%%\n", wer*100)
				fmt.Printf("CER        : %.2f%%\n", cer*100)
			}

			fmt.Println("Evaluation complete. Exiting.")
			return nil
		},
	}

	cmd.Flags().StringVarP(&lang, "language", "l", "", "Language code for transcription (e.g. tr, en)")

	return cmd
}
