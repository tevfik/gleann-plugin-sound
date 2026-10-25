package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
)

func newModelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Manage local speech recognition and VAD models",
		Long: `Inspect, download, and verify local models for gleann-plugin-sound.

Supported model families:
  Whisper GGML (whisper.cpp): tiny, base, small, medium, large-v3-turbo (+ quantized q5/q8)
  Whisper ONNX (ONNX Runtime): onnx-tiny, onnx-base, onnx-small, onnx-medium
  Nemotron ASR (streaming causal INT4): nemotron-3.5-asr-int4
  Neural VAD (Silero): silero-vad`,
	}

	cmd.AddCommand(
		newModelsListCmd(),
		newModelsDownloadCmd(),
		newModelsVerifyCmd(),
	)

	return cmd
}

func newModelsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all available and downloaded models",
		Run: func(cmd *cobra.Command, args []string) {
			models := config.AllAvailableModels()
			fmt.Printf("%-24s  %-10s  %-12s  %s\n", "NAME", "SIZE", "STATUS", "FILE / BUNDLE")
			fmt.Println(strings.Repeat("─", 80))

			installedCount := 0
			for _, m := range models {
				status := "available"
				if config.IsModelDownloaded(m.FileName) {
					status = "✓ downloaded"
					installedCount++
				}
				fmt.Printf("%-24s  %-10s  %-12s  %s\n", m.Name, m.Size, status, m.FileName)
			}

			fmt.Println(strings.Repeat("─", 80))
			fmt.Printf("Total: %d models (%d installed in %s)\n", len(models), installedCount, config.ModelsDir())
		},
	}
}

func newModelsDownloadCmd() *cobra.Command {
	var (
		flagDefault bool
		flagVAD     bool
		flagAll     bool
	)

	cmd := &cobra.Command{
		Use:   "download [model-name...]",
		Short: "Download speech recognition or VAD models into ~/.gleann/models",
		Long: `Download speech recognition or VAD models into ~/.gleann/models.

Examples:
  gleann-plugin-sound models download --default             # Downloads base.en + silero-vad
  gleann-plugin-sound models download base.en silero-vad
  gleann-plugin-sound models download small-q5_1
  gleann-plugin-sound models download onnx-base
  gleann-plugin-sound models download nemotron-3.5-asr-int4`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var targets []config.WhisperModel

			if flagAll {
				targets = config.AllAvailableModels()
			} else if flagDefault || (len(args) == 0 && !flagVAD) {
				// Default profile: base.en (or base) + Silero VAD
				if m := config.FindModel("base.en"); m != nil {
					targets = append(targets, *m)
				}
				targets = append(targets, config.SileroVADModel())
			} else {
				if flagVAD {
					targets = append(targets, config.SileroVADModel())
				}
				for _, name := range args {
					m := config.FindModel(name)
					if m == nil {
						return fmt.Errorf("unknown model %q (run 'gleann-plugin-sound models list' to see all available)", name)
					}
					targets = append(targets, *m)
				}
			}

			if len(targets) == 0 {
				fmt.Println("No models selected for download.")
				return nil
			}

			fmt.Printf("📦 Downloading %d model(s) into %s...\n\n", len(targets), config.ModelsDir())

			for _, m := range targets {
				fmt.Printf("==> Model: %s (%s)\n", m.DisplayName, m.Size)
				err := config.DownloadModelWithProgress(m, func(p config.DownloadProgress) {
					if p.Message != "" {
						fmt.Printf("\n  %s\n", p.Message)
					} else if p.TotalBytes > 0 {
						readMB := float64(p.BytesRead) / (1024 * 1024)
						totalMB := float64(p.TotalBytes) / (1024 * 1024)
						speedMB := p.SpeedBPS / (1024 * 1024)
						fmt.Printf("\r    [%d/%d] %s: %.1f%% (%.1f / %.1f MB) %.1f MB/s   ",
							p.FileIndex, p.TotalFiles, p.FileName, p.Percent, readMB, totalMB, speedMB)
					}
				})
				fmt.Println()
				if err != nil {
					return fmt.Errorf("failed downloading %s: %w", m.Name, err)
				}
			}

			fmt.Println("\n✓ All requested models downloaded successfully.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&flagDefault, "default", false, "Download default recommended models (base.en + silero-vad)")
	cmd.Flags().BoolVar(&flagVAD, "vad", false, "Download only Silero VAD model")
	cmd.Flags().BoolVar(&flagAll, "all", false, "Download all available models")

	return cmd
}

func newModelsVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Verify downloaded models on disk",
		Run: func(cmd *cobra.Command, args []string) {
			models := config.AllAvailableModels()
			modelsDir := config.ModelsDir()

			fmt.Printf("Verifying models in %s:\n\n", modelsDir)
			verified := 0
			for _, m := range models {
				path := config.ModelPath(m.FileName)
				info, err := os.Stat(path)
				if err != nil {
					continue
				}

				if info.IsDir() {
					// Check bundle
					filesFound := 0
					for _, bf := range m.BundleFiles {
						if _, err := os.Stat(filepath.Join(path, bf.Name)); err == nil {
							filesFound++
						}
					}
					if filesFound == len(m.BundleFiles) {
						fmt.Printf("  ✓ [bundle] %-20s  all %d files intact\n", m.Name, filesFound)
						verified++
					} else {
						fmt.Printf("  ⚠️ [bundle] %-20s  incomplete (%d/%d files present)\n", m.Name, filesFound, len(m.BundleFiles))
					}
				} else {
					sizeMB := float64(info.Size()) / (1024 * 1024)
					fmt.Printf("  ✓ %-20s  %.1f MB  (%s)\n", m.Name, sizeMB, m.FileName)
					verified++
				}
			}

			if verified == 0 {
				fmt.Println("No models downloaded yet. Run `gleann-plugin-sound models download --default` to fetch recommended models.")
			} else {
				fmt.Printf("\n✓ %d model(s) verified.\n", verified)
			}
		},
	}
}
