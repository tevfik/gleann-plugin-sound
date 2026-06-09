package main

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// newBenchmarkCmd creates the "benchmark" subcommand.
func newBenchmarkCmd() *cobra.Command {
	var filePath string
	var lang string

	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Benchmark transcription performance and measure real-time capability",
		Long: `Loads a model, processes a WAV/MP3 file, measures transcription execution time,
and calculates the Real-Time Factor (RTF = Processing Time / Audio Duration).

If RTF < 1.0, transcription is faster than real-time (can decode live microphone streams).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if filePath == "" {
				return fmt.Errorf("--file is required")
			}

			modelPath, _ := cmd.Flags().GetString("model")
			backend, _ := cmd.Flags().GetString("backend")

			if lang == "" {
				if cfg := config.Load(); cfg != nil && cfg.Language != "" {
					lang = cfg.Language
				}
			}

			fmt.Println("=== GLEANN SOUND BENCHMARK ===")
			fmt.Printf("[1/3] Decoding audio file: %s...\n", filePath)

			// ── 1. Decode audio via FFmpeg to calculate true audio duration ──
			startFFmpeg := time.Now()
			cmdFFmpeg := exec.CommandContext(cmd.Context(),
				"ffmpeg",
				"-i", filePath,
				"-ar", "16000",
				"-ac", "1",
				"-f", "s16le",
				"-acodec", "pcm_s16le",
				"-",
			)
			raw, err := cmdFFmpeg.Output()
			if err != nil {
				return fmt.Errorf("failed to decode audio file via FFmpeg (is it installed?): %w", err)
			}
			ffmpegDur := time.Since(startFFmpeg)

			nSamples := len(raw) / 2
			audioDuration := float64(nSamples) / 16000.0
			fmt.Printf("      ✓ Decoded %d samples (Duration: %.2fs, FFmpeg decode time: %v)\n", nSamples, audioDuration, ffmpegDur.Round(time.Millisecond))

			if audioDuration == 0 {
				return fmt.Errorf("audio duration is 0, cannot benchmark")
			}

			// ── 2. Load model and measure initialization time ──
			fmt.Printf("[2/3] Loading model (%s - %s)...\n", backend, modelPath)
			startInit := time.Now()
			engine, err := core.NewTranscriber(backend, modelPath)
			if err != nil {
				return fmt.Errorf("failed to load model: %w", err)
			}
			defer engine.Close()

			if lang != "" {
				engine.SetLanguage(lang)
			}
			initDur := time.Since(startInit)
			fmt.Printf("      ✓ Model loaded in %v\n", initDur.Round(time.Millisecond))

			// Convert raw PCM bytes to int16 samples
			pcm := make([]int16, nSamples)
			for i := 0; i < nSamples; i++ {
				pcm[i] = int16(uint16(raw[i*2]) | uint16(raw[i*2+1])<<8)
			}

			// ── 3. Run transcription and measure execution time ──
			fmt.Println("[3/3] Running transcription engine...")
			startTranscribe := time.Now()
			segments, err := engine.TranscribeStreamSegments(cmd.Context(), pcm)
			if err != nil {
				return fmt.Errorf("transcription failed: %w", err)
			}
			transcribeDur := time.Since(startTranscribe)

			// Combine transcribed segments
			var text string
			for _, s := range segments {
				text += s.Text + " "
			}

			// ── 4. Calculate metrics and print report ──
			rtf := transcribeDur.Seconds() / audioDuration

			fmt.Println("\n================ BENCHMARK REPORT ================")
			fmt.Printf("  Backend:             %s\n", backend)
			fmt.Printf("  Model:               %s\n", modelPath)
			fmt.Printf("  Language:            %s\n", lang)
			fmt.Printf("  Audio Duration:      %.2fs\n", audioDuration)
			fmt.Printf("  Model Load Time:     %v\n", initDur.Round(time.Millisecond))
			fmt.Printf("  Transcription Time:  %v\n", transcribeDur.Round(time.Millisecond))
			fmt.Printf("  Real-Time Factor:    %.3f (RTF)\n", rtf)

			// Print RTF Speed and Real-time Capability Status
			var status string
			if rtf < 0.5 {
				status = "⚡ Ultra Fast (Excellent real-time capability)"
			} else if rtf <= 1.0 {
				status = "✓ Real-Time Capable (Faster than real-time)"
			} else {
				status = "❌ Slow (Slower than real-time - might cause lag in live dictation)"
			}
			fmt.Printf("  Performance Rating:  %s\n", status)
			fmt.Println("--------------------------------------------------")
			fmt.Printf("  Clean Transcript:\n  %q\n", text)
			fmt.Println("==================================================")

			return nil
		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "Path to the audio/video file to benchmark")
	_ = cmd.MarkFlagRequired("file")
	cmd.Flags().StringVarP(&lang, "language", "l", "", "Language code for transcription (e.g. tr, en)")

	return cmd
}
